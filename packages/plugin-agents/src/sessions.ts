import type {
  ChatMessage,
  Effort,
  FindContext,
  LocalizedText,
  ModelProvider,
  ModelRequest,
  ModelStreamPart,
  SettingsSchema,
  ToolSpec,
} from '@ariadne/plugin-sdk'
import { createHash, randomUUID } from 'node:crypto'
import { mkdirSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import type { ToolBridge, ToolOutput } from './bridge'
import { splitCommand } from './process'
import {
  fingerprint,
  joinInput,
  renderHistory,
  systemHash,
  updatedInstructions,
  type AgentInput,
} from './history'

/**
 * A step ends once the agent has called tools and then says nothing more for this long:
 * calls it makes side by side arrive within it, and it waits on their results.
 */
export const QUIET_MS = 150
/** A session nobody has used for this long is closed: its program ends. */
export const IDLE_MS = 20 * 60 * 1000
/** Live sessions per kind at most; the least recently used idle one goes first. */
const MAX_SESSIONS = 16

/** Tokens of one model call of the agent, as `usage` parts count them. */
export interface AgentUsage {
  input: number
  output: number
  cached?: number
  cacheWrite?: number
  reasoning?: number
  costUsd?: number
}

/** What an agent program does, as a driver reports it. */
export type AgentEvent =
  | { type: 'text'; text: string }
  /** It began a tool call: the text so far is not its answer. */
  | { type: 'calling' }
  | { type: 'reasoning'; text: string }
  | { type: 'usage'; usage: AgentUsage }
  /** It sent something with nothing to hand on: it is still working. */
  | { type: 'alive' }
  /** Its turn is over: the last text was its answer. */
  | { type: 'done' }
  | { type: 'failed'; detail: string; retryable: boolean; busy?: boolean }

type QueuedEvent = AgentEvent | { type: 'call'; id: string; name: string; arguments: string }

/** The conversation's tools, as a driver offers them to its agent. */
export interface AgentTools {
  specs: ToolSpec[]
  /** Hand a call to the conversation; it settles once the call has run there. */
  call(name: string, args: unknown): Promise<ToolOutput>
}

export interface DriverContext<Settings> {
  settings: Settings
  model: string
  effort?: Effort
  system: string
  tools: AgentTools
  /** An empty directory of this session's own, for the agent to start in. */
  cwd: string
  bridge: ToolBridge
  emit(event: AgentEvent): void
}

/** One agent program running one session. */
export interface Driver {
  /** Start the program and its session; throws with a reason for the user. */
  start(): Promise<void>
  /** Begin a turn with this input. */
  prompt(input: AgentInput): Promise<void>
  close(): Promise<void>
}

/** A kind of agent program, made into a model provider by `agentProvider`. */
export interface AgentKind<Settings> {
  kind: string
  title: LocalizedText
  settings: SettingsSchema
  /**
   * The model names it takes besides `default` (whatever the program itself is set to),
   * when they are the same for every account.
   */
  models?: string[]
  /**
   * Whether the program is on this machine and signed in, as far as files and the
   * environment tell: it is then offered on first run.
   */
  signedIn?(context: FindContext): boolean
  create(context: DriverContext<Settings>): Driver
  /** What starting it looks like, for the trace: the program, its options, the tools. */
  describe(settings: Settings, request: ModelRequest): unknown
}

/** The program a kind starts when the user names none. */
const PROGRAM = <Settings>(agent: AgentKind<Settings>): string => {
  const command = agent.settings.properties.command
  return (command?.type === 'string' && command.default) || agent.kind
}

class EventQueue {
  private readonly items: QueuedEvent[] = []
  private wake: (() => void) | null = null

  push(event: QueuedEvent) {
    this.items.push(event)
    this.wake?.()
  }

  /** The next event; `timeout` when none comes in `wait` ms, `aborted` when `signal` is. */
  async next(
    wait: number | null,
    signal?: AbortSignal,
  ): Promise<QueuedEvent | 'timeout' | 'aborted'> {
    for (;;) {
      if (signal?.aborted) return 'aborted'
      const item = this.items.shift()
      if (item) return item
      const outcome = await new Promise<'event' | 'timeout' | 'aborted'>((resolve) => {
        let timer: ReturnType<typeof setTimeout> | undefined
        const done = (value: 'event' | 'timeout' | 'aborted') => {
          clearTimeout(timer)
          signal?.removeEventListener('abort', onAbort)
          this.wake = null
          resolve(value)
        }
        const onAbort = () => done('aborted')
        this.wake = () => done('event')
        if (wait !== null) timer = setTimeout(() => done('timeout'), wait)
        signal?.addEventListener('abort', onAbort, { once: true })
      })
      if (outcome !== 'event') return outcome
    }
  }
}

class Session {
  /** The messages it has seen, as fingerprints, its own replies included. */
  fps: string[] = []
  system = ''
  /** Calls of its last step, waiting on their results, in order. */
  awaiting: string[] = []
  readonly pending = new Map<string, (output: ToolOutput) => void>()
  readonly queue = new EventQueue()
  busy = false
  closed = false
  lastUsed = Date.now()
  driver!: Driver

  constructor(
    readonly key: string | null,
    readonly config: string,
    readonly dir: string,
  ) {}

  async close() {
    if (this.closed) return
    this.closed = true
    for (const resolve of this.pending.values()) {
      resolve({ content: 'The session ended before the call ran.', isError: true, images: [] })
    }
    this.pending.clear()
    try {
      await this.driver?.close()
    } finally {
      rmSync(this.dir, { recursive: true, force: true })
    }
  }
}

const hash = (value: unknown) =>
  createHash('sha256').update(JSON.stringify(value)).digest('hex').slice(0, 24)

const finishError = (detail: string, retryable: boolean, busy = false): ModelStreamPart => ({
  type: 'finish',
  reason: 'error',
  error: detail,
  ...(retryable ? { retryable: true } : {}),
  ...(busy ? { busy: true } : {}),
})

/** A provider made of an agent program, and what to call to end its sessions. */
export interface AgentProvider<Settings> extends ModelProvider<Settings> {
  dispose(): Promise<void>
  /** Live sessions, for tests. */
  sessions(): number
}

/**
 * A model provider run by an agent program. The program keeps its own conversation, so a
 * session stays open per Ariadne conversation and each request hands it only what is new:
 * the results of the calls it waits on, or the messages that came after its answer. The
 * agent calls the conversation's tools, which Ariadne runs as usual (so they are traced
 * and confirmed like any model's); a step ends when it waits on calls, and its
 * results go back to it with the next request. A request that does not go on from what the
 * session saw (history summarised, a restart, an edit) starts a new session with all of it.
 */
export function agentProvider<Settings>(
  agent: AgentKind<Settings>,
  options: { dataDir: string; bridge: ToolBridge },
): AgentProvider<Settings> {
  const pool = new Map<string, Session>()
  const reaper = setInterval(() => {
    const now = Date.now()
    for (const [key, session] of pool) {
      if (!session.busy && now - session.lastUsed > IDLE_MS) {
        pool.delete(key)
        void session.close()
      }
    }
  }, 60_000)
  reaper.unref?.()

  const drop = async (session: Session) => {
    if (session.key && pool.get(session.key) === session) pool.delete(session.key)
    await session.close()
  }

  async function open(
    settings: Settings,
    request: ModelRequest,
    system: string,
    config: string,
  ): Promise<Session> {
    const dir = join(options.dataDir, 'sessions', randomUUID())
    mkdirSync(dir, { recursive: true, mode: 0o700 })
    const session = new Session(request.conversation ?? null, config, dir)
    const tools: AgentTools = {
      specs: request.tools,
      call: (name, args) =>
        new Promise<ToolOutput>((resolve) => {
          if (session.closed) {
            resolve({ content: 'The session has ended.', isError: true, images: [] })
            return
          }
          const id = `call_${randomUUID().replaceAll('-', '').slice(0, 20)}`
          session.pending.set(id, resolve)
          session.queue.push({
            type: 'call',
            id,
            name,
            arguments: JSON.stringify(args ?? {}),
          })
        }),
    }
    session.driver = agent.create({
      settings,
      model: request.model,
      ...(request.effort ? { effort: request.effort } : {}),
      system,
      tools,
      cwd: dir,
      bridge: options.bridge,
      emit: (event) => session.queue.push(event),
    })
    session.system = system
    try {
      await session.driver.start()
    } catch (error) {
      await session.close()
      throw error
    }
    if (session.key) {
      if (pool.size >= MAX_SESSIONS) {
        const idle = [...pool.values()]
          .filter((s) => !s.busy)
          .toSorted((a, b) => a.lastUsed - b.lastUsed)
        if (idle[0]) await drop(idle[0])
      }
      pool.set(session.key, session)
    }
    return session
  }

  /**
   * How the request goes on in `session`, or `null` when it does not: it must hold what the
   * session saw, then the results of the calls the session waits on (and nothing else from
   * the model), and then only messages for it.
   */
  function continuation(
    session: Session,
    rest: ChatMessage[],
    fps: string[],
    system: string,
  ): (() => Promise<void>) | null {
    const seen = session.fps
    if (fps.length <= seen.length) return null
    for (let i = 0; i < seen.length; i++) if (fps[i] !== seen[i]) return null
    const tail = rest.slice(seen.length)
    if (tail.some((m) => m.role === 'assistant' || m.role === 'system')) return null
    const results = tail.filter(
      (m): m is Extract<ChatMessage, { role: 'tool' }> => m.role === 'tool',
    )
    const inputs = tail.filter((m) => m.role === 'user')
    const changed = systemHash(system) !== systemHash(session.system)
    const prefix = changed ? updatedInstructions(system) : null
    session.system = system
    if (!session.awaiting.length) {
      if (results.length || !inputs.length) return null
      const input = joinInput(inputs, prefix)
      return () => session.driver.prompt(input)
    }
    const byId = new Map(results.map((m) => [m.toolCallId, m]))
    if (results.length !== session.awaiting.length) return null
    if (!session.awaiting.every((id) => byId.has(id))) return null
    const awaiting = session.awaiting
    return async () => {
      const extra = inputs.length || prefix ? joinInput(inputs, prefix) : null
      awaiting.forEach((id, index) => {
        const result = byId.get(id)!
        const resolve = session.pending.get(id)
        session.pending.delete(id)
        let content = result.content
        const images = [...(result.images ?? [])]
        // What came in meanwhile reaches the agent with the last result: it is waiting on
        // these, and cannot take a message in between.
        if (extra && index === awaiting.length - 1) {
          content = `${content}\n\n<arrived_meanwhile>\n${extra.text}\n</arrived_meanwhile>`
          images.push(...extra.images)
        }
        resolve?.({ content, isError: !!result.isError, images })
      })
    }
  }

  return {
    kind: agent.kind,
    title: agent.title,
    settings: agent.settings,
    program: true,
    models: async () => ['default', ...(agent.models ?? [])],
    async check(settings) {
      const [program] = splitCommand(String((settings as { command?: unknown }).command ?? ''))
      return !program || Bun.which(program)
        ? null
        : { 'zh-CN': `找不到命令 ${program}`, en: `command not found: ${program}` }
    },
    find(context) {
      return agent.signedIn?.(context)
        ? [{ id: agent.kind, from: { program: PROGRAM(agent) }, settings: {} as Settings }]
        : []
    },
    requestBody: (settings, request) => agent.describe(settings, request),
    sessions: () => pool.size,
    async dispose() {
      clearInterval(reaper)
      const all = [...pool.values()]
      pool.clear()
      await Promise.all(all.map((session) => session.close()))
    },
    async *stream(settings, request) {
      const first = request.messages[0]
      const system = first?.role === 'system' ? first.content : ''
      const rest = first?.role === 'system' ? request.messages.slice(1) : request.messages
      const fps = rest.map(fingerprint)
      const config = hash([
        agent.kind,
        settings,
        request.model,
        request.effort ?? null,
        request.tools,
      ])
      let session = request.conversation ? pool.get(request.conversation) : undefined
      let deliver: (() => Promise<void>) | null = null
      if (session) {
        deliver =
          !session.busy && !session.closed && session.config === config
            ? continuation(session, rest, fps, system)
            : null
        if (!deliver) {
          await drop(session)
          session = undefined
        }
      }
      if (!session) {
        try {
          session = await open(settings, request, system, config)
        } catch (error) {
          yield finishError(error instanceof Error ? error.message : String(error), false)
          return
        }
        const input = renderHistory(rest)
        const opened = session
        deliver = () => opened.driver.prompt(input)
      }
      session.fps = fps
      session.busy = true
      session.lastUsed = Date.now()
      const calls: { id: string; name: string; arguments: string }[] = []
      const usage: Required<Omit<AgentUsage, 'costUsd'>> & { costUsd?: number } = {
        input: 0,
        output: 0,
        cached: 0,
        cacheWrite: 0,
        reasoning: 0,
      }
      let counted = false
      const usagePart = (): ModelStreamPart[] =>
        counted
          ? [
              {
                type: 'usage',
                input: usage.input,
                output: usage.output,
                ...(usage.cached ? { cached: usage.cached } : {}),
                ...(usage.cacheWrite ? { cacheWrite: usage.cacheWrite } : {}),
                ...(usage.reasoning ? { reasoning: usage.reasoning } : {}),
                ...(usage.costUsd !== undefined ? { costUsd: usage.costUsd } : {}),
              },
            ]
          : []
      try {
        try {
          await deliver!()
        } catch (error) {
          await drop(session)
          yield finishError(error instanceof Error ? error.message : String(error), true)
          return
        }
        for (;;) {
          const event = await session.queue.next(calls.length ? QUIET_MS : null, request.signal)
          if (event === 'aborted') {
            await drop(session)
            return
          }
          if (event === 'timeout') {
            for (const call of calls) yield { type: 'tool-call', ...call }
            yield* usagePart()
            yield { type: 'finish', reason: 'tool-calls' }
            session.awaiting = calls.map((call) => call.id)
            session.fps.push(fingerprint({ role: 'assistant', content: '', toolCalls: calls }))
            // Nothing can come back to a session that is not kept.
            if (!session.key) await drop(session)
            return
          }
          switch (event.type) {
            case 'call':
              calls.push({ id: event.id, name: event.name, arguments: event.arguments })
              yield { type: 'calling' }
              break
            case 'calling':
              yield { type: 'calling' }
              break
            case 'text':
              if (event.text) yield { type: 'text', text: event.text }
              break
            case 'reasoning':
              if (event.text) yield { type: 'reasoning', text: event.text, startedAt: Date.now() }
              break
            case 'alive':
              yield { type: 'alive' }
              break
            case 'usage': {
              counted = true
              const u = event.usage
              usage.input += u.input
              usage.output += u.output
              usage.cached += u.cached ?? 0
              usage.cacheWrite += u.cacheWrite ?? 0
              usage.reasoning += u.reasoning ?? 0
              if (u.costUsd !== undefined) usage.costUsd = (usage.costUsd ?? 0) + u.costUsd
              break
            }
            case 'failed':
              await drop(session)
              yield finishError(event.detail, event.retryable, event.busy)
              return
            case 'done':
              // Calls it gave up on are not waited for.
              for (const call of calls) {
                session.pending.get(call.id)?.({
                  content: 'The turn ended before the call ran.',
                  isError: true,
                  images: [],
                })
                session.pending.delete(call.id)
              }
              yield* usagePart()
              yield { type: 'finish', reason: 'stop' }
              session.awaiting = []
              session.fps.push('a:')
              if (!session.key) await drop(session)
              return
          }
        }
      } finally {
        session.busy = false
        session.lastUsed = Date.now()
      }
    },
  }
}
