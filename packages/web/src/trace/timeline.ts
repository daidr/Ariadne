import type { TraceRow } from './rows'

/** What the timeline draws a span for; each has its own band of lanes. */
export type SpanGroup = 'request' | 'tool' | 'research'

/** `sequence` gives every step the same width; `duration` draws real time, idle gaps left out. */
export type TimelineMode = 'sequence' | 'duration'

export interface TimeRange {
  start: number
  end: number
}

export interface TimelineSpan extends TimeRange {
  key: string
  row: TraceRow
  group: SpanGroup
  /** Its lane counted over all bands, from the top. */
  lane: number
  /** For a request, how far into it the first output came, 0–1. */
  waited: number | null
  /** It has no length of its own: a retry's wait, a switch of model, a cleanup. */
  instant: boolean
  running: boolean
}

export interface TimelineBand {
  group: SpanGroup
  /** The first lane of the band, and how many it has. */
  lane: number
  lanes: number
}

export interface TimelineModel extends TimeRange {
  spans: TimelineSpan[]
  bands: TimelineBand[]
  lanes: number
  /** Where each turn begins, on the timeline's own scale. */
  turns: { turnId: string; at: number }[]
}

interface RawSpan {
  key: string
  row: TraceRow
  group: SpanGroup
  start: number
  end: number
  instant: boolean
  running: boolean
}

const GROUPS: SpanGroup[] = ['request', 'tool', 'research']

function collect(rows: TraceRow[], now: number, inResearch: boolean, out: RawSpan[]) {
  for (const row of rows) {
    if (row.kind !== 'turn') {
      const group: SpanGroup = row.kind === 'tool' ? 'tool' : inResearch ? 'research' : 'request'
      const end = row.end ?? now
      out.push({
        key: row.key,
        row,
        group,
        start: row.start,
        end: Math.max(row.start, end),
        instant: row.kind !== 'request' && row.kind !== 'tool' && row.kind !== 'retry',
        running: row.end === null,
      })
    }
    // A tool's children are the requests of the research it ran.
    if (row.children) collect(row.children, now, inResearch || row.kind === 'tool', out)
  }
}

/** Lanes for spans of one band: each goes in the first lane free when it starts. */
function pack(spans: RawSpan[]): Map<string, number> {
  const ends: number[] = []
  const lane = new Map<string, number>()
  // Same start: in the order the model asked for them.
  for (const span of spans.toSorted((a, b) => a.start - b.start)) {
    let free = ends.findIndex((end) => end <= span.start)
    if (free === -1) free = ends.push(span.end) - 1
    else ends[free] = span.end
    lane.set(span.key, free)
  }
  return lane
}

/**
 * Real time, with the stretches no span covers (the user away between turns) cut out, so
 * a day's turns fit side by side.
 */
function realTime(spans: RawSpan[]): (time: number) => number {
  const covered: TimeRange[] = []
  for (const span of spans.toSorted((a, b) => a.start - b.start)) {
    const last = covered.at(-1)
    if (last && span.start <= last.end) last.end = Math.max(last.end, span.end)
    else covered.push({ start: span.start, end: span.end })
  }
  return (time) => {
    let cut = 0
    for (const [index, range] of covered.entries()) {
      if (index > 0) cut += range.start - covered[index - 1]!.end
      if (time <= range.end) return time - cut
    }
    return time - cut
  }
}

/**
 * Steps instead of time: every moment something starts or ends is one step on, so a quick
 * tool call is as wide as a long request, and calls that ran together still line up.
 */
function steps(spans: RawSpan[]): (time: number) => number {
  const moments = [...new Set(spans.flatMap((span) => [span.start, span.end]))].toSorted(
    (a, b) => a - b,
  )
  const index = new Map(moments.map((moment, i) => [moment, i]))
  return (time) => index.get(time) ?? 0
}

/** The timeline for the trace's rows; `null` with nothing to draw. */
export function timelineModel(
  rows: TraceRow[],
  mode: TimelineMode,
  now = Date.now(),
): TimelineModel | null {
  const raw: RawSpan[] = []
  collect(rows, now, false, raw)
  if (!raw.length) return null
  const bands: TimelineBand[] = []
  const laneOf = new Map<string, number>()
  let lanes = 0
  for (const group of GROUPS) {
    const own = raw.filter((span) => span.group === group)
    if (!own.length) continue
    const packed = pack(own)
    const count = Math.max(...packed.values()) + 1
    for (const [key, lane] of packed) laneOf.set(key, lanes + lane)
    bands.push({ group, lane: lanes, lanes: count })
    lanes += count
  }
  const scale = mode === 'sequence' ? steps(raw) : realTime(raw)
  const spans = raw.map((span): TimelineSpan => {
    let start = scale(span.start)
    let end = scale(span.end)
    // In steps, a moment of its own still gets a step to be seen in.
    if (mode === 'sequence' && end === start) end = start + (span.instant ? 0 : 1)
    if (end < start) [start, end] = [end, start]
    const request = span.row.kind === 'request' ? span.row.request : null
    const waited =
      request?.firstOutputAt && request.finishedAt > request.startedAt
        ? Math.min(
            1,
            Math.max(
              0,
              (request.firstOutputAt - request.startedAt) /
                (request.finishedAt - request.startedAt),
            ),
          )
        : null
    return {
      key: span.key,
      row: span.row,
      group: span.group,
      lane: laneOf.get(span.key) ?? 0,
      start,
      end,
      waited,
      instant: span.instant,
      running: span.running,
    }
  })
  const start = Math.min(...spans.map((span) => span.start))
  const last = Math.max(...spans.map((span) => span.end))
  const turns = rows.flatMap((row) => {
    const first = spans.filter((span) => span.row.turn.turnId === row.key)
    return first.length
      ? [{ turnId: row.key, at: Math.min(...first.map((span) => span.start)) }]
      : []
  })
  return { start, end: last > start ? last : start + 1, spans, bands, lanes, turns }
}

/**
 * The rows a range of the timeline covers, and the rows they sit under (their request, the
 * research call, the turn), so the table shows where they are.
 */
export function rowsInRange(
  rows: TraceRow[],
  model: TimelineModel | null,
  range: TimeRange | null,
): Set<string> | null {
  if (!model || !range) return null
  const inside = new Set(
    model.spans
      .filter((span) => span.start <= range.end && span.end >= range.start)
      .map((span) => span.key),
  )
  const keys = new Set<string>()
  const visit = (row: TraceRow): boolean => {
    let found = inside.has(row.key)
    for (const child of row.children ?? []) if (visit(child)) found = true
    if (found) keys.add(row.key)
    return found
  }
  for (const row of rows) visit(row)
  return keys
}

/** Where a span is drawn: its left edge and width as shares of the whole timeline. */
export function placement(model: TimelineModel, span: TimeRange) {
  const length = Math.max(1, model.end - model.start)
  return {
    left: (span.start - model.start) / length,
    width: (span.end - span.start) / length,
  }
}
