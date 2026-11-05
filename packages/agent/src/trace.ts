import {
  DomainError,
  type AnyStoredEvent,
  type Ariadne,
  type EventMap,
  type TokenUsage,
  type TraceRequest,
  type TraceRequestDetail,
  type TraceToolCall,
  type TraceTurn,
} from '@ariadne/core'
import type { Effort, ModelRequest, ToolSpec } from '@ariadne/plugin-sdk'
import { summaryRequest } from './compaction'
import {
  attachedText,
  toChatMessages,
  withImageData,
  writtenBy,
  type ModelMessage,
} from './transcript'

/** What a request went with, as `callOf` recorded it. */
export interface RecordedCall {
  provider: string
  model: string
  effort: Effort | null
  settings: Record<string, unknown>
  /** What the user added to the request in the model's settings. */
  maxTokens?: number
  extraBody?: Record<string, unknown>
}

/**
 * A conversation's turns, newest last, rebuilt from the event log. `before` pages back:
 * pass the `seq` of the oldest turn shown.
 */
export function buildTrace(
  app: Ariadne,
  conversationId: string,
  options: { before?: number; turns?: number } = {},
): { turns: TraceTurn[]; more: boolean } {
  const { events, more } = app.listTurnEvents(conversationId, options)
  const first = events[0]
  let number = first ? app.countRequests(conversationId, first.seq) : 0
  const calls = new Map<string, RecordedCall | null>()
  const callOf = (projectId: string, hash: string) => {
    if (!calls.has(hash)) calls.set(hash, parseCall(app.getPrompt(projectId, hash)))
    return calls.get(hash) ?? null
  }
  const turns: TraceTurn[] = []
  let turn: TraceTurn | null = null
  // Without times of their own, tools ran one after another once their step was answered.
  let toolsFrom = 0
  const running = new Map<string, TraceToolCall>()
  for (const event of events) {
    if (event.type === 'turn.started') {
      turn = {
        turnId: event.data.turnId,
        seq: event.seq,
        model: event.data.model,
        startedAt: event.at,
        finishedAt: null,
        outcome: null,
        error: null,
        usage: { input: 0, output: 0 },
        items: [],
      }
      turns.push(turn)
      running.clear()
      continue
    }
    if (!turn) continue
    switch (event.type) {
      case 'turn.step': {
        const { usage, timing, input } = event.data
        if (usage) addUsage(turn.usage, usage)
        const call = callOf(event.projectId, input.call)
        toolsFrom = event.at
        const toolCalls = event.data.toolCalls.map((tool) => {
          const entry: TraceToolCall = { ...tool, startedAt: event.at, result: null }
          running.set(tool.id, entry)
          return entry
        })
        turn.items.push({
          ...requestBase(event.seq, ++number, call, timing, event.at, usage),
          purpose: 'step',
          finish: event.data.finish,
          text: event.data.text,
          reasoning: event.data.reasoning.map((block) => ({
            text: block.redacted ? '' : block.text,
            redacted: !!block.redacted,
          })),
          toolCalls,
        })
        break
      }
      case 'research.step': {
        const { usage, timing, input } = event.data
        if (usage) addUsage(turn.usage, usage)
        const parent = running.get(event.data.toolCallId)
        if (!parent) break
        const toolCalls = event.data.toolCalls.map((tool): TraceToolCall => {
          const result = event.data.results.find((r) => r.toolCallId === tool.id)
          return {
            ...tool,
            startedAt: event.data.answeredAt,
            result: result
              ? {
                  content: result.content,
                  isError: result.isError,
                  finishedAt: event.at,
                  files: [],
                }
              : null,
          }
        })
        ;(parent.research ??= []).push({
          ...requestBase(
            event.seq,
            event.data.number,
            callOf(event.projectId, input.call),
            timing,
            event.data.answeredAt,
            usage,
          ),
          purpose: 'research',
          finish: event.data.finish,
          text: event.data.text,
          reasoning: event.data.reasoning.map((block) => ({
            text: block.redacted ? '' : block.text,
            redacted: !!block.redacted,
          })),
          toolCalls,
        })
        break
      }
      case 'tool.finished': {
        const tool = running.get(event.data.toolCallId)
        if (tool) {
          // Calls that ran together have their own times; one closed unrun ends here.
          tool.startedAt = event.data.timing?.startedAt ?? toolsFrom
          tool.result = {
            content: event.data.content,
            isError: event.data.isError,
            finishedAt: event.data.timing?.finishedAt ?? event.at,
            files: event.data.files ?? [],
          }
          running.delete(event.data.toolCallId)
        }
        // Calls that ran together end in any order: the next goes on from the latest.
        toolsFrom = Math.max(toolsFrom, event.data.timing?.finishedAt ?? event.at)
        break
      }
      case 'conversation.model_retry':
        turn.items.push({
          kind: 'retry',
          at: event.at,
          attempt: event.data.attempt,
          delayMs: event.data.delayMs,
          detail: event.data.detail,
        })
        break
      case 'conversation.model_fallback':
        turn.items.push({
          kind: 'fallback',
          at: event.at,
          from: event.data.from,
          to: event.data.to,
          detail: event.data.detail,
        })
        break
      case 'conversation.compacted': {
        const { usage, timing, call } = event.data
        if (usage) addUsage(turn.usage, usage)
        turn.items.push({
          ...requestBase(
            event.seq,
            ++number,
            callOf(event.projectId, call),
            timing,
            event.at,
            usage,
          ),
          purpose: 'summary',
          finish: 'stop',
          text: event.data.summary,
          reasoning: event.data.reasoning.map((block) => ({ text: block.text, redacted: false })),
          toolCalls: [],
        })
        break
      }
      case 'conversation.results_cleared':
        turn.items.push({ kind: 'results-cleared', at: event.at })
        break
      case 'conversation.compaction_failed':
        turn.items.push({ kind: 'compaction-failed', at: event.at, detail: event.data.detail })
        break
      case 'conversation.instructions':
        turn.items.push({
          kind: 'instructions',
          at: event.at,
          path: event.data.path,
          changed: event.data.changed,
          removed: event.data.content === null,
        })
        break
      case 'turn.finished':
        turn.finishedAt = event.at
        turn.outcome = event.data.outcome
        turn.error = event.data.error
        turn = null
        break
    }
  }
  return { turns, more }
}

function requestBase(
  seq: number,
  number: number,
  call: RecordedCall | null,
  timing: { startedAt: number; firstOutputAt: number | null },
  finishedAt: number,
  usage: TokenUsage | null,
): Omit<TraceRequest, 'purpose' | 'finish' | 'text' | 'reasoning' | 'toolCalls'> {
  return {
    kind: 'request',
    seq,
    number,
    provider: call?.provider ?? null,
    model: call?.model ?? null,
    effort: call?.effort ?? null,
    startedAt: timing.startedAt,
    firstOutputAt: timing.firstOutputAt,
    finishedAt,
    usage,
  }
}

function addUsage(sum: TokenUsage, usage: TokenUsage) {
  sum.input += usage.input
  sum.output += usage.output
  if (usage.cached) sum.cached = (sum.cached ?? 0) + usage.cached
  if (usage.cacheWrite) sum.cacheWrite = (sum.cacheWrite ?? 0) + usage.cacheWrite
  if (usage.reasoning) sum.reasoning = (sum.reasoning ?? 0) + usage.reasoning
}

function parseCall(content: string | null): RecordedCall | null {
  if (content === null) return null
  try {
    return JSON.parse(content) as RecordedCall
  } catch {
    return null
  }
}

/**
 * The whole of one model request of a conversation: a step (`turn.step`) or the summary
 * of its history (`conversation.compacted`). Rebuilt from the transcript as it stood then
 * and the pieces recorded with it, so it is what the provider was handed. `request` is
 * that, ready for the provider's `requestBody`, with each image's bytes named by its file
 * rather than spelled out; the detail leaves the body to the caller.
 */
export function traceRequest(
  app: Ariadne,
  conversationId: string,
  seq: number,
): { detail: Omit<TraceRequestDetail, 'body'>; request: ModelRequest | null } {
  const event = app.events({ afterSeq: seq - 1, limit: 1 })[0]
  if (!event || event.seq !== seq || !belongsTo(event, conversationId)) {
    throw new DomainError('error.notFound', { what: String(seq) })
  }
  const current = requestOf(app, conversationId, event)
  // What the step before it was sent, to mark what is new in this one. Steps only: a
  // summary request reads differently and would mark everything changed. A research step
  // follows the research's own steps.
  const previous =
    event.type === 'research.step'
      ? (app.researchSteps(conversationId, event.data.toolCallId, seq - 1).at(-1) ?? null)
      : app.lastStepBefore(conversationId, seq)
  const before = previous ? requestOf(app, conversationId, previous).messages : []
  // The system prompt moves on its own (the plan, memory, the workspace opening); the
  // conversation after it is compared apart, so one change does not mark everything new.
  const systemChanged = before.length > 0 && !same(before[0], current.messages[0])
  let newFrom = before.length ? 1 : 0
  while (
    newFrom < before.length &&
    newFrom < current.messages.length &&
    same(before[newFrom], current.messages[newFrom])
  ) {
    newFrom++
  }
  const hash =
    event.type === 'turn.step' || event.type === 'research.step'
      ? event.data.input.call
      : event.type === 'conversation.compacted'
        ? event.data.call
        : null
  const call = hash === null ? null : parseCall(app.getPrompt(event.projectId, hash))
  const detail = {
    messages: current.messages,
    tools: current.tools,
    effort: call?.effort ?? null,
    call: call ? { provider: call.provider, model: call.model, settings: call.settings } : null,
    newFrom,
    systemChanged,
  }
  const request: ModelRequest | null = call
    ? {
        model: call.model,
        messages: withImageData(current.messages, (file) => ({
          mime: file.mime,
          data: `<${file.size} bytes of file ${file.fileId}>`,
        })),
        tools: current.tools,
        ...(call.effort ? { effort: call.effort } : {}),
        ...(call.maxTokens ? { maxTokens: call.maxTokens } : {}),
        ...(call.extraBody ? { extraBody: call.extraBody } : {}),
        ...(event.type === 'conversation.compacted' ? { cache: false } : {}),
      }
    : null
  return { detail, request }
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

function belongsTo(event: AnyStoredEvent, conversationId: string): boolean {
  return (
    (event.type === 'turn.step' ||
      event.type === 'conversation.compacted' ||
      event.type === 'research.step') &&
    event.data.conversationId === conversationId
  )
}

function requestOf(
  app: Ariadne,
  conversationId: string,
  event: AnyStoredEvent,
): { messages: ModelMessage[]; tools: ToolSpec[] } {
  const titleOf = (threadId: string) => {
    try {
      return app.getThread(threadId).title
    } catch {
      return threadId
    }
  }
  const readText = attachedText(app)
  if (event.type === 'conversation.compacted') {
    // The summary was asked for from the history as it stood when it was read.
    const transcript = app.listTranscript(conversationId, { upTo: event.data.sawSeq })
    const role = app.getConversation(conversationId).role
    const call = parseCall(app.getPrompt(event.projectId, event.data.call))
    const keep = call ? writtenBy(app, event.projectId, call) : undefined
    return {
      messages: summaryRequest(transcript, event.data.upTo, titleOf, role, readText, keep),
      tools: [],
    }
  }
  const piece = (hash: string) => {
    const content = app.getPrompt(event.projectId, hash)
    if (content === null) throw new DomainError('error.notFound', { what: hash })
    return content
  }
  if (event.type === 'research.step') {
    // The research's own context: its question, then its earlier steps and their results.
    const { input, toolCallId, question } = event.data
    const before = app
      .researchSteps(conversationId, toolCallId, event.seq - 1)
      .flatMap((step) => (step.type === 'research.step' ? [step.data] : []))
    return {
      messages: [
        { role: 'system', content: piece(input.system) },
        { role: 'user', content: question },
        ...researchHistory(before),
      ],
      tools: JSON.parse(piece(input.tools)) as ToolSpec[],
    }
  }
  if (event.type !== 'turn.step') throw new DomainError('error.notFound', { what: event.id })
  const { input, sawSeq } = event.data
  const transcript = app.listTranscript(conversationId, { upTo: sawSeq })
  const call = parseCall(app.getPrompt(event.projectId, input.call))
  return {
    messages: [
      { role: 'system', content: piece(input.system) },
      ...toChatMessages(transcript, titleOf, {
        readText,
        images: input.images,
        ...(call ? { keepReasoning: writtenBy(app, event.projectId, call) } : {}),
      }),
    ],
    tools: JSON.parse(piece(input.tools)) as ToolSpec[],
  }
}

/** A research's steps as the messages its model read after its question. */
export function researchHistory(
  steps: Pick<EventMap['research.step'], 'text' | 'reasoning' | 'toolCalls' | 'results'>[],
): ModelMessage[] {
  return steps.flatMap((step): ModelMessage[] => [
    {
      role: 'assistant',
      content: step.text,
      ...(step.toolCalls.length ? { toolCalls: step.toolCalls } : {}),
      ...(step.reasoning.length ? { reasoning: step.reasoning } : {}),
    },
    ...step.results.map((result): ModelMessage => ({
      role: 'tool',
      toolCallId: result.toolCallId,
      content: result.content,
      ...(result.isError ? { isError: true } : {}),
    })),
  ])
}
