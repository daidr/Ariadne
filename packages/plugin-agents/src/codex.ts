import type { ModelRequest } from '@ariadne/plugin-sdk'
import { agentEnv, AgentProcess, AgentProcessError, splitCommand } from './process'
import { busyText, RpcPeer, Unhandled } from './rpc'
import type { AgentKind, AgentUsage, DriverContext } from './sessions'

export interface CodexSettings {
  command?: string
}

/**
 * Its own tools, switched off: it works only through the conversation's tools, which
 * Ariadne runs. Unknown names are ignored, so a version without one of them still starts.
 */
const OWN_TOOLS = [
  'shell_tool',
  'unified_exec',
  'view_image',
  'image_generation',
  'multi_agent',
  'apps',
  'plugins',
  'browser_use',
  'in_app_browser',
  'computer_use',
  'sleep_tool',
  'tool_suggest',
  'skill_search',
  'memories',
]

function argvOf(settings: CodexSettings): string[] {
  return [
    ...splitCommand(settings.command?.trim() || 'codex'),
    'app-server',
    '-c',
    'web_search="disabled"',
    // The user's own tool servers stay out of Ariadne's work.
    '-c',
    'mcp_servers={}',
    ...OWN_TOOLS.flatMap((feature) => ['-c', `features.${feature}=false`]),
  ]
}

interface Breakdown {
  inputTokens?: number
  cachedInputTokens?: number
  cacheWriteInputTokens?: number
  outputTokens?: number
  reasoningOutputTokens?: number
}

const dataUrl = (image: { mime: string; data: string }) => `data:${image.mime};base64,${image.data}`

/**
 * The Codex program as an app server: JSON-RPC on stdin and stdout. The conversation's
 * tools go to it as tools of the client, which it calls back for; its own are off, and
 * anything it still asks to approve (a command, a change to a file) is declined.
 */
export const codex: AgentKind<CodexSettings> = {
  kind: 'codex',
  title: { 'zh-CN': 'Codex', en: 'Codex' },
  settings: {
    type: 'object',
    properties: {
      command: {
        type: 'string',
        title: { 'zh-CN': '命令', en: 'Command' },
        description: {
          'zh-CN': '启动 Codex 的命令，用它自己登录的账号',
          en: 'The command that starts Codex; it uses the account it is signed in with',
        },
        default: 'codex',
      },
    },
  },
  describe(settings, request: ModelRequest) {
    return {
      argv: argvOf(settings),
      thread: {
        model: request.model,
        ...(request.effort ? { effort: request.effort } : {}),
        baseInstructions: '<system prompt>',
        dynamicTools: request.tools.map((tool) => tool.name),
      },
      messages: request.messages.length,
    }
  },
  create(ctx: DriverContext<CodexSettings>) {
    let threadId = ''
    let started = false
    let total: Breakdown = {}
    const proc = new AgentProcess({
      argv: argvOf(ctx.settings),
      cwd: ctx.cwd,
      env: agentEnv(),
      sandbox: ctx.sandbox,
      label: 'agent',
    })
    const peer = new RpcPeer(proc, false, {
      request: async (method, raw) => {
        const params = (raw ?? {}) as Record<string, unknown>
        switch (method) {
          case 'item/tool/call': {
            const output = await ctx.tools.call(String(params.tool ?? ''), params.arguments ?? {})
            return {
              contentItems: [
                { type: 'inputText', text: output.content },
                ...output.images.map((image) => ({ type: 'inputImage', imageUrl: dataUrl(image) })),
              ],
              success: !output.isError,
            }
          }
          case 'item/commandExecution/requestApproval':
          case 'item/fileChange/requestApproval':
            return { decision: 'decline' }
          case 'execCommandApproval':
          case 'applyPatchApproval':
            return { decision: { denied: { rejection: 'Use the tools you were given.' } } }
          case 'mcpServer/elicitation/request':
            return { action: 'decline', content: null, _meta: null }
          default:
            throw new Unhandled(`${method} is not supported`)
        }
      },
      notification: (method, raw) => {
        const params = (raw ?? {}) as Record<string, unknown>
        switch (method) {
          case 'item/completed': {
            const item = (params.item ?? {}) as {
              type?: string
              text?: string
              summary?: string[]
              content?: string[]
            }
            if (item.type === 'agentMessage' && item.text) {
              ctx.emit({ type: 'text', text: item.text })
            } else if (item.type === 'reasoning') {
              const text = [...(item.summary ?? []), ...(item.content ?? [])].join('\n').trim()
              if (text) ctx.emit({ type: 'reasoning', text })
            } else ctx.emit({ type: 'alive' })
            return
          }
          case 'thread/tokenUsage/updated': {
            const now = ((params.tokenUsage as { total?: Breakdown } | undefined)?.total ??
              {}) as Breakdown
            const grew = (field: keyof Breakdown) =>
              Math.max(0, (now[field] ?? 0) - (total[field] ?? 0))
            const usage: AgentUsage = {
              input: grew('inputTokens'),
              output: grew('outputTokens'),
              cached: grew('cachedInputTokens'),
              cacheWrite: grew('cacheWriteInputTokens'),
              reasoning: grew('reasoningOutputTokens'),
            }
            total = now
            if (usage.input || usage.output) ctx.emit({ type: 'usage', usage })
            return
          }
          case 'turn/completed': {
            const turn = (params.turn ?? {}) as {
              status?: string
              error?: { message?: string; additionalDetails?: string | null } | null
            }
            if (turn.status === 'completed') ctx.emit({ type: 'done' })
            else {
              const detail =
                [turn.error?.message, turn.error?.additionalDetails].filter(Boolean).join(': ') ||
                `the turn ended ${turn.status ?? 'unexpectedly'}`
              const busy = busyText(detail)
              ctx.emit({ type: 'failed', detail, retryable: busy, busy })
            }
            return
          }
          default:
            ctx.emit({ type: 'alive' })
        }
      },
    })

    return {
      async start() {
        proc.start(
          (line) => peer.take(line),
          (reason) => {
            peer.end(reason)
            ctx.emit({ type: 'failed', detail: reason, retryable: started })
          },
        )
        try {
          await peer.request('initialize', {
            clientInfo: { name: 'ariadne', title: 'Ariadne', version: '1.0.0' },
            capabilities: { experimentalApi: true },
          })
          peer.notify('initialized')
          const result = await peer.request<{ thread?: { id?: string } }>('thread/start', {
            ...(ctx.model && ctx.model !== 'default' ? { model: ctx.model } : {}),
            cwd: ctx.cwd,
            approvalPolicy: 'untrusted',
            sandbox: 'read-only',
            baseInstructions: ctx.system,
            ephemeral: true,
            dynamicTools: ctx.tools.specs.map((tool) => ({
              type: 'function',
              name: tool.name,
              description: tool.description,
              inputSchema: { type: 'object', ...tool.parameters },
            })),
          })
          threadId = result?.thread?.id ?? ''
          if (!threadId) throw new AgentProcessError('codex started no thread')
          started = true
        } catch (error) {
          await proc.close()
          const tail = proc.stderrTail().split('\n').slice(-3).join('\n')
          throw new AgentProcessError(
            `${error instanceof Error ? error.message : String(error)}${tail ? `: ${tail}` : ''}`,
          )
        }
      },
      async prompt(input) {
        await peer.request('turn/start', {
          threadId,
          input: [
            { type: 'text', text: input.text, text_elements: [] },
            ...input.images.map((image) => ({ type: 'image', url: dataUrl(image) })),
          ],
          ...(ctx.effort ? { effort: ctx.effort } : {}),
        })
      },
      async close() {
        peer.end('closed')
        await proc.close()
      },
    }
  },
}
