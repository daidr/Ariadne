import {
  DomainError,
  type Ariadne,
  type ConversationView,
  type ToolCallRecord,
  type TranscriptEntry,
} from '@ariadne/core'
import type { Locale, LocalizedMessage } from '@ariadne/i18n'
import {
  MAX_RETRY_AFTER_MS,
  type DashboardSource,
  type ModelProvider,
  type ModelRequest,
  type ModelStreamPart,
} from '@ariadne/plugin-sdk'
import { createHash } from 'node:crypto'
import { coordinatorPrompt, threadPrompt, type ThreadContext } from './prompts'
import { toolsFor, ToolInputError, type Tool, type ToolContext } from './tools'
import { compactAt, compactionCut, estimateTokens, summarise, summaryRequest } from './compaction'
import { toChatMessages } from './transcript'
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
): string {
  if (conversation.role === 'coordinator') {
    return coordinatorPrompt({
      projectName: app.getProject(conversation.projectId).name,
      locale,
      memories: app.listMemories(conversation.projectId),
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
  const tools =
    options.tools ??
    toolsFor(conversation.role, { workspace: hasWorkspace, repo: hasRepo, tracker: hasTracker })
  const byName = new Map(tools.map((tool) => [tool.spec.name, tool]))
  const conversationId = conversation.id
  // Input newer than this arrived for this turn.
  const handledBefore = lastHandled(app.listTranscript(conversationId))
  const turnId = app.startTurn(conversationId, model.label)
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
    userSpoke: () => userSpokeSince(app.listTranscript(conversationId), handledBefore),
    ...(options.dashboardSources ? { dashboardSources: options.dashboardSources } : {}),
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
    // A turn has no step limit: it ends when the model answers, the user stops it, or it
    // goes in circles (see `MAX_REPEATS`). Only the latest steps count, so a long turn may
    // read an unchanged file again now and then.
    const recent: string[] = []
    for (;;) {
      if (isThread && workspaces && hasWorkspace) {
        await syncInstructions(app, conversation, turnId, workspaces)
      }
      const sawSeq = app.lastSeq()
      const system = {
        role: 'system' as const,
        content: systemPrompt(app, conversation, options.locale, hasWorkspace, hasRepo),
      }
      let transcript = app.listTranscript(conversationId)
      let messages = [system, ...toChatMessages(transcript, titleOf, { state })]
      // A long conversation gets its older history summarised before it outgrows the
      // model's context. Between steps every call has its result, so any cut is whole.
      if (!compactionFailed && Math.max(lastInput, estimateTokens(messages)) >= threshold) {
        const upTo = compactionCut(transcript, threshold / 4)
        if (upTo !== null) {
          const request = summaryRequest(transcript, upTo, titleOf, conversation.role)
          const summary = await summarise(model, request, signal)
          if (signal.aborted) break
          if (summary.ok) {
            app.compactConversation(conversationId, turnId, {
              summary: summary.text,
              upTo,
              usage: summary.usage,
            })
            transcript = app.listTranscript(conversationId)
            messages = [system, ...toChatMessages(transcript, titleOf, { state })]
          } else {
            compactionFailed = true
            app.recordCompactionFailure(conversationId, turnId, summary.detail.slice(0, 500))
          }
        }
      }
      const answer = await requestStep(
        model,
        { model: model.model, messages, tools: tools.map((tool) => tool.spec), signal },
        (retry) => app.recordModelRetry(conversationId, turnId, retry),
        options.retryBaseMs ?? 1000,
      )
      if (signal.aborted || !answer) break
      const { text, toolCalls, usage } = answer
      lastInput = usage?.input ?? 0
      app.recordStep(conversationId, turnId, { text, toolCalls, sawSeq, usage })

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
  toolCalls: ToolCallRecord[]
  usage: { input: number; output: number } | null
} | null> {
  for (let attempt = 0; ; attempt++) {
    let text = ''
    const toolCalls: ToolCallRecord[] = []
    let usage: { input: number; output: number } | null = null
    let failure: Extract<ModelStreamPart, { type: 'finish' }> | null = null
    for await (const part of model.provider.stream(model.settings as never, request)) {
      if (part.type === 'text') text += part.text
      else if (part.type === 'tool-call')
        toolCalls.push({ id: part.id, name: part.name, arguments: part.arguments })
      else if (part.type === 'usage') usage = { input: part.input, output: part.output }
      else if (part.type === 'finish' && part.reason === 'error') {
        failure = part
        break
      } else if (part.type === 'finish' && part.reason === 'length') {
        // A cut-off reply is not an answer, and cut-off tool arguments are not a call.
        throw new TurnError({ key: 'error.truncated' })
      }
    }
    if (request.signal.aborted) return null
    if (!failure) return { text, toolCalls, usage }
    const detail = (failure.error ?? '').slice(0, 500)
    if (!failure.retryable || attempt >= MAX_MODEL_RETRIES) {
      throw new TurnError({ key: 'error.modelRequest', params: { detail } })
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
    ? asking.map((a) => `- [${a.id}] ${a.question}`).join('\n')
    : '(none)'
  return `<current_state>\nYour todo list as stored now (update_todo replaces it whole, so keep what still applies):\n${todoText}\nYour questions still waiting for the user's answer (do not ask them again):\n${askingText}\n</current_state>`
}

/** The scheduler aborts with `interrupted` on shutdown and `cancelled` when the user stops. */
function abortOutcome(signal: AbortSignal): 'cancelled' | 'interrupted' {
  return signal.reason === 'interrupted' ? 'interrupted' : 'cancelled'
}
