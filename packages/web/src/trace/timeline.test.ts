import type { TraceRequest, TraceToolCall, TraceTurn } from '@ariadne/core'
import { describe, expect, test } from 'vitest'
import { traceRows } from './rows'
import { rowsInRange, timelineModel } from './timeline'

const tool = (
  id: string,
  startedAt: number,
  finishedAt: number | null,
  research?: TraceRequest[],
) =>
  ({
    id,
    name: id,
    arguments: '{}',
    startedAt,
    result: finishedAt === null ? null : { content: '', isError: false, finishedAt, files: [] },
    ...(research ? { research } : {}),
  }) satisfies TraceToolCall

const request = (
  seq: number,
  startedAt: number,
  finishedAt: number,
  toolCalls: TraceToolCall[] = [],
  purpose: TraceRequest['purpose'] = 'step',
): TraceRequest => ({
  kind: 'request',
  seq,
  number: seq,
  purpose,
  provider: null,
  model: null,
  effort: null,
  startedAt,
  firstOutputAt: startedAt + (finishedAt - startedAt) / 4,
  finishedAt,
  usage: null,
  finish: toolCalls.length ? 'tool-calls' : 'stop',
  text: '',
  reasoning: [],
  toolCalls,
})

const turn = (turnId: string, startedAt: number, items: TraceTurn['items']): TraceTurn => ({
  turnId,
  seq: startedAt,
  model: 'm',
  startedAt,
  finishedAt: null,
  outcome: null,
  error: null,
  usage: { input: 0, output: 0 },
  items,
})

// One turn: a request, two calls that ran together and a research with a request of its own;
// an hour later another turn with one request.
const rows = traceRows([
  turn('t1', 1000, [
    request(1, 1000, 2000, [
      tool('read_a', 2000, 2500),
      tool('read_b', 2000, 2200),
      tool('research', 2500, 4000, [request(10, 2600, 3900, [], 'research')]),
    ]),
    request(2, 4000, 5000),
  ]),
  turn('t2', 3_605_000, [request(3, 3_605_000, 3_606_000)]),
])

describe('trace timeline', () => {
  test('puts calls that ran together in lanes of their own, below the requests', () => {
    const model = timelineModel(rows, 'duration')!
    expect(model.bands).toEqual([
      { group: 'request', lane: 0, lanes: 1 },
      { group: 'tool', lane: 1, lanes: 2 },
      { group: 'research', lane: 3, lanes: 1 },
    ])
    const lane = (key: string) => model.spans.find((span) => span.key === key)!.lane
    expect([lane('1:read_a'), lane('1:read_b'), lane('1:research')]).toEqual([1, 2, 1])
    expect(lane('10')).toBe(3)
  })

  test('in real time leaves out the hour nobody worked, and marks where turns begin', () => {
    const model = timelineModel(rows, 'duration')!
    const second = model.spans.find((span) => span.key === '3')!
    expect(second.start).toBe(5000)
    expect(second.end).toBe(6000)
    expect(model.turns).toEqual([
      { turnId: 't1', at: 1000 },
      { turnId: 't2', at: 5000 },
    ])
    expect(model.spans.find((span) => span.key === '1')!.waited).toBe(0.25)
  })

  test('in steps gives every moment the same width, and calls that ran together line up', () => {
    const model = timelineModel(rows, 'sequence')!
    const at = (key: string) => {
      const span = model.spans.find((item) => item.key === key)!
      return [span.start, span.end]
    }
    // Moments: 1000 2000 2200 2500 2600 3900 4000 5000 3605000 3606000
    expect(at('1')).toEqual([0, 1])
    expect(at('1:read_a')).toEqual([1, 3])
    expect(at('1:read_b')).toEqual([1, 2])
    expect(at('1:research')).toEqual([3, 6])
    expect(at('3')).toEqual([8, 9])
  })

  test('a running call reaches to now', () => {
    const running = traceRows([turn('t', 0, [request(1, 0, 100, [tool('run', 100, null)])])])
    const span = timelineModel(running, 'duration', 400)!.spans.find((s) => s.key === '1:run')!
    expect(span).toMatchObject({ start: 100, end: 400, running: true })
  })

  test('a marked range brings out its rows and the rows they sit under', () => {
    const model = timelineModel(rows, 'duration')!
    expect(rowsInRange(rows, model, { start: 3000, end: 3100 })).toEqual(
      new Set(['10', '1:research', '1', 't1']),
    )
    expect(rowsInRange(rows, model, null)).toBeNull()
  })
})
