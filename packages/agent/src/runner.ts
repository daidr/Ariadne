import {
  askTitle,
  DomainError,
  type Ariadne,
  type Attachment,
  type CallTiming,
  type ConversationView,
  type ReasoningRecord,
  type TokenUsage,
  type ToolCallRecord,
  type TranscriptEntry,
} from '@ariadne/core'
import type { Locale, LocalizedMessage } from '@ariadne/i18n'
import {
  MAX_RETRY_AFTER_MS,
  type DashboardSource,
  type Effort,
  type ModelProvider,
  type ModelRequest,
  type ModelStreamPart,
  type SearchProvider,
} from '@ariadne/plugin-sdk'
import { createHash } from 'node:crypto'
import { coordinatorPrompt, threadPrompt, type ThreadContext } from './prompts'
import { toolsFor, ToolInputError, type ModelChoice, type Tool, type ToolContext } from './tools'
import {
  compactAt,
  compactionCut,
  estimateTokens,
  summarise,
  summaryRequest,
  usageOf,
} from './compaction'
import { attachedText, toChatMessages, visibleHistory, withImageData } from './transcript'
import type { McpHub } from './mcp'
import { mcpTools } from './mcp-tools'
import type { WorkspaceManager } from './workspaces'

/** The model a conversation talks to, with its provider's settings. */
export interface ResolvedModel {
  provider: ModelProvider<never>
  settings: unknown
  model: string
  /** Shown to the user, e.g. `anthropic / some-model`. */
  label: string
  /** How many tokens the model reads at most, when its settings say. */
  contextTokens?: number
  /** `false` when the model cannot look at images: they are named in words instead. */
  images?: boolean
  /** How hard it thinks in this role; without it the request asks nothing. */
  effort?: Effort
}

/**
 * What a request went with besides its messages, as recorded with it: the provider kind,
 * model, effort and settings, secrets left out. The trace rebuilds the body from it.
 */
export function callOf(model: ResolvedModel, effort: Effort | null): string {
  const settings: Record<string, unknown> = {}
  const fields = model.provider.settings.properties
  for (const [key, value] of Object.entries((model.settings ?? {}) as Record<string, unknown>)) {
    const field = fields[key]
    if (field && !(field.type === 'string' && field.secret)) settings[key] = value
  }
  return JSON.stringify({ provider: model.provider.kind, model: model.model, effort, settings })
}

export interface TurnOptions {
  app: Ariadne
  conversation: ConversationView
  model: ResolvedModel
  locale: Locale
  signal: AbortSignal
  tools?: Tool[]
  /** Opens threads' workspaces; without it threads get no file or command tools. */
  workspaces?: WorkspaceManager
  /** Input size, in tokens, at which older history is summarised. */
  compactAt?: number
  /** First wait before a model request is retried; it doubles each time. */
  retryBaseMs?: number
  dashboardSources?: ReadonlyMap<string, DashboardSource<never>>
  /** The plugins' web search services; without any, agents get no search tool. */
  searchProviders?: ReadonlyMap<string, SearchProvider>
  /** The models the user set up; the coordinator may give one to each thread it opens. */
  models?: ModelChoice[]
  /** The user's external tool servers; the project's enabled ones bring their tools. */
  mcp?: McpHub
}

/** Why a turn stopped early, for the user. */
class TurnError extends Error {
  constructor(readonly localized: LocalizedMessage) {
    super(localized.key)
  }
}

function threadContext(
  app: Ariadne,
  conversation: ConversationView,
  locale: Locale,
  hasWorkspace: boolean,
  hasRepo: boolean,
): ThreadContext {
  const project = app.getProject(conversation.projectId)
  const thread = app.getThread(conversation.id)
  const plan = thread.nodeId ? app.getPlan(project.id) : null
  const node = plan?.nodes.find((n) => n.id === thread.nodeId) ?? null
  const threads = node ? app.listThreads(project.id) : []
  const upstream = (node?.dependsOn ?? []).flatMap((id) => {
    const title = plan?.nodes.find((n) => n.id === id)?.title ?? id
    return threads
      .filter((t) => t.nodeId === id && t.lastReport === 'done' && t.lastSummary)
      .map((t) => ({ node: title, summary: t.lastSummary as string }))
  })
  return {
    projectName: project.name,
    title: thread.title,
    locale,
    node: node
      ? {
          title: node.title,
          description: node.description,
          acceptance: node.acceptance,
          issues: node.issues.map((issue) => issue.number),
        }
      : null,
    upstream,
    workspace: !hasWorkspace ? 'none' : (thread.workspace ?? 'not-open'),
    repo: hasRepo,
    memories: app.listMemories(project.id),
    instructions: (() => {
      const recorded = app.getInstructions(conversation.id)
      return recorded?.content ? { path: recorded.path, content: recorded.content } : null
    })(),
  }
}

function systemPrompt(
  app: Ariadne,
  conversation: ConversationView,
  locale: Locale,
  hasWorkspace: boolean,
  hasRepo: boolean,
  models: ModelChoice[],
): string {
  if (conversation.role === 'coordinator') {
    return coordinatorPrompt({
      projectName: app.getProject(conversation.projectId).name,
      locale,
      memories: app.listMemories(conversation.projectId),
      models,
    })
  }
  return threadPrompt(threadContext(app, conversation, locale, hasWorkspace, hasRepo))
}

async function runTool(tool: Tool | undefined, call: ToolCallRecord, ctx: ToolContext) {
  if (!tool) return { content: `Unknown tool "${call.name}".`, isError: true }
  let args: unknown
  try {
    args = JSON.parse(call.arguments || '{}')
  } catch {
    return { content: 'The arguments are not valid JSON.', isError: true }
  }
  if (!args || typeof args !== 'object' || Array.isArray(args)) {
    return { content: 'The arguments must be a JSON object.', isError: true }
  }
  try {
    return await tool.run(args as Record<string, unknown>, ctx)
  } catch (error) {
    if (error instanceof ToolInputError) return { content: error.message, isError: true }
    if (error instanceof DomainError) {
      return { content: `${error.code} ${JSON.stringify(error.params ?? {})}`, isError: true }
    }
    throw error
  }
}

/**
 * Run one turn: ask the model, run the tools it calls, and repeat until it answers without
 * calls. Every step is an event, and the model's input is rebuilt from the log each time,
 * so a turn can be replayed or resumed from what is stored.
 */
export async function runTurn(options: TurnOptions): Promise<void> {
  const { app, conversation, model, signal } = options
  const { workspaces } = options
  const isThread = conversation.role === 'thread'
  const hasWorkspace = isThread && !!workspaces?.available(conversation.projectId)
  const hasRepo = hasWorkspace && !!workspaces?.hasRepo(conversation.projectId)
  const hasTracker = !!workspaces?.hasTracker(conversation.projectId)
  const builtIn =
    options.tools ??
    toolsFor(conversation.role, {
      workspace: hasWorkspace,
      repo: hasRepo,
      tracker: hasTracker,
      models: options.models ?? [],
      ...(options.searchProviders ? { search: options.searchProviders } : {}),
    })
  const conversationId = conversation.id
  // Input newer than this arrived for this turn.
  const handledBefore = lastHandled(app.listTranscript(conversationId))
  const turnId = app.startTurn(conversationId, model.label)
  // The project's external tools, as their servers list them now; one that cannot be
  // reached brings none, and the turn goes on without it.
  const external =
    options.mcp && !options.tools
      ? await mcpTools(
          options.mcp,
          app.getProject(conversation.projectId).mcp,
          conversation.role,
          signal,
        )
      : []
  const tools = [...builtIn, ...external]
  const byName = new Map(tools.map((tool) => [tool.spec.name, tool]))
  const ctx: ToolContext = {
    app,
    projectId: conversation.projectId,
    conversationId,
    role: conversation.role,
    locale: options.locale,
    signal,
    workspace: () => {
      if (!workspaces || conversation.role !== 'thread') {
        return Promise.reject(new DomainError('error.noSource'))
      }
      return workspaces.forThread(conversation.projectId, conversationId)
    },
    repo: () => {
      if (!workspaces || !isThread) return Promise.reject(new DomainError('error.noRepo'))
      return workspaces.repo(conversation.projectId)
    },
    tracker: () => {
      if (!workspaces) return Promise.reject(new DomainError('error.noTracker'))
      return workspaces.tracker(conversation.projectId)
    },
    branches: async () => (workspaces ? workspaces.branches(conversation.projectId) : null),
    userSpoke: () => userSpokeSince(app.listTranscript(conversationId), handledBefore),
    ...(options.dashboardSources ? { dashboardSources: options.dashboardSources } : {}),
    ...(options.searchProviders ? { searchProviders: options.searchProviders } : {}),
    models: options.models ?? [],
  }
  const readText = attachedText(app)
  const images = model.images !== false
  const chat = { readText, images }
  const loadImage = (file: Attachment) => {
    try {
      return {
        mime: file.mime,
        data: Buffer.from(app.getFile(file.fileId).data).toString('base64'),
      }
    } catch {
      return null
    }
  }
  const titleOf = (threadId: string) => {
    try {
      return app.getThread(threadId).title
    } catch {
      return threadId
    }
  }

  try {
    const threshold = options.compactAt ?? compactAt(model.contextTokens)
    // One try per turn: a failed summary would fail the same way on every step.
    let compactionFailed = false
    let lastInput = 0
    const state = () => currentState(app, conversation)
    const system = () =>
      systemPrompt(app, conversation, options.locale, hasWorkspace, hasRepo, options.models ?? [])
    const specs = tools.map((tool) => tool.spec)
    const toolsJson = JSON.stringify(specs)
    // A turn has no step limit: it ends when the model answers, the user stops it, or it
    // goes in circles (see `MAX_REPEATS`). Only the latest steps count, so a long turn may
    // read an unchanged file again now and then.
    const recent: string[] = []
    for (;;) {
      if (isThread && workspaces && hasWorkspace) {
        await syncInstructions(app, conversation, turnId, workspaces)
      }
      // A long conversation gets its older history summarised before it outgrows the
      // model's context. Between steps every call has its result, so any cut is whole.
      if (!compactionFailed) {
        const readAt = app.lastSeq()
        const transcript = app.listTranscript(conversationId)
        const estimate = estimateTokens([
          { role: 'system', content: system() },
          ...toChatMessages(transcript, titleOf, { ...chat, state: state() }),
        ])
        const upTo =
          Math.max(lastInput, estimate) >= threshold
            ? compactionCut(transcript, threshold / 4)
            : null
        if (upTo !== null) {
          const request = summaryRequest(transcript, upTo, titleOf, conversation.role, readText)
          const summary = await summarise(model, withImageData(request, loadImage), signal)
          if (signal.aborted) break
          if (summary.ok) {
            app.compactConversation(conversationId, turnId, {
              summary: summary.text,
              upTo,
              sawSeq: readAt,
              usage: summary.usage,
              call: callOf(model, null),
              timing: summary.timing,
            })
          } else {
            compactionFailed = true
            app.recordCompactionFailure(conversationId, turnId, summary.detail.slice(0, 500))
          }
        }
      }
      // Everything below is read in one go, so the input is exactly the log up to `sawSeq`
      // plus the pieces recorded with the step: the trace rebuilds it from those.
      const sawSeq = app.lastSeq()
      const input = { system: system(), tools: toolsJson, state: state() }
      const transcript = app.listTranscript(conversationId)
      const messages = withImageData(
        [
          { role: 'system' as const, content: input.system },
          ...toChatMessages(transcript, titleOf, { ...chat, state: input.state }),
        ],
        loadImage,
      )
      const shownState = visibleHistory(transcript).summary === null ? null : input.state
      const answer = await requestStep(
        model,
        {
          model: model.model,
          messages,
          tools: specs,
          ...(model.effort ? { effort: model.effort } : {}),
          signal,
        },
        (retry) => app.recordModelRetry(conversationId, turnId, retry),
        options.retryBaseMs ?? 1000,
      )
      if (signal.aborted || !answer) break
      const { text, reasoning, toolCalls, usage, timing } = answer
      lastInput = usage?.input ?? 0
      app.recordStep(conversationId, turnId, {
        text,
        reasoning,
        toolCalls,
        sawSeq,
        usage,
        input: {
          system: input.system,
          tools: input.tools,
          state: shownState,
          call: callOf(model, model.effort ?? null),
          images,
        },
        timing,
      })

      if (!toolCalls.length) {
        if (text.trim()) {
          app.postMessage(conversation.projectId, {
            conversationId,
            author: conversation.role,
            content: text.trim(),
          })
        }
        app.finishTurn(conversationId, turnId, 'completed')
        return
      }
      // The same calls with the same results again: the model is not getting anywhere.
      // It is told once, with the last result of the step before the turn is stopped.
      const results: { content: string; isError: boolean }[] = []
      for (const [i, call] of toolCalls.entries()) {
        if (signal.aborted) break
        const result = await runTool(byName.get(call.name), call, ctx)
        results.push({ content: result.content, isError: result.isError ?? false })
        const files = result.files ?? []
        let { content } = result
        let count = 0
        if (i === toolCalls.length - 1) {
          const step = fingerprint(toolCalls, results)
          recent.push(step)
          if (recent.length > REPEAT_WINDOW) recent.shift()
          count = recent.filter((seen) => seen === step).length
          if (count === MAX_REPEATS - 1) content = `${content}\n\n${REPEAT_HINT}`
        }
        app.recordToolResult(conversationId, turnId, {
          toolCallId: call.id,
          name: call.name,
          content,
          isError: result.isError ?? false,
          ...(files.length ? { files } : {}),
        })
        if (count >= MAX_REPEATS) {
          throw new TurnError({ key: 'error.repeating', params: { count } })
        }
      }
      if (signal.aborted) break
    }
    app.finishTurn(conversationId, turnId, abortOutcome(signal))
  } catch (error) {
    if (signal.aborted) {
      app.finishTurn(conversationId, turnId, abortOutcome(signal))
      return
    }
    const localized =
      error instanceof TurnError
        ? error.localized
        : error instanceof DomainError
          ? error.toLocalized()
          : { key: 'error.modelRequest' as const, params: { detail: String(error) } }
    app.finishTurn(conversationId, turnId, 'failed', localized)
  }
}

/** The repository's instruction file, read from the workspace root. */
export const INSTRUCTIONS_FILE = 'AGENTS.md'
/** Longer instruction files are cut, so they cannot crowd out the conversation. */
const MAX_INSTRUCTIONS = 16_000

/**
 * Record AGENTS.md when it appears or changes in an open workspace. A workspace not
 * opened yet is left alone: reading the file must not create a checkout.
 */
async function syncInstructions(
  app: Ariadne,
  conversation: ConversationView,
  turnId: string,
  workspaces: WorkspaceManager,
): Promise<void> {
  // Reading the file must not bring back a worktree that is not there.
  const known = app.getThread(conversation.id).workspace
  if (!known || known.removed) return
  let content: string | null = null
  let workspace: Awaited<ReturnType<WorkspaceManager['forThread']>> | null = null
  try {
    workspace = await workspaces.forThread(conversation.projectId, conversation.id)
    content = await workspace.readFile(INSTRUCTIONS_FILE)
  } catch {
    content = null
  }
  if (content !== null && content.length > MAX_INSTRUCTIONS) {
    content = `${content.slice(0, MAX_INSTRUCTIONS)}\n[cut: the file is longer]`
  }
  const hash = content === null ? 'none' : createHash('sha256').update(content).digest('hex')
  if (app.getInstructions(conversation.id)?.hash === hash) return
  if (!app.getInstructions(conversation.id) && content === null) return
  // Whether the change is the checkout's own or was made in the workspace (by the thread):
  // the user should know which one the thread follows.
  let uncommitted = false
  if (content !== null && workspace?.branch) {
    const status = await workspace
      .exec(`git status --porcelain -- ${INSTRUCTIONS_FILE}`, { timeoutMs: 10_000 })
      .catch(() => null)
    uncommitted = status?.exitCode === 0 && !!status.stdout.trim()
  }
  app.recordInstructions(conversation.id, turnId, {
    path: INSTRUCTIONS_FILE,
    hash,
    content,
    uncommitted,
  })
}

/**
 * How often the same step (the same tool calls, returning the same results) may come
 * back within the last `REPEAT_WINDOW` steps before the turn is stopped. Steps need not
 * follow each other, so a loop between two or three calls is caught too.
 */
export const MAX_REPEATS = 4
export const REPEAT_WINDOW = 12

const REPEAT_HINT =
  '[Ariadne: this exact call has returned this exact result several times in this turn. Doing it again will stop the turn. Try something else, or answer with what you have. If you are waiting for something (checks, a reply), end the turn: you will be woken.]'

function fingerprint(
  calls: ToolCallRecord[],
  results: { content: string; isError: boolean }[],
): string {
  const hash = createHash('sha256')
  calls.forEach((call, i) => {
    hash.update(
      JSON.stringify([call.name, call.arguments, results[i]?.content, results[i]?.isError]),
    )
  })
  return hash.digest('hex')
}

/** How often a passing model error (rate limit, overload, broken connection) is retried. */
export const MAX_MODEL_RETRIES = 4

/**
 * One model response. Only errors the provider marks retryable are tried again, at most
 * `MAX_MODEL_RETRIES` times with a growing wait, and each retry is recorded so the user
 * sees it; what a failed attempt streamed is dropped. `null` when aborted.
 */
async function requestStep(
  model: ResolvedModel,
  request: ModelRequest & { signal: AbortSignal },
  onRetry: (retry: { attempt: number; delayMs: number; detail: string }) => void,
  baseDelayMs: number,
): Promise<{
  text: string
  reasoning: ReasoningRecord[]
  toolCalls: ToolCallRecord[]
  usage: TokenUsage | null
  timing: CallTiming
} | null> {
  for (let attempt = 0; ; attempt++) {
    let text = ''
    const reasoning: ReasoningRecord[] = []
    const toolCalls: ToolCallRecord[] = []
    let usage: TokenUsage | null = null
    let failure: Extract<ModelStreamPart, { type: 'finish' }> | null = null
    const timing: CallTiming = { startedAt: Date.now(), firstOutputAt: null }
    for await (const part of model.provider.stream(model.settings as never, request)) {
      if (part.type === 'text' || part.type === 'reasoning' || part.type === 'tool-call') {
        // Reasoning is handed on whole; it began when its first piece came.
        timing.firstOutputAt ??= (part.type === 'reasoning' && part.startedAt) || Date.now()
      }
      if (part.type === 'text') text += part.text
      else if (part.type === 'reasoning') {
        const { type: _, startedAt: __, ...block } = part
        reasoning.push(block)
      } else if (part.type === 'tool-call')
        toolCalls.push({ id: part.id, name: part.name, arguments: part.arguments })
      else if (part.type === 'usage') usage = usageOf(part)
      else if (part.type === 'finish' && part.reason === 'error') {
        failure = part
        break
      } else if (part.type === 'finish' && part.reason === 'length') {
        // A cut-off reply is not an answer, and cut-off tool arguments are not a call.
        throw new TurnError({ key: 'error.truncated' })
      }
    }
    if (request.signal.aborted) return null
    if (!failure) return { text, reasoning, toolCalls, usage, timing }
    const detail = (failure.error ?? '').slice(0, 500)
    if (!failure.retryable || attempt >= MAX_MODEL_RETRIES) {
      // A service that cannot take images refuses every request that carries one.
      const images = request.messages.some(
        (message) => (message.role === 'user' || message.role === 'tool') && message.images?.length,
      )
      throw new TurnError({
        key: images ? 'error.modelRequestImages' : 'error.modelRequest',
        params: { detail },
      })
    }
    const delayMs = Math.min(MAX_RETRY_AFTER_MS, failure.retryAfterMs ?? baseDelayMs * 2 ** attempt)
    onRetry({ attempt: attempt + 1, delayMs, detail })
    await abortableSleep(delayMs, request.signal)
    if (request.signal.aborted) return null
  }
}

function abortableSleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) return resolve()
    const timer = setTimeout(done, ms)
    function done() {
      clearTimeout(timer)
      signal.removeEventListener('abort', done)
      resolve()
    }
    signal.addEventListener('abort', done, { once: true })
  })
}

/** The newest event a step of this conversation had seen. */
function lastHandled(transcript: TranscriptEntry[]): number {
  let seen = 0
  for (const entry of transcript) if (entry.kind === 'step') seen = Math.max(seen, entry.sawSeq)
  return seen
}

/** Whether the user's own words reached the conversation after `since`. */
function userSpokeSince(transcript: TranscriptEntry[], since: number): boolean {
  return transcript.some(
    (entry) =>
      entry.seq > since &&
      ((entry.kind === 'message' && entry.message.author === 'user') ||
        (entry.kind === 'note' &&
          (entry.note.type === 'user-in-thread' || entry.note.type === 'answer'))),
  )
}

/**
 * What stored state says now, beside the notes of a summarised conversation: the notes
 * may blur the todo list, and `update_todo` replaces it whole.
 */
function currentState(app: Ariadne, conversation: ConversationView): string {
  const todo = app.getTodo(conversation.id).items
  const asking = app
    .listOpenAttentions(conversation.projectId)
    .filter((a) =>
      conversation.role === 'thread' ? a.threadId === conversation.id : a.threadId === null,
    )
  const todoText = todo.length
    ? todo.map((item) => `- [${item.status}] ${item.label}`).join('\n')
    : '(empty)'
  const askingText = asking.length
    ? asking.map((a) => `- [${a.id}] ${askTitle(a.block)}`).join('\n')
    : '(none)'
  return `<current_state>\nYour todo list as stored now (update_todo replaces it whole, so keep what still applies):\n${todoText}\nYour questions still waiting for the user's answer (do not ask them again; withdraw one with withdraw_ask once the user answered it in words or it no longer matters):\n${askingText}\n</current_state>`
}

/** The scheduler aborts with `interrupted` on shutdown and `cancelled` when the user stops. */
function abortOutcome(signal: AbortSignal): 'cancelled' | 'interrupted' {
  return signal.reason === 'interrupted' ? 'interrupted' : 'cancelled'
}
