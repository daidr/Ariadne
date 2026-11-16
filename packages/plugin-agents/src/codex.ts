import {
  EFFORTS,
  type ChatMessage,
  type Effort,
  type ListedModel,
  type ModelRequest,
} from '@ariadne/plugin-sdk'
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { agentEnv, AgentProcess, AgentProcessError, exchange, splitCommand } from './process'
import { busyText, RpcError, RpcPeer, Unhandled } from './rpc'
import type { AgentKind, AgentUsage, DriverContext } from './sessions'
import { nativeFingerprint } from './history'

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
  'goals',
]

const LOCAL_CONTEXT = [
  'include_environment_context=false',
  'include_permissions_instructions=false',
  'include_apps_instructions=false',
  'skills.include_instructions=false',
  'skills.bundled.enabled=false',
  'project_doc_max_bytes=0',
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
    // Allows turn/settings/update while a dynamic tool call is waiting on Ariadne.
    '-c',
    'features.step_model_switching=true',
    ...OWN_TOOLS.flatMap((feature) => ['-c', `features.${feature}=false`]),
    // What it would tell the model about the machine it runs on (its directory, shell and
    // sandbox, its skills and the instructions files there): that is not where the work is.
    // The workspace, local or remote, is as the conversation's own instructions describe it.
    ...LOCAL_CONTEXT.flatMap((setting) => ['-c', setting]),
  ]
}

interface Breakdown {
  inputTokens?: number
  cachedInputTokens?: number
  cacheWriteInputTokens?: number
  outputTokens?: number
  reasoningOutputTokens?: number
}

/** Pages of its model list read at most, in case it never stops paging. */
const MAX_PAGES = 10

/** A model as its model list has it. */
interface CodexModel {
  id?: unknown
  model?: unknown
  displayName?: unknown
  supportedReasoningEfforts?: unknown
}

/** The efforts it lists for a model that Ariadne has too (not its `none` or `minimal`). */
function effortsOf(entry: CodexModel): ListedModel['efforts'] {
  const listed = entry.supportedReasoningEfforts
  if (!Array.isArray(listed)) return undefined
  const names = listed.map((item: unknown) =>
    typeof item === 'string'
      ? item
      : trimmed((item as { reasoningEffort?: unknown } | null)?.reasoningEffort),
  )
  return EFFORTS.filter((effort) => names.includes(effort))
}

const trimmed = (value: unknown) => (typeof value === 'string' ? value.trim() : '')

/** Its models as entries, the name it is asked for by and what it calls it. */
export function codexModels(listed: CodexModel[]): ListedModel[] {
  return listed.flatMap((entry) => {
    const id = trimmed(entry.model) || trimmed(entry.id)
    if (!id) return []
    const title = trimmed(entry.displayName)
    const efforts = effortsOf(entry)
    return [{ id, ...(title && title !== id ? { title } : {}), ...(efforts ? { efforts } : {}) }]
  })
}

/**
 * Minimum verified version for native history injection and environment-free threads.
 */
const OLDEST = [0, 167, 1]

/** Its version as the user agent from `initialize` says, when it is older than OLDEST. */
export function tooOld(userAgent: unknown): string | null {
  const found = /\/(\d+)\.(\d+)\.(\d+)/.exec(typeof userAgent === 'string' ? userAgent : '')
  if (!found) return null
  const version = found.slice(1).map(Number)
  for (let i = 0; i < OLDEST.length; i++) {
    if (version[i]! !== OLDEST[i]!) return version[i]! < OLDEST[i]! ? version.join('.') : null
  }
  return null
}

const CLIENT = { name: 'ariadne', title: 'Ariadne', version: '1.0.0' }

const dataUrl = (image: { mime: string; data: string }) => `data:${image.mime};base64,${image.data}`

/**
 * The Codex program as an app server: JSON-RPC on stdin and stdout. The conversation's
 * tools go to it as tools of the client, which it calls back for; its own are off, and
 * anything it still asks to approve (a command, a change to a file) is declined.
 */
export const codex: AgentKind<CodexSettings> = {
  kind: 'codex',
  persistent: true,
  mutableEffort: true,
  fingerprint: nativeFingerprint,
  title: { 'zh-CN': 'Codex', en: 'Codex' },
  signedIn({ env, home, which }) {
    return (
      !!which('codex') &&
      existsSync(join(env.CODEX_HOME?.trim() || join(home, '.codex'), 'auth.json'))
    )
  },
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
        default: 'codex',
      },
    },
  },
  // `default` stays unnamed: what it stands for is in the user's own configuration.
  listModels(settings, { cwd, signal }) {
    const proc = new AgentProcess({ argv: argvOf(settings), cwd, env: agentEnv(), label: 'agent' })
    const peer = new RpcPeer(proc, false, {
      request: async (method) => {
        throw new Unhandled(`${method} is not supported`)
      },
      notification: () => {},
    })
    return exchange(proc, signal, {
      onLine: (line) => peer.take(line),
      async run() {
        await peer.request('initialize', { clientInfo: CLIENT })
        peer.notify('initialized')
        const models: ListedModel[] = [{ id: 'default' }]
        let cursor: string | null = null
        for (let page = 0; page < MAX_PAGES; page++) {
          const result: { data?: CodexModel[]; nextCursor?: string | null } | undefined =
            await peer.request('model/list', cursor ? { cursor } : {})
          models.push(...codexModels(result?.data ?? []))
          cursor = result?.nextCursor || null
          if (!cursor) break
        }
        return models
      },
    })
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
    let defaultEffort: Effort | undefined
    let currentEffort: Effort | null = null
    let activeTurnId = ''
    let supportsActiveUpdates = true
    // Answers whose text came in pieces, so their whole copy is not handed on again.
    const streamed = new Set<string>()
    const proc = new AgentProcess({
      argv: argvOf(ctx.settings),
      cwd: ctx.cwd,
      env: agentEnv(),
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
          case 'item/agentMessage/delta': {
            const delta = typeof params.delta === 'string' ? params.delta : ''
            if (delta) {
              if (typeof params.itemId === 'string') streamed.add(params.itemId)
              ctx.emit({ type: 'text', text: delta })
            } else ctx.emit({ type: 'alive' })
            return
          }
          case 'item/completed': {
            const item = (params.item ?? {}) as {
              type?: string
              id?: string
              text?: string
              summary?: string[]
              content?: string[]
            }
            if (item.type === 'agentMessage' && item.text) {
              // Its pieces came already as they were written.
              if (item.id && streamed.delete(item.id)) ctx.emit({ type: 'alive' })
              else ctx.emit({ type: 'text', text: item.text })
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
              contextInput: (params.tokenUsage as { last?: Breakdown } | undefined)?.last
                ?.inputTokens,
            }
            total = now
            if (started && (usage.input || usage.output)) ctx.emit({ type: 'usage', usage })
            return
          }
          case 'turn/completed': {
            activeTurnId = ''
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
          const init = await peer.request<{ userAgent?: string }>('initialize', {
            clientInfo: CLIENT,
            capabilities: { experimentalApi: true },
          })
          const old = tooOld(init?.userAgent)
          if (old) {
            throw new AgentProcessError(
              `codex ${old} is too old: ${OLDEST.join('.')} or later is needed, update it first`,
            )
          }
          peer.notify('initialized')
          const saved = ctx.resume as
            { threadId?: string; total?: Breakdown; defaultEffort?: Effort } | undefined
          if (
            saved &&
            (typeof saved.threadId !== 'string' ||
              !saved.threadId ||
              !saved.total ||
              typeof saved.total !== 'object' ||
              Object.values(saved.total).some(
                (value) => typeof value !== 'number' || !Number.isFinite(value) || value < 0,
              ))
          ) {
            throw new AgentProcessError('invalid Codex resume checkpoint')
          }
          total = saved?.total ?? {}
          const result = await peer.request<{ thread?: { id?: string }; reasoningEffort?: Effort }>(
            saved?.threadId ? 'thread/resume' : 'thread/start',
            {
              ...(saved?.threadId ? { threadId: saved.threadId, excludeTurns: true } : {}),
              ...(ctx.model && ctx.model !== 'default' ? { model: ctx.model } : {}),
              cwd: ctx.cwd,
              approvalPolicy: 'untrusted',
              // What it asks to approve comes here to be declined, never to a reviewer of its own.
              approvalsReviewer: 'user',
              sandbox: 'read-only',
              baseInstructions: ctx.system,
              ...(!saved?.threadId ? { ephemeral: !ctx.persistent } : {}),
              // No environment of its own: whatever own tool it still offers (a command, a
              // patch, an image to view) has nowhere to run on this machine. The work happens
              // where the conversation's tools run, which may be another machine.
              environments: [],
              dynamicTools: ctx.tools.specs.map((tool) => ({
                type: 'function',
                name: tool.name,
                description: tool.description,
                inputSchema: { type: 'object', ...tool.parameters },
              })),
            },
          )
          threadId = result?.thread?.id ?? ''
          defaultEffort = saved ? saved.defaultEffort : result?.reasoningEffort
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
      async seed(messages: ChatMessage[]) {
        const last = messages.at(-1)
        const history = last?.role === 'user' ? messages.slice(0, -1) : messages
        const items = history.flatMap((message): unknown[] => {
          switch (message.role) {
            case 'system':
              return []
            case 'user':
              return [
                {
                  type: 'message',
                  role: 'user',
                  content: [
                    { type: 'input_text', text: message.content },
                    ...(message.images ?? []).map((image) => ({
                      type: 'input_image',
                      image_url: dataUrl(image),
                    })),
                  ],
                },
              ]
            case 'assistant':
              return [
                ...(message.content
                  ? [
                      {
                        type: 'message',
                        role: 'assistant',
                        content: [{ type: 'output_text', text: message.content }],
                      },
                    ]
                  : []),
                ...(message.toolCalls ?? []).map((call) => ({
                  type: 'function_call',
                  call_id: call.id,
                  name: call.name,
                  arguments: call.arguments,
                })),
              ]
            case 'tool':
              return [
                {
                  type: 'function_call_output',
                  call_id: message.toolCallId,
                  output: [
                    { type: 'input_text', text: message.content },
                    ...(message.images ?? []).map((image) => ({
                      type: 'input_image',
                      image_url: dataUrl(image),
                    })),
                  ],
                },
              ]
          }
        })
        if (items.length) await peer.request('thread/inject_items', { threadId, items })
        return last?.role === 'user'
          ? { text: last.content, images: last.images ?? [] }
          : {
              text: 'Continue from the latest tool results. All recorded tool calls have already run.',
              images: [],
            }
      },
      async prompt(input, options) {
        currentEffort = options?.effort ?? defaultEffort ?? null
        const result = await peer.request<{ turn?: { id?: string } }>('turn/start', {
          threadId,
          input: [
            { type: 'text', text: input.text, text_elements: [] },
            ...input.images.map((image) => ({ type: 'image', url: dataUrl(image) })),
          ],
          effort: currentEffort,
        })
        activeTurnId = result?.turn?.id ?? ''
      },
      async configure(options) {
        const effort = options.effort ?? defaultEffort ?? null
        if (activeTurnId && supportsActiveUpdates && effort !== currentEffort) {
          try {
            await peer.request('turn/settings/update', { threadId, turnId: activeTurnId, effort })
            currentEffort = effort
          } catch (error) {
            if (
              error instanceof RpcError &&
              error.code === -32600 &&
              (error.message.includes('only fallback metadata') ||
                error.message.includes('does not support reasoning effort updates'))
            ) {
              // Such models can still take effort on the next turn/start. Keep this
              // turn and its pending tool call intact instead of restarting history.
              supportsActiveUpdates = false
            } else throw error
          }
        }
      },
      checkpoint() {
        return { threadId, total, defaultEffort }
      },
      async close(options) {
        if (options?.discard && ctx.persistent && threadId && proc.running) {
          // Completed obsolete threads need not remain in the user's active thread list.
          await Promise.race([
            peer.request('thread/archive', { threadId }).catch(() => {}),
            Bun.sleep(500),
          ])
        }
        peer.end('closed')
        await proc.close()
      },
    }
  },
}
