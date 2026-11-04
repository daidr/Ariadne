import type { TraceRequest, TraceToolCall, TraceTurn } from '@ariadne/core'
import { t, tm } from '../i18n'

/** One line of the trace table: a turn, a request in it, a tool call of that request, … */
export interface TraceRow {
  key: string
  kind: 'turn' | 'request' | 'tool' | 'retry' | 'instructions' | 'compaction-failed'
  event: string
  content: string
  tone: 'danger' | 'warning' | null
  input: number | null
  cached: number | null
  cacheWrite: number | null
  output: number | null
  reasoning: number | null
  /** When it started and ended, in ms; `end: null` while it runs. */
  start: number
  end: number | null
  turn: TraceTurn
  request: TraceRequest | null
  tool: TraceToolCall | null
  /** For a retry: the attempt, the wait and what failed. */
  retry: { attempt: number; delayMs: number; detail: string } | null
  children?: TraceRow[]
}

const oneLine = (text: string, max = 160) => text.replace(/\s+/g, ' ').trim().slice(0, max)

function base(turn: TraceTurn, key: string, start: number, end: number | null) {
  return {
    key,
    tone: null,
    input: null,
    cached: null,
    cacheWrite: null,
    output: null,
    reasoning: null,
    start,
    end,
    turn,
    request: null,
    tool: null,
    retry: null,
  } satisfies Partial<TraceRow>
}

function requestContent(request: TraceRequest): string {
  if (request.text.trim()) return oneLine(request.text)
  if (request.toolCalls.length) return request.toolCalls.map((call) => call.name).join(', ')
  const thought = request.reasoning.find((block) => block.text)
  return thought ? oneLine(thought.text) : ''
}

function toolRow(turn: TraceTurn, request: TraceRequest, tool: TraceToolCall): TraceRow {
  const args = oneLine(tool.arguments, 120)
  return {
    ...base(turn, `${request.seq}:${tool.id}`, tool.startedAt, tool.result?.finishedAt ?? null),
    kind: 'tool',
    event: t('trace.kind.tool'),
    content: args && args !== '{}' ? `${tool.name}  ${args}` : tool.name,
    tone: tool.result?.isError ? 'danger' : null,
    request,
    tool,
  }
}

/** The turns as a tree: turns, then their requests and other events, then tool calls. */
export function traceRows(turns: TraceTurn[]): TraceRow[] {
  return turns.map((turn) => {
    const children: TraceRow[] = []
    for (const [index, item] of turn.items.entries()) {
      const key = `${turn.turnId}:${index}`
      switch (item.kind) {
        case 'request': {
          const tools = item.toolCalls.map((tool) => toolRow(turn, item, tool))
          children.push({
            ...base(turn, String(item.seq), item.startedAt, item.finishedAt),
            kind: 'request',
            event: t(item.purpose === 'summary' ? 'trace.kind.summary' : 'trace.kind.request', {
              n: item.number,
            }),
            content: requestContent(item),
            input: item.usage?.input ?? null,
            cached: item.usage?.cached ?? null,
            cacheWrite: item.usage?.cacheWrite ?? null,
            output: item.usage?.output ?? null,
            reasoning: item.usage?.reasoning ?? null,
            request: item,
            ...(tools.length ? { children: tools } : {}),
          })
          break
        }
        case 'retry':
          children.push({
            ...base(turn, key, item.at, item.at + item.delayMs),
            kind: 'retry',
            event: t('trace.kind.retry'),
            content: oneLine(item.detail),
            tone: 'warning',
            retry: { attempt: item.attempt, delayMs: item.delayMs, detail: item.detail },
          })
          break
        case 'compaction-failed':
          children.push({
            ...base(turn, key, item.at, item.at),
            kind: 'compaction-failed',
            event: t('trace.kind.compactionFailed'),
            content: oneLine(item.detail),
            tone: 'warning',
          })
          break
        case 'instructions':
          children.push({
            ...base(turn, key, item.at, item.at),
            kind: 'instructions',
            event: t('trace.kind.instructions'),
            content: t(
              item.removed
                ? 'notice.instructionsRemoved'
                : item.changed
                  ? 'notice.instructionsChanged'
                  : 'notice.instructions',
              { path: item.path },
            ),
          })
          break
      }
    }
    const outcome = turn.outcome ? t(`trace.outcome.${turn.outcome}`) : t('trace.running')
    return {
      ...base(turn, turn.turnId, turn.startedAt, turn.finishedAt),
      kind: 'turn',
      event: t('trace.kind.turn'),
      content: [turn.model, outcome, turn.error ? tm(turn.error) : ''].filter(Boolean).join(' · '),
      tone: turn.outcome === 'failed' ? 'danger' : null,
      input: turn.usage.input,
      cached: turn.usage.cached ?? null,
      cacheWrite: turn.usage.cacheWrite ?? null,
      output: turn.usage.output,
      reasoning: turn.usage.reasoning ?? null,
      ...(children.length ? { children } : {}),
    }
  })
}

/** Every row that has rows under it. */
export function parentKeys(rows: TraceRow[]): string[] {
  return rows.flatMap((row) => (row.children ? [row.key, ...parentKeys(row.children)] : []))
}

export function findRow(rows: TraceRow[], key: string | number | null): TraceRow | null {
  for (const row of rows) {
    if (row.key === key) return row
    const found = row.children ? findRow(row.children, key) : null
    if (found) return found
  }
  return null
}
