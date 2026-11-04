import {
  DomainError,
  type AnyStoredEvent,
  type Ariadne,
  type TraceInput,
  type TraceItem,
  type TraceTurn,
} from '@ariadne/core'
import type { ChatMessage, ToolSpec } from '@ariadne/plugin-sdk'
import { summaryRequest } from './compaction'
import { toChatMessages } from './transcript'

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
  const turns: TraceTurn[] = []
  let turn: TraceTurn | null = null
  // When the previous event of the turn happened, to time what came after it.
  let last = 0
  let retryWait = 0
  const calls = new Map<string, Extract<TraceItem, { kind: 'step' }>['toolCalls'][number]>()
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
      last = event.at
      retryWait = 0
      calls.clear()
      continue
    }
    if (!turn) continue
    const took = Math.max(0, event.at - last - retryWait)
    retryWait = 0
    switch (event.type) {
      case 'turn.step': {
        const { usage } = event.data
        if (usage) addUsage(turn, usage)
        const toolCalls = event.data.toolCalls.map((call) => ({ ...call, result: null }))
        for (const call of toolCalls) calls.set(call.id, call)
        turn.items.push({
          kind: 'step',
          seq: event.seq,
          at: event.at,
          durationMs: took,
          text: event.data.text,
          toolCalls,
          usage,
        })
        break
      }
      case 'tool.finished': {
        const call = calls.get(event.data.toolCallId)
        if (call) {
          call.result = {
            content: event.data.content,
            isError: event.data.isError,
            durationMs: took,
          }
        }
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
        retryWait = event.data.delayMs
        break
      case 'conversation.compacted':
        if (event.data.usage) addUsage(turn, event.data.usage)
        turn.items.push({
          kind: 'compacted',
          seq: event.seq,
          at: event.at,
          durationMs: took,
          summary: event.data.summary,
          usage: event.data.usage,
        })
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
    last = event.at
  }
  return { turns, more }
}

function addUsage(turn: TraceTurn, usage: { input: number; output: number }) {
  turn.usage.input += usage.input
  turn.usage.output += usage.output
}

/**
 * The whole request of one model call of a conversation: a step (`turn.step`) or the
 * summary of its history (`conversation.compacted`). Rebuilt from the transcript as it
 * stood then and the pieces the step recorded, so it is what the provider was sent.
 */
export function traceInput(app: Ariadne, conversationId: string, seq: number): TraceInput {
  const event = app.events({ afterSeq: seq - 1, limit: 1 })[0]
  if (!event || event.seq !== seq || !belongsTo(event, conversationId)) {
    throw new DomainError('error.notFound', { what: String(seq) })
  }
  const current = requestOf(app, conversationId, event)
  // What the call before it was sent, to mark what is new in this one.
  const previous = previousCall(app, conversationId, seq)
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
  return { messages: current.messages, tools: current.tools, newFrom, systemChanged }
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

function belongsTo(event: AnyStoredEvent, conversationId: string): boolean {
  return (
    (event.type === 'turn.step' || event.type === 'conversation.compacted') &&
    event.data.conversationId === conversationId
  )
}

function requestOf(
  app: Ariadne,
  conversationId: string,
  event: AnyStoredEvent,
): { messages: ChatMessage[]; tools: ToolSpec[] } {
  const titleOf = (threadId: string) => {
    try {
      return app.getThread(threadId).title
    } catch {
      return threadId
    }
  }
  if (event.type === 'conversation.compacted') {
    // The summary was asked for from the history before it, as it stood then.
    const transcript = app.listTranscript(conversationId, { upTo: event.seq - 1 })
    const role = app.getConversation(conversationId).role
    return { messages: summaryRequest(transcript, event.data.upTo, titleOf, role), tools: [] }
  }
  if (event.type !== 'turn.step') throw new DomainError('error.notFound', { what: event.id })
  const { input, sawSeq } = event.data
  const piece = (hash: string) => {
    const content = app.getPrompt(event.projectId, hash)
    if (content === null) throw new DomainError('error.notFound', { what: hash })
    return content
  }
  const transcript = app.listTranscript(conversationId, { upTo: sawSeq })
  const state = input.state === null ? undefined : piece(input.state)
  return {
    messages: [
      { role: 'system', content: piece(input.system) },
      ...toChatMessages(transcript, titleOf, state === undefined ? {} : { state }),
    ],
    tools: JSON.parse(piece(input.tools)) as ToolSpec[],
  }
}

/** The conversation's model call (a step or a summary) right before `seq`, if any. */
function previousCall(app: Ariadne, conversationId: string, seq: number): AnyStoredEvent | null {
  const { events } = app.listTurnEvents(conversationId, { before: seq, turns: 1 })
  // A step's earlier calls are in its own turn; the turn before holds the rest.
  for (const source of [
    events,
    app.listTurnEvents(conversationId, { before: events[0]?.seq ?? seq, turns: 1 }).events,
  ]) {
    const found = source.filter((e) => e.seq < seq && belongsTo(e, conversationId)).at(-1)
    if (found) return found
  }
  return null
}
