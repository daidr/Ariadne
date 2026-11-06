import type { ModelRequest } from '@ariadne/plugin-sdk'
import { fileURLToPath } from 'node:url'
import { SERVER_NAME } from './bridge'
import { agentEnv, AgentProcess, AgentProcessError, splitCommand } from './process'
import { busyText, RpcError, RpcPeer, Unhandled } from './rpc'
import type { AgentKind, DriverContext } from './sessions'

const noop = () => {}

export interface AcpSettings {
  command?: string
}

/** The protocol version this client speaks. */
const PROTOCOL_VERSION = 1
/** The small program that hands a tool server over stdio to the bridge's address. */
const STDIO_SHIM = fileURLToPath(new URL('./mcp-stdio.ts', import.meta.url))

interface PermissionOption {
  optionId: string
  kind?: string
}

/**
 * Whether a permission the agent asks for is for one of the conversation's tools. Those
 * Ariadne runs and confirms itself; anything else is the agent's own tool and is refused.
 */
export function forOwnTools(toolCall: unknown): boolean {
  return new RegExp(`\\b${SERVER_NAME}\\b`, 'i').test(JSON.stringify(toolCall ?? {}))
}

/**
 * Any agent that speaks the Agent Client Protocol: JSON-RPC on its stdin and stdout. It
 * gets no file system or terminal from Ariadne, the conversation's tools as a tool server,
 * and every permission it asks for its own tools is refused. The protocol has no system
 * prompt, so the instructions open the first message.
 */
export const acp: AgentKind<AcpSettings> = {
  kind: 'acp',
  title: { 'zh-CN': 'ACP agent', en: 'ACP agent' },
  settings: {
    type: 'object',
    properties: {
      command: {
        type: 'string',
        title: { 'zh-CN': '命令', en: 'Command' },
        description: {
          'zh-CN': '以 ACP 方式启动 agent 的命令，例如它的 --acp 模式',
          en: 'The command that starts the agent in its ACP mode',
        },
      },
    },
    required: ['command'],
  },
  describe(settings, request: ModelRequest) {
    return {
      argv: splitCommand(settings.command ?? ''),
      session: { model: request.model, mcpServers: request.tools.length ? [SERVER_NAME] : [] },
      tools: request.tools.map((tool) => tool.name),
      messages: request.messages.length,
    }
  },
  create(ctx: DriverContext<AcpSettings>) {
    const argv = splitCommand(ctx.settings.command ?? '')
    let sessionId = ''
    let started = false
    let first = true
    let images = false
    let thought = ''
    let removeTools: () => void = noop
    const proc = new AgentProcess({
      argv,
      cwd: ctx.cwd,
      env: agentEnv(),
      sandbox: ctx.sandbox,
      label: 'agent',
    })
    const flushThought = () => {
      if (thought.trim()) ctx.emit({ type: 'reasoning', text: thought.trim() })
      thought = ''
    }
    const peer = new RpcPeer(proc, true, {
      request: async (method, raw) => {
        const params = (raw ?? {}) as { toolCall?: unknown; options?: PermissionOption[] }
        if (method === 'session/request_permission') {
          const options = params.options ?? []
          const own = forOwnTools(params.toolCall)
          const pick = own
            ? (options.find((o) => o.kind === 'allow_once') ??
              options.find((o) => o.kind?.startsWith('allow')))
            : (options.find((o) => o.kind === 'reject_once') ??
              options.find((o) => o.kind?.startsWith('reject')))
          return pick
            ? { outcome: { outcome: 'selected', optionId: pick.optionId } }
            : { outcome: { outcome: 'cancelled' } }
        }
        throw new Unhandled(`${method} is not supported`)
      },
      notification: (method, raw) => {
        if (method !== 'session/update') return
        const update = ((raw ?? {}) as { update?: Record<string, unknown> }).update ?? {}
        const content = update.content as { type?: string; text?: string } | undefined
        if (update.sessionUpdate === 'agent_thought_chunk') {
          if (content?.type === 'text' && content.text) thought += content.text
          ctx.emit({ type: 'alive' })
          return
        }
        flushThought()
        if (update.sessionUpdate === 'agent_message_chunk' && content?.type === 'text') {
          if (content.text) ctx.emit({ type: 'text', text: content.text })
          return
        }
        ctx.emit({ type: 'alive' })
      },
    })

    return {
      async start() {
        if (!argv.length) throw new AgentProcessError('no command is set for the agent')
        proc.start(
          (line) => peer.take(line),
          (reason) => {
            peer.end(reason)
            ctx.emit({ type: 'failed', detail: reason, retryable: started })
          },
        )
        try {
          const init = await peer.request<{
            agentCapabilities?: {
              promptCapabilities?: { image?: boolean }
              mcpCapabilities?: { http?: boolean }
            }
          }>('initialize', {
            protocolVersion: PROTOCOL_VERSION,
            clientCapabilities: {
              fs: { readTextFile: false, writeTextFile: false },
              terminal: false,
            },
            clientInfo: { name: 'ariadne', title: 'Ariadne', version: '1.0.0' },
          })
          images = !!init?.agentCapabilities?.promptCapabilities?.image
          const mcpServers = []
          if (ctx.tools.specs.length) {
            const tools = ctx.bridge.add({ tools: () => ctx.tools.specs, call: ctx.tools.call })
            removeTools = tools.remove
            mcpServers.push(
              init?.agentCapabilities?.mcpCapabilities?.http
                ? { type: 'http', name: SERVER_NAME, url: tools.url, headers: [] }
                : {
                    name: SERVER_NAME,
                    command: process.execPath,
                    args: [STDIO_SHIM, tools.url],
                    env: [],
                  },
            )
          }
          const session = await peer.request<{
            sessionId?: string
            models?: { availableModels?: { modelId: string }[] }
          }>('session/new', { cwd: ctx.cwd, mcpServers })
          sessionId = session?.sessionId ?? ''
          if (!sessionId) throw new AgentProcessError('the agent opened no session')
          const model = ctx.model.trim()
          if (
            model &&
            model !== 'default' &&
            session.models?.availableModels?.some((m) => m.modelId === model)
          ) {
            await peer.request('session/set_model', { sessionId, modelId: model }).catch(() => {})
          }
          started = true
        } catch (error) {
          removeTools()
          await proc.close()
          const tail = proc.stderrTail().split('\n').slice(-3).join('\n')
          const message =
            error instanceof RpcError && error.code === -32000
              ? `the agent asks to sign in first (run it once in a terminal): ${error.message}`
              : error instanceof Error
                ? error.message
                : String(error)
          throw new AgentProcessError(`${message}${tail ? `: ${tail}` : ''}`)
        }
      },
      async prompt(input) {
        const text = first
          ? `<instructions>\n${ctx.system}\n</instructions>\n\n${input.text}`
          : input.text
        first = false
        const prompt = [
          { type: 'text', text },
          ...(images
            ? input.images.map((image) => ({
                type: 'image',
                mimeType: image.mime,
                data: image.data,
              }))
            : []),
        ]
        // The answer comes when the turn is over; until then its updates stream in.
        void peer
          .request<{
            stopReason?: string
            usage?: {
              inputTokens?: number
              outputTokens?: number
              cachedReadTokens?: number
              cachedWriteTokens?: number
              thoughtTokens?: number
            }
          }>('session/prompt', { sessionId, prompt })
          .then(
            (result) => {
              flushThought()
              const usage = result?.usage
              if (usage) {
                ctx.emit({
                  type: 'usage',
                  usage: {
                    input: usage.inputTokens ?? 0,
                    output: usage.outputTokens ?? 0,
                    cached: usage.cachedReadTokens ?? 0,
                    cacheWrite: usage.cachedWriteTokens ?? 0,
                    reasoning: usage.thoughtTokens ?? 0,
                  },
                })
              }
              if (result?.stopReason === 'refusal') {
                ctx.emit({ type: 'failed', detail: 'the agent refused to go on', retryable: false })
              } else if (result?.stopReason === 'cancelled') {
                ctx.emit({
                  type: 'failed',
                  detail: 'the agent cancelled the turn',
                  retryable: false,
                })
              } else ctx.emit({ type: 'done' })
            },
            (error: unknown) => {
              const detail = error instanceof Error ? error.message : String(error)
              const busy = busyText(detail)
              ctx.emit({ type: 'failed', detail, retryable: busy, busy })
            },
          )
      },
      async close() {
        if (sessionId) peer.notify('session/cancel', { sessionId })
        removeTools()
        peer.end('closed')
        await proc.close()
      },
    }
  },
}
