import type { ModelRequest } from '@ariadne/plugin-sdk'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { SERVER_NAME } from './bridge'
import { agentEnv, AgentProcess, splitCommand } from './process'
import { busyText } from './rpc'
import type { AgentKind, AgentUsage, DriverContext } from './sessions'

const noop = () => {}

export interface ClaudeCodeSettings {
  command?: string
}

/** How long it may wait on one tool call: as long as the user takes to approve it. */
const TOOL_TIMEOUT_MS = String(24 * 60 * 60 * 1000)

/** The command line, before the system prompt and tool server are filled in. */
function argvOf(settings: ClaudeCodeSettings, model: string, effort?: string): string[] {
  const command = splitCommand(settings.command?.trim() || 'claude')
  return [
    ...command,
    '-p',
    '--input-format',
    'stream-json',
    '--output-format',
    'stream-json',
    '--verbose',
    ...(model && model !== 'default' ? ['--model', model] : []),
    ...(effort ? ['--effort', effort] : []),
    // None of its own tools: it works only through the conversation's, which Ariadne runs.
    '--tools',
    '',
    '--allowedTools',
    `mcp__${SERVER_NAME}`,
    '--permission-mode',
    'dontAsk',
    '--strict-mcp-config',
    // The user's own settings, hooks and commands for it stay out of Ariadne's work.
    '--setting-sources',
    '',
    '--settings',
    JSON.stringify({ disableAllHooks: true }),
    '--disable-slash-commands',
    '--no-session-persistence',
  ]
}

interface ContentBlock {
  type?: string
  text?: string
  thinking?: string
}

interface ApiUsage {
  input_tokens?: number
  output_tokens?: number
  cache_read_input_tokens?: number
  cache_creation_input_tokens?: number
  output_tokens_details?: { thinking_tokens?: number }
}

const tokens = (usage: ApiUsage): AgentUsage => {
  const cached = usage.cache_read_input_tokens ?? 0
  const written = usage.cache_creation_input_tokens ?? 0
  return {
    input: (usage.input_tokens ?? 0) + cached + written,
    output: usage.output_tokens ?? 0,
    cached,
    cacheWrite: written,
    reasoning: usage.output_tokens_details?.thinking_tokens ?? 0,
  }
}

const FIELDS = ['input', 'output', 'cached', 'cacheWrite', 'reasoning'] as const

/**
 * The Claude Code program in print mode, talking JSON lines both ways: it keeps one
 * conversation while it runs, takes each new message on stdin and prints what it does.
 * Its own tools are off; the conversation's tools reach it as a tool server.
 */
export const claudeCode: AgentKind<ClaudeCodeSettings> = {
  kind: 'claude-code',
  title: { 'zh-CN': 'Claude Code', en: 'Claude Code' },
  settings: {
    type: 'object',
    properties: {
      command: {
        type: 'string',
        title: { 'zh-CN': '命令', en: 'Command' },
        description: {
          'zh-CN': '用它已登录的账号',
          en: 'Uses the account it is signed in with',
        },
        default: 'claude',
      },
    },
  },
  describe(settings, request: ModelRequest) {
    return {
      argv: [
        ...argvOf(settings, request.model, request.effort),
        '--system-prompt-file',
        '<system prompt>',
        ...(request.tools.length ? ['--mcp-config', '<tool server>'] : []),
      ],
      tools: request.tools.map((tool) => tool.name),
      messages: request.messages.length,
    }
  },
  create(ctx: DriverContext<ClaudeCodeSettings>) {
    const systemFile = join(ctx.cwd, 'system.md')
    const mcpFile = join(ctx.cwd, 'mcp.json')
    let removeTools: () => void = noop
    let sawOutput = false
    // Each message's usage as last seen: it is printed before the message is whole, so
    // only what grew is counted, and the turn's total in its result settles the rest.
    const seen = new Map<string, AgentUsage>()
    let turn: Required<Omit<AgentUsage, 'costUsd'>> = {
      input: 0,
      output: 0,
      cached: 0,
      cacheWrite: 0,
      reasoning: 0,
    }
    let costSoFar = 0
    const count = (now: AgentUsage, before: AgentUsage | undefined) => {
      const grew: AgentUsage = { input: 0, output: 0 }
      for (const field of FIELDS) {
        const more = Math.max(0, (now[field] ?? 0) - (before?.[field] ?? 0))
        grew[field] = more
        turn[field] += more
      }
      if (FIELDS.some((field) => grew[field])) ctx.emit({ type: 'usage', usage: grew })
    }
    const proc = new AgentProcess({
      argv: [
        ...argvOf(ctx.settings, ctx.model, ctx.effort),
        '--system-prompt-file',
        systemFile,
        ...(ctx.tools.specs.length ? ['--mcp-config', mcpFile] : []),
      ],
      cwd: ctx.cwd,
      env: agentEnv({ MCP_TOOL_TIMEOUT: TOOL_TIMEOUT_MS, MAX_MCP_OUTPUT_TOKENS: '200000' }),
      sandbox: ctx.sandbox,
      label: 'agent',
    })

    const onLine = (value: unknown) => {
      sawOutput = true
      const line = value as {
        type?: string
        subtype?: string
        is_error?: boolean
        result?: string
        errors?: string[]
        total_cost_usd?: number
        usage?: ApiUsage
        message?: { id?: string; content?: ContentBlock[] | string; usage?: ApiUsage }
      }
      if (line.type === 'assistant' && line.message) {
        const content = Array.isArray(line.message.content) ? line.message.content : []
        for (const block of content) {
          if (block.type === 'text' && block.text) ctx.emit({ type: 'text', text: block.text })
          else if (block.type === 'thinking' && block.thinking) {
            ctx.emit({ type: 'reasoning', text: block.thinking })
          }
        }
        const id = line.message.id
        if (id && line.message.usage) {
          const now = tokens(line.message.usage)
          count(now, seen.get(id))
          seen.set(id, now)
        }
        ctx.emit({ type: 'alive' })
        return
      }
      if (line.type === 'result') {
        if (line.usage) {
          const total = tokens(line.usage)
          count(total, { ...turn })
        }
        turn = { input: 0, output: 0, cached: 0, cacheWrite: 0, reasoning: 0 }
        seen.clear()
        if (typeof line.total_cost_usd === 'number') {
          // The total so far in this process; a smaller one started over.
          const cost =
            line.total_cost_usd >= costSoFar ? line.total_cost_usd - costSoFar : line.total_cost_usd
          costSoFar = line.total_cost_usd
          if (cost > 0) ctx.emit({ type: 'usage', usage: { input: 0, output: 0, costUsd: cost } })
        }
        if (line.is_error || (line.subtype && line.subtype !== 'success')) {
          const detail =
            (typeof line.result === 'string' && line.result) ||
            line.errors?.join('; ') ||
            line.subtype ||
            'error'
          const busy = busyText(detail)
          ctx.emit({ type: 'failed', detail, retryable: busy, busy })
        } else {
          ctx.emit({ type: 'done' })
        }
        return
      }
      ctx.emit({ type: 'alive' })
    }

    return {
      async start() {
        writeFileSync(systemFile, ctx.system, { mode: 0o600 })
        if (ctx.tools.specs.length) {
          const tools = ctx.bridge.add({ tools: () => ctx.tools.specs, call: ctx.tools.call })
          removeTools = tools.remove
          writeFileSync(
            mcpFile,
            JSON.stringify({ mcpServers: { [SERVER_NAME]: { type: 'http', url: tools.url } } }),
            { mode: 0o600 },
          )
        }
        proc.start(onLine, (reason) =>
          // Exiting before it said anything is a setup problem; trying again will not help.
          ctx.emit({ type: 'failed', detail: reason, retryable: sawOutput }),
        )
      },
      async prompt(input) {
        proc.send({
          type: 'user',
          message: {
            role: 'user',
            content: [
              { type: 'text', text: input.text },
              ...input.images.map((image) => ({
                type: 'image',
                source: { type: 'base64', media_type: image.mime, data: image.data },
              })),
            ],
          },
          parent_tool_use_id: null,
          session_id: 'default',
        })
      },
      async close() {
        removeTools()
        await proc.close()
      },
    }
  },
}
