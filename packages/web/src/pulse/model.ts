import type {
  AllNeedsMeResult,
  AnyStoredEvent,
  AttentionView,
  LiveCommand,
  NeedsMeItem,
  NeedsMeThreadSummary,
  PulseConcurrency,
  PulseHandoff,
  PulseLane,
  PulseLaneRole,
  PulseMarker,
  PulseSpan,
  ThreadStage,
  ThreadView,
} from '@ariadne/core'

export const PULSE_WINDOW_KEYS = ['15m', '1h', '6h', '24h'] as const
export type PulseWindowKey = (typeof PULSE_WINDOW_KEYS)[number]

export type PulseScope = 'project' | 'all'
export type PulseFilter = 'all' | 'needs' | 'active'
export type PulseGroupBy = 'stage' | 'node'
export type PulseNarrowTab = 'cards' | 'feed'

export type PulseSpanVisualKind =
  | 'working'
  | 'reviewing'
  | 'waiting_review'
  | 'waiting_user'
  | 'waiting_ci'
  | 'blocked'
  | 'queued'
  | 'waiting'
  | 'aside'
  | 'coord'

/** Restrained 4-group semantic palette shared by desktop bars and narrow mini-strips. */
export type PulseSpanToneGroup = 'active' | 'attention' | 'danger' | 'passive'

export type PulseDouTone = 'neutral' | 'primary' | 'success' | 'warning' | 'danger'

export const DEFAULT_LABEL_COL_WIDTH = 248
export const MIN_LABEL_COL_WIDTH = 192
export const MAX_LABEL_COL_WIDTH = 360

export type PulseMarkerTone = 'primary' | 'success' | 'warning' | 'danger' | 'review' | 'ci'
export type PulseHandoffTone = 'request' | 'pass' | 'changes'

export interface PulseNodeMeta {
  nodeId: string
  nodeTitle: string
  order: number
}

export interface PulseLaneRow {
  lane: PulseLane
  nested: boolean
}

export interface PulseLaneGroup {
  key: string
  kind: 'needs' | 'active' | 'node' | 'unlinked' | 'project'
  title?: string
  projectId?: string
  isCurrent?: boolean
  rows: PulseLaneRow[]
}

export interface PulseLaneCounts {
  all: number
  needs: number
  active: number
  completed: number
}

export interface PulsePartitionedLanes {
  coordinator: PulseLane | null
  groups: PulseLaneGroup[]
  completed: PulseLaneRow[]
  counts: PulseLaneCounts
  visibleCount: number
}

export interface PulseViewport {
  viewStart: number
  viewEnd: number
}

export interface PulseRibbonBar {
  index: number
  leftPct: number
  widthPct: number
  heightPx: number
  active: number
  slots: number
  full: boolean
  midAt: number
}

export interface PulseAxisTick {
  at: number
  pct: number
}

export interface PulseTooltipData {
  title: string
  subtitle?: string
  meta?: string
  detail?: string
}

const MINUTE_MS = 60_000
const HOUR_MS = 60 * MINUTE_MS
const DAY_MS = 24 * HOUR_MS

export const WINDOW_DURATION_MS: Record<PulseWindowKey, number> = {
  '15m': 15 * MINUTE_MS,
  '1h': HOUR_MS,
  '6h': 6 * HOUR_MS,
  '24h': 24 * HOUR_MS,
}

export const MIN_VIEW_SPAN_MS = 5 * MINUTE_MS
export const MAX_VIEW_SPAN_MS = 24 * HOUR_MS
export const MAX_RETENTION_MS = 30 * DAY_MS
export const LIVE_LEAD_RATIO = 0.04

const NICE_TICK_STEPS_MS = [
  MINUTE_MS,
  2 * MINUTE_MS,
  5 * MINUTE_MS,
  10 * MINUTE_MS,
  15 * MINUTE_MS,
  30 * MINUTE_MS,
  HOUR_MS,
  2 * HOUR_MS,
  4 * HOUR_MS,
  6 * HOUR_MS,
]

export function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}

export function percentOf(at: number, viewStart: number, viewEnd: number): number {
  const span = Math.max(1, viewEnd - viewStart)
  return ((at - viewStart) / span) * 100
}

export function computeLiveViewport(nowMs: number, durationMs: number): PulseViewport {
  const safeDuration = clamp(durationMs, MIN_VIEW_SPAN_MS, MAX_VIEW_SPAN_MS)
  const viewEnd = nowMs + safeDuration * LIVE_LEAD_RATIO
  return { viewStart: viewEnd - safeDuration, viewEnd }
}

export function clampViewport(viewStart: number, viewEnd: number, nowMs: number): PulseViewport {
  const span = clamp(viewEnd - viewStart, MIN_VIEW_SPAN_MS, MAX_VIEW_SPAN_MS)
  const earliest = nowMs - MAX_RETENTION_MS
  const latest = nowMs + span * LIVE_LEAD_RATIO
  let end = viewEnd
  let start = end - span
  if (end > latest) {
    end = latest
    start = end - span
  }
  if (start < earliest) {
    start = earliest
    end = start + span
  }
  return { viewStart: start, viewEnd: end }
}

export function zoomViewport(
  viewStart: number,
  viewEnd: number,
  anchorRatio: number,
  factor: number,
  nowMs: number,
): PulseViewport {
  const frac = clamp(anchorRatio, 0, 1)
  const currentLen = Math.max(1, viewEnd - viewStart)
  const anchor = viewStart + frac * currentLen
  const nextLen = clamp(currentLen * factor, MIN_VIEW_SPAN_MS, MAX_VIEW_SPAN_MS)
  const nextStart = anchor - frac * nextLen
  return clampViewport(nextStart, nextStart + nextLen, nowMs)
}

export function panViewport(
  viewStart: number,
  viewEnd: number,
  deltaMs: number,
  nowMs: number,
): PulseViewport {
  return clampViewport(viewStart + deltaMs, viewEnd + deltaMs, nowMs)
}

export function computeFetchBounds(
  viewStart: number,
  viewEnd: number,
  nowMs: number,
): { fromMs: number; toMs: number } {
  const span = Math.max(MIN_VIEW_SPAN_MS, viewEnd - viewStart)
  const pad = Math.round(span * 0.25)
  const earliest = nowMs - MAX_RETENTION_MS
  const fromMs = Math.max(earliest, Math.floor(viewStart - pad))
  const toMs = Math.max(fromMs, Math.min(nowMs, Math.ceil(viewEnd + pad)))
  return { fromMs, toMs }
}

export function needsRefetch(
  viewStart: number,
  viewEnd: number,
  fetchedFrom: number,
  fetchedTo: number,
  nowMs: number,
): boolean {
  if (fetchedTo <= fetchedFrom) return true
  if (viewStart < fetchedFrom) return true
  const visibleEnd = Math.min(viewEnd, nowMs)
  return visibleEnd > fetchedTo
}

export function resolveSpanVisualKind(
  span: Pick<PulseSpan, 'stage'>,
  laneRole: PulseLaneRole,
): PulseSpanVisualKind {
  switch (span.stage) {
    case 'working':
      if (laneRole === 'coordinator') return 'coord'
      if (laneRole === 'reviewer') return 'reviewing'
      return 'working'
    case 'review':
      return 'waiting_review'
    case 'answer':
      return 'waiting_user'
    case 'ci':
      return 'waiting_ci'
    case 'halted':
      return 'blocked'
    case 'queued':
      return 'queued'
    case 'waiting':
      return 'waiting'
    case 'aside':
      return 'aside'
    default:
      return 'waiting'
  }
}

export function resolveSpanToneGroup(kind: PulseSpanVisualKind): PulseSpanToneGroup {
  switch (kind) {
    case 'working':
    case 'reviewing':
      return 'active'
    case 'waiting_user':
      return 'attention'
    case 'blocked':
      return 'danger'
    default:
      return 'passive'
  }
}

export function markerToneToDouTone(tone: PulseMarkerTone): PulseDouTone {
  switch (tone) {
    case 'success':
      return 'success'
    case 'danger':
      return 'danger'
    case 'warning':
      return 'warning'
    case 'primary':
    case 'review':
      return 'primary'
    default:
      return 'neutral'
  }
}

export function classifyHandoffTone(
  handoff: Pick<PulseHandoff, 'kind' | 'label'>,
): PulseHandoffTone {
  if (handoff.kind === 'review_handoff') return 'request'
  const lower = handoff.label.trim().toLowerCase()
  if (lower.startsWith('pass')) return 'pass'
  if (lower.startsWith('changes')) return 'changes'
  return 'request'
}

export function classifyMarkerTone(marker: Pick<PulseMarker, 'kind' | 'label'>): PulseMarkerTone {
  switch (marker.kind) {
    case 'pr_merged':
      return 'success'
    case 'ci_failed':
      return 'danger'
    case 'attention_opened':
      return 'warning'
    case 'review_handoff':
      return 'review'
    case 'review_submitted': {
      const lower = marker.label.trim().toLowerCase()
      if (lower.startsWith('pass')) return 'success'
      if (lower.startsWith('changes')) return 'warning'
      return 'review'
    }
    case 'pr_opened':
      return 'primary'
    case 'artifact_updated':
      return 'ci'
    case 'user_returned':
      return 'primary'
    default:
      return 'primary'
  }
}

export function isNeedsLane(lane: Pick<PulseLane, 'stage' | 'attentionId'>): boolean {
  return (
    lane.stage === 'answer' ||
    lane.stage === 'blocked' ||
    lane.stage === 'halted' ||
    lane.stage === 'silent' ||
    lane.stage === 'stopped' ||
    lane.attentionId !== null
  )
}

export function isActiveLane(lane: Pick<PulseLane, 'stage'>): boolean {
  return (
    lane.stage === 'working' ||
    lane.stage === 'review' ||
    lane.stage === 'queued' ||
    lane.stage === 'waiting'
  )
}

function attachNestedFlags(lanes: readonly PulseLane[]): PulseLaneRow[] {
  const present = new Set(lanes.map((lane) => lane.conversationId))
  return lanes.map((lane) => ({
    lane,
    nested: lane.role === 'reviewer' && lane.reviewOf !== null && present.has(lane.reviewOf),
  }))
}

interface LaneUnit {
  primary: PulseLane
  reviewers: PulseLane[]
}

function buildLaneUnits(lanes: readonly PulseLane[]): LaneUnit[] {
  const byId = new Map(lanes.map((lane) => [lane.conversationId, lane]))
  const reviewersByAuthor = new Map<string, PulseLane[]>()
  const primaries: PulseLane[] = []

  for (const lane of lanes) {
    if (lane.role === 'reviewer' && lane.reviewOf && byId.has(lane.reviewOf)) {
      const list = reviewersByAuthor.get(lane.reviewOf) ?? []
      list.push(lane)
      reviewersByAuthor.set(lane.reviewOf, list)
    } else {
      primaries.push(lane)
    }
  }

  return primaries.map((primary) => ({
    primary,
    reviewers: reviewersByAuthor.get(primary.conversationId) ?? [],
  }))
}

function flattenUnits(units: readonly LaneUnit[]): PulseLaneRow[] {
  const rows: PulseLaneRow[] = []
  for (const unit of units) {
    rows.push({ lane: unit.primary, nested: false })
    for (const reviewer of unit.reviewers) {
      rows.push({ lane: reviewer, nested: true })
    }
  }
  return rows
}

export function filterAndPartitionLanes(
  lanes: readonly PulseLane[],
  filter: PulseFilter,
  groupBy: PulseGroupBy,
  threadNodes: ReadonlyMap<string, PulseNodeMeta> = new Map(),
  scope: PulseScope = 'project',
  currentProjectId: string | null = null,
): PulsePartitionedLanes {
  const activePool = lanes.filter((lane) => lane.role !== 'coordinator' && !lane.completed)
  const completedPool = lanes.filter((lane) => lane.role !== 'coordinator' && lane.completed)
  const countPool = scope === 'all' ? lanes.filter((lane) => !lane.completed) : activePool

  const counts: PulseLaneCounts = {
    all: countPool.length,
    needs: countPool.filter(isNeedsLane).length,
    active: countPool.filter(isActiveLane).length,
    completed: completedPool.length,
  }

  const filtered =
    filter === 'needs'
      ? activePool.filter(isNeedsLane)
      : filter === 'active'
        ? activePool.filter(isActiveLane)
        : activePool

  if (scope === 'all') {
    const coordinators = lanes.filter((lane) => {
      if (lane.role !== 'coordinator' || lane.completed) return false
      if (filter === 'needs') return isNeedsLane(lane)
      if (filter === 'active') return isActiveLane(lane)
      return true
    })

    const byProject = new Map<
      string,
      {
        projectId: string
        projectName: string
        coordinators: PulseLane[]
        workers: PulseLane[]
        latestActiveAt: number
        hasNeeds: boolean
      }
    >()

    const ensureProject = (lane: PulseLane) => {
      const pid = lane.projectId || 'unknown'
      let bucket = byProject.get(pid)
      if (!bucket) {
        bucket = {
          projectId: pid,
          projectName: lane.projectName || pid,
          coordinators: [],
          workers: [],
          latestActiveAt: lane.activeAt,
          hasNeeds: false,
        }
        byProject.set(pid, bucket)
      }
      if (lane.activeAt > bucket.latestActiveAt) bucket.latestActiveAt = lane.activeAt
      if (isNeedsLane(lane)) bucket.hasNeeds = true
      if (!bucket.projectName && lane.projectName) bucket.projectName = lane.projectName
      return bucket
    }

    for (const coord of coordinators) {
      ensureProject(coord).coordinators.push(coord)
    }
    for (const worker of filtered) {
      ensureProject(worker).workers.push(worker)
    }

    const sortedProjects = [...byProject.values()].toSorted((a, b) => {
      const aCur = currentProjectId !== null && a.projectId === currentProjectId
      const bCur = currentProjectId !== null && b.projectId === currentProjectId
      if (aCur !== bCur) return aCur ? -1 : 1
      if (a.hasNeeds !== b.hasNeeds) return a.hasNeeds ? -1 : 1
      if (b.latestActiveAt !== a.latestActiveAt) return b.latestActiveAt - a.latestActiveAt
      return a.projectName.localeCompare(b.projectName) || a.projectId.localeCompare(b.projectId)
    })

    const groups: PulseLaneGroup[] = []
    let visibleCount = 0
    for (const bucket of sortedProjects) {
      const units = buildLaneUnits(bucket.workers)
      const needsUnits: LaneUnit[] = []
      const activeUnits: LaneUnit[] = []
      for (const unit of units) {
        const unitNeeds =
          isNeedsLane(unit.primary) || unit.reviewers.some((reviewer) => isNeedsLane(reviewer))
        if (unitNeeds) needsUnits.push(unit)
        else activeUnits.push(unit)
      }
      const rows: PulseLaneRow[] = [
        ...bucket.coordinators.map((lane) => ({ lane, nested: false })),
        ...flattenUnits(needsUnits),
        ...flattenUnits(activeUnits),
      ]
      if (!rows.length) continue
      visibleCount += rows.length
      groups.push({
        key: `project:${bucket.projectId}`,
        kind: 'project',
        title: bucket.projectName,
        projectId: bucket.projectId,
        isCurrent: currentProjectId !== null && bucket.projectId === currentProjectId,
        rows,
      })
    }

    return {
      coordinator: null,
      groups,
      completed: attachNestedFlags(completedPool),
      counts,
      visibleCount,
    }
  }

  const coordinator = lanes.find((lane) => lane.role === 'coordinator') ?? null
  const units = buildLaneUnits(filtered)
  const groups: PulseLaneGroup[] = []

  if (groupBy === 'node') {
    const byNode = new Map<string, { meta: PulseNodeMeta; units: LaneUnit[] }>()
    const unlinked: LaneUnit[] = []
    for (const unit of units) {
      const meta =
        threadNodes.get(unit.primary.conversationId) ??
        (unit.primary.reviewOf ? threadNodes.get(unit.primary.reviewOf) : undefined)
      if (meta) {
        const entry = byNode.get(meta.nodeId) ?? { meta, units: [] }
        entry.units.push(unit)
        byNode.set(meta.nodeId, entry)
      } else {
        unlinked.push(unit)
      }
    }
    const sortedNodes = [...byNode.values()].toSorted(
      (a, b) => a.meta.order - b.meta.order || a.meta.nodeTitle.localeCompare(b.meta.nodeTitle),
    )
    for (const entry of sortedNodes) {
      groups.push({
        key: `node:${entry.meta.nodeId}`,
        kind: 'node',
        title: entry.meta.nodeTitle,
        rows: flattenUnits(entry.units),
      })
    }
    if (unlinked.length) {
      groups.push({
        key: 'node:unlinked',
        kind: 'unlinked',
        rows: flattenUnits(unlinked),
      })
    }
  } else {
    const needsUnits: LaneUnit[] = []
    const activeUnits: LaneUnit[] = []
    for (const unit of units) {
      const unitNeeds =
        isNeedsLane(unit.primary) || unit.reviewers.some((reviewer) => isNeedsLane(reviewer))
      if (unitNeeds) needsUnits.push(unit)
      else activeUnits.push(unit)
    }
    if (needsUnits.length) {
      groups.push({
        key: 'stage:needs',
        kind: 'needs',
        rows: flattenUnits(needsUnits),
      })
    }
    if (activeUnits.length) {
      groups.push({
        key: 'stage:active',
        kind: 'active',
        rows: flattenUnits(activeUnits),
      })
    }
  }

  return {
    coordinator,
    groups,
    completed: attachNestedFlags(completedPool),
    counts,
    visibleCount: filtered.length,
  }
}

export function countLiveSlotOccupancy(spans: readonly PulseSpan[]): number {
  const occupied = new Set<string>()
  for (const span of spans) {
    if (span.occupiesSlot && span.endedAt === null) {
      occupied.add(span.conversationId)
    }
  }
  return occupied.size
}

interface StepInterval {
  start: number
  end: number
  active: number
}

function buildStepIntervals(
  concurrency: PulseConcurrency,
  spans: readonly PulseSpan[],
  nowMs: number,
): StepInterval[] {
  const pts = concurrency.points
  if (pts.length > 0) {
    const intervals: StepInterval[] = []
    for (let i = 0; i < pts.length; i++) {
      const curr = pts[i]!
      const next = pts[i + 1]
      const end = next ? next.at : Math.max(curr.at, nowMs)
      if (end > curr.at) {
        intervals.push({ start: curr.at, end, active: curr.active })
      }
    }
    const lastPt = pts[pts.length - 1]!
    if (nowMs > lastPt.at) {
      const tail = intervals[intervals.length - 1]
      if (tail && tail.start === lastPt.at) tail.end = nowMs
      else intervals.push({ start: lastPt.at, end: nowMs, active: lastPt.active })
    }
    return intervals
  }

  // Fallback sweep if concurrency points are absent.
  const events: { at: number; delta: number }[] = []
  for (const span of spans) {
    if (!span.occupiesSlot) continue
    const end = span.endedAt ?? nowMs
    if (end <= span.startedAt) continue
    events.push({ at: span.startedAt, delta: 1 }, { at: end, delta: -1 })
  }
  events.sort((a, b) => a.at - b.at)
  const intervals: StepInterval[] = []
  let active = 0
  let i = 0
  while (i < events.length) {
    const t = events[i]!.at
    while (i < events.length && events[i]!.at === t) {
      active = Math.max(0, active + events[i]!.delta)
      i++
    }
    const nextAt = i < events.length ? events[i]!.at : nowMs
    if (nextAt > t) intervals.push({ start: t, end: nextAt, active })
  }
  return intervals
}

export function buildConcurrencyBars(
  concurrency: PulseConcurrency,
  spans: readonly PulseSpan[],
  viewStart: number,
  viewEnd: number,
  nowMs: number,
  bins = 48,
): PulseRibbonBar[] {
  const len = Math.max(1, viewEnd - viewStart)
  const step = len / bins
  const intervals = buildStepIntervals(concurrency, spans, nowMs)
  if (!intervals.length) return []

  const displaySlots = Math.max(
    1,
    concurrency.slots > 0 ? concurrency.slots : Math.max(concurrency.peak, 1),
  )
  const bars: PulseRibbonBar[] = []
  let ptr = 0

  for (let i = 0; i < bins; i++) {
    const binStart = viewStart + i * step
    if (binStart >= nowMs) break
    const binEnd = Math.min(binStart + step, nowMs)
    if (binEnd <= binStart) continue

    while (ptr < intervals.length && intervals[ptr]!.end <= binStart) ptr++

    let maxActive = 0
    for (let j = ptr; j < intervals.length; j++) {
      const iv = intervals[j]!
      if (iv.start >= binEnd) break
      if (iv.active > maxActive) maxActive = iv.active
    }
    if (maxActive <= 0) continue

    const heightPx = Math.max(
      4,
      Math.round((Math.min(maxActive, displaySlots) / displaySlots) * 20),
    )
    const full = concurrency.slots > 0 && maxActive >= concurrency.slots
    bars.push({
      index: i,
      leftPct: (i / bins) * 100,
      widthPct: Math.max(0.4, (1 / bins) * 100 - 0.15),
      heightPx,
      active: maxActive,
      slots: concurrency.slots > 0 ? concurrency.slots : displaySlots,
      full,
      midAt: (binStart + binEnd) / 2,
    })
  }

  return bars
}

export function buildAxisTicks(viewStart: number, viewEnd: number, minGapPct = 0): PulseAxisTick[] {
  const len = Math.max(1, viewEnd - viewStart)
  const rawStep = len / 6
  const step = NICE_TICK_STEPS_MS.find((candidate) => candidate >= rawStep) ?? 6 * HOUR_MS
  const first = Math.ceil(viewStart / step) * step
  const ticks: PulseAxisTick[] = []
  let lastPct = -Infinity
  for (let at = first; at <= viewEnd; at += step) {
    const pct = percentOf(at, viewStart, viewEnd)
    if (pct < 0 || pct > 97) continue
    if (minGapPct > 0 && pct - lastPct < minGapPct) continue
    ticks.push({ at, pct })
    lastPct = pct
  }
  return ticks
}

export interface PulseAxisHeaderLayout {
  visibleLabelTicks: PulseAxisTick[]
  nowAlign: 'left' | 'right'
  showReturnBadge: boolean
  returnAlign: 'left' | 'right'
}

export function spansMultipleDays(viewStart: number, viewEnd: number): boolean {
  const startDate = new Date(viewStart)
  const endDate = new Date(viewEnd)
  return (
    startDate.getFullYear() !== endDate.getFullYear() ||
    startDate.getMonth() !== endDate.getMonth() ||
    startDate.getDate() !== endDate.getDate()
  )
}

/**
 * Computes collision-free label placement along the time axis header so tick
 * timestamps, the live "now" badge, and the "last away" badge never overlap
 * while vertical gridlines keep rendering at every tick.
 */
export function computeAxisHeaderLayout(params: {
  ticks: readonly PulseAxisTick[]
  trackWidthPx: number
  nowPct: number | null
  returnPct: number | null
  spansDays: boolean
}): PulseAxisHeaderLayout {
  const width = Math.max(120, params.trackWidthPx)
  const gap = 8
  const tickLabelW = params.spansDays ? 86 : 48
  const nowBadgeW = params.spansDays ? 116 : 84
  const retBadgeW = params.spansDays ? 132 : 98

  const nowX = params.nowPct !== null ? (params.nowPct / 100) * width : null
  // Default to placing the "now" badge to the left of the vertical now line so it
  // stays inside the right edge at live tail (96%). Flip to the right only when
  // the now line is close to the left edge.
  const nowAlign: 'left' | 'right' = nowX !== null && nowX < nowBadgeW + gap ? 'left' : 'right'
  const nowRange: [number, number] | null =
    nowX === null
      ? null
      : nowAlign === 'right'
        ? [nowX - 4 - nowBadgeW, nowX + 4]
        : [nowX - 4, nowX + 4 + nowBadgeW]

  const retX = params.returnPct !== null ? (params.returnPct / 100) * width : null
  let returnAlign: 'left' | 'right' = 'left'
  let retRange: [number, number] | null = null
  let showReturnBadge = false

  if (retX !== null && retX >= 0 && retX <= width) {
    // Prefer placing the away badge to the right of the dashed away line, matching
    // the direction of the shaded away band; fall back to the opposite side if the
    // preferred side clips the edge or collides with the live "now" badge.
    const preferred: 'left' | 'right' = retX + 4 + retBadgeW > width - 4 ? 'right' : 'left'
    const candidates: readonly ('left' | 'right')[] =
      preferred === 'left' ? ['left', 'right'] : ['right', 'left']
    for (const align of candidates) {
      const range: [number, number] =
        align === 'left' ? [retX - 4, retX + 4 + retBadgeW] : [retX - 4 - retBadgeW, retX + 4]
      const overlapsNow =
        nowRange !== null && range[0] < nowRange[1] + gap && range[1] > nowRange[0] - gap
      if (!overlapsNow && range[0] >= 0 && range[1] <= width) {
        returnAlign = align
        showReturnBadge = true
        retRange = range
        break
      }
    }
  }

  const visibleLabelTicks: PulseAxisTick[] = []
  let prevRight = -Infinity
  for (const tick of params.ticks) {
    const x = (tick.pct / 100) * width
    const left = x
    const right = x + tickLabelW
    if (left < 0 || right > width - 4) continue
    if (left < prevRight + gap) continue
    if (nowRange && left < nowRange[1] + gap && right > nowRange[0] - gap) continue
    if (retRange && left < retRange[1] + gap && right > retRange[0] - gap) continue
    visibleLabelTicks.push(tick)
    prevRight = right
  }

  return {
    visibleLabelTicks,
    nowAlign,
    showReturnBadge,
    returnAlign,
  }
}

export function formatClockShort(
  ms: number,
  viewStart: number,
  viewEnd: number,
  locale: string,
): string {
  const date = new Date(ms)
  if (spansMultipleDays(viewStart, viewEnd)) {
    return date.toLocaleString(locale, {
      month: 'numeric',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
    })
  }
  return date.toLocaleTimeString(locale, {
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
}

export function formatDurationCompact(ms: number): string {
  const safe = Math.max(0, ms)
  if (safe < 60_000) {
    const secs = Math.max(1, Math.round(safe / 1000))
    return secs < 60 ? `${secs}s` : '1m'
  }
  const mins = Math.round(safe / 60_000)
  if (mins < 60) return `${mins}m`
  const hours = Math.floor(mins / 60)
  const rem = mins % 60
  return rem ? `${hours}h ${rem}m` : `${hours}h`
}

const PULSE_RELEVANT_PREFIXES = new Set([
  'turn',
  'thread',
  'attention',
  'review',
  'pull_request',
  'checks',
  'branch',
  'artifact',
  'plan',
  'autopilot',
  'workspace',
  'user',
])

const PULSE_RELEVANT_EVENTS = new Set<AnyStoredEvent['type']>([
  'conversation.wait_set',
  'conversation.wait_ended',
  'message.posted',
  'todo.updated',
  'routine.ran',
])

const PULSE_IGNORED_EVENTS = new Set([
  'turn.changed',
  'turn.step',
  'branch.changes_synced',
  'message.read',
  'message.reacted',
  'prompt.recorded',
  'tool.finished',
])

export function isPulseRelevantEvent(type: AnyStoredEvent['type']): boolean {
  if (PULSE_IGNORED_EVENTS.has(type)) return false
  if (PULSE_RELEVANT_EVENTS.has(type)) return true
  const prefix = type.split('.')[0] ?? ''
  return PULSE_RELEVANT_PREFIXES.has(prefix)
}

const KNOWN_THREAD_STAGES: ReadonlySet<ThreadStage> = new Set([
  'answer',
  'stopped',
  'halted',
  'silent',
  'working',
  'blocked',
  'review',
  'done',
  'queued',
  'idle',
])

export function resolveLaneStage(
  lane: Pick<PulseLane, 'stage' | 'attentionId' | 'completed'>,
): ThreadStage {
  if (lane.attentionId !== null || lane.stage === 'answer') return 'answer'
  if (lane.stage === 'waiting') return 'queued'
  if (lane.completed) return 'done'
  if (KNOWN_THREAD_STAGES.has(lane.stage as ThreadStage)) return lane.stage as ThreadStage
  return 'idle'
}

export function liveCommandLabel(
  lane: Pick<PulseLane, 'conversationId' | 'liveCommand'>,
  liveCommands: Readonly<Record<string, LiveCommand>>,
  maxChars = 64,
): string | null {
  const raw = liveCommands[lane.conversationId]?.command ?? lane.liveCommand
  if (!raw) return null
  const firstLine = raw.split('\n')[0]?.trim() ?? ''
  if (!firstLine) return null
  return firstLine.length > maxChars ? `${firstLine.slice(0, maxChars)}…` : firstLine
}

export function resolveLaneAttentions(
  lane: Pick<PulseLane, 'conversationId' | 'projectId' | 'role' | 'attentionId'>,
  currentProjectId: string | null,
  currentNeedsMe: readonly NeedsMeItem[] | undefined,
  allNeedsMe: AllNeedsMeResult | null | undefined,
): AttentionView[] {
  const seen = new Set<string>()
  const out: AttentionView[] = []

  const matches = (item: Extract<NeedsMeItem, { kind: 'attention' }>) => {
    if (lane.attentionId !== null && item.attention.id === lane.attentionId) return true
    if (item.threadId === lane.conversationId) return true
    if (item.joinedIds.includes(lane.conversationId)) return true
    if (
      lane.role === 'coordinator' &&
      item.threadId === null &&
      item.attention.conversationId === lane.conversationId
    ) {
      return true
    }
    return false
  }

  const push = (attention: AttentionView) => {
    if (seen.has(attention.id)) return
    seen.add(attention.id)
    out.push(attention)
  }

  if (lane.projectId === currentProjectId && currentNeedsMe !== undefined) {
    for (const item of currentNeedsMe) {
      if (item.kind === 'attention' && matches(item)) push(item.attention)
    }
    return out
  }

  if (allNeedsMe) {
    for (const entry of allNeedsMe.items) {
      if (entry.projectId !== lane.projectId) continue
      if (entry.item.kind === 'attention' && matches(entry.item)) push(entry.item.attention)
    }
  }

  return out
}

export function resolveLaneThreadSummary(
  lane: Pick<PulseLane, 'conversationId' | 'projectId' | 'role'>,
  currentProjectId: string | null,
  currentThreads: ReadonlyMap<string, ThreadView>,
  allNeedsMe: AllNeedsMeResult | null | undefined,
): ThreadView | NeedsMeThreadSummary | null {
  if (lane.role === 'coordinator') return null
  if (lane.projectId === currentProjectId) {
    return currentThreads.get(lane.conversationId) ?? null
  }
  return currentThreads.get(lane.conversationId) ?? allNeedsMe?.threads[lane.conversationId] ?? null
}

export interface PulseFeedItem {
  key: string
  at: number
  seq: number
  conversationId: string
  projectId: string
  projectName: string
  laneTitle: string
  laneRole: PulseLaneRole
  kind: PulseMarker['kind']
  tone: PulseMarkerTone
  label: string
}

export function buildPulseFeed(
  lanes: readonly PulseLane[],
  markers: readonly PulseMarker[],
  viewStart: number,
  viewEnd: number,
): PulseFeedItem[] {
  const lanesById = new Map(lanes.map((lane) => [lane.conversationId, lane]))
  const items: PulseFeedItem[] = []
  for (const marker of markers) {
    if (marker.kind === 'user_returned') continue
    if (marker.at < viewStart || marker.at > viewEnd) continue
    const lane = lanesById.get(marker.conversationId)
    items.push({
      key: `${marker.conversationId}:${marker.seq}:${marker.kind}`,
      at: marker.at,
      seq: marker.seq,
      conversationId: marker.conversationId,
      projectId: marker.projectId || lane?.projectId || '',
      projectName: lane?.projectName ?? '',
      laneTitle: lane?.title || marker.conversationId,
      laneRole: lane?.role ?? 'worker',
      kind: marker.kind,
      tone: classifyMarkerTone(marker),
      label: marker.label,
    })
  }
  items.sort((a, b) => b.at - a.at || b.seq - a.seq || a.key.localeCompare(b.key))
  return items
}
