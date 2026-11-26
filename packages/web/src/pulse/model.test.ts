import type { PulseConcurrency, PulseLane, PulseSpan } from '@ariadne/core'
import { describe, expect, test } from 'vitest'
import {
  buildAxisTicks,
  buildConcurrencyBars,
  classifyHandoffTone,
  classifyMarkerTone,
  computeFetchBounds,
  computeLiveViewport,
  filterAndPartitionLanes,
  formatDurationCompact,
  isPulseRelevantEvent,
  MAX_RETENTION_MS,
  MAX_VIEW_SPAN_MS,
  MIN_VIEW_SPAN_MS,
  needsRefetch,
  panViewport,
  resolveSpanVisualKind,
  WINDOW_DURATION_MS,
  zoomViewport,
  type PulseNodeMeta,
} from './model'

const MINUTE = 60_000
const HOUR = 60 * MINUTE
const NOW = 1_795_700_000_000

function lane(partial: Partial<PulseLane> & { conversationId: string }): PulseLane {
  return {
    projectId: 'prj_1',
    projectName: 'Ariadne',
    title: partial.conversationId,
    role: 'worker',
    reviewOf: null,
    stage: 'working',
    completed: false,
    attentionId: null,
    liveCommand: null,
    liveTail: null,
    lastHeadline: null,
    activeAt: NOW - 2 * MINUTE,
    ...partial,
  }
}

function span(partial: Partial<PulseSpan> & { id: number; conversationId: string }): PulseSpan {
  return {
    projectId: 'prj_1',
    stage: 'working',
    occupiesSlot: true,
    startedAt: NOW - 15 * MINUTE,
    endedAt: null,
    seqStart: 1,
    seqEnd: null,
    label: '',
    ...partial,
  }
}

describe('pulse viewport math', () => {
  test('computeLiveViewport reserves a 4% right lead buffer', () => {
    const vp = computeLiveViewport(NOW, WINDOW_DURATION_MS['1h'])
    expect(vp.viewEnd - vp.viewStart).toBe(HOUR)
    expect(vp.viewEnd).toBeCloseTo(NOW + HOUR * 0.04, 0)
  })

  test('zoomViewport preserves anchor timestamp and clamps span within 5m..24h', () => {
    const initial = computeLiveViewport(NOW, HOUR)
    const anchorRatio = 0.5
    const anchorBefore = initial.viewStart + anchorRatio * (initial.viewEnd - initial.viewStart)

    const zoomedIn = zoomViewport(initial.viewStart, initial.viewEnd, anchorRatio, 0.5, NOW)
    const anchorAfter = zoomedIn.viewStart + anchorRatio * (zoomedIn.viewEnd - zoomedIn.viewStart)
    expect(zoomedIn.viewEnd - zoomedIn.viewStart).toBeCloseTo(30 * MINUTE, -1)
    expect(anchorAfter).toBeCloseTo(anchorBefore, -1)

    const clampedMin = zoomViewport(initial.viewStart, initial.viewEnd, 0.5, 0.001, NOW)
    expect(clampedMin.viewEnd - clampedMin.viewStart).toBe(MIN_VIEW_SPAN_MS)

    const clampedMax = zoomViewport(initial.viewStart, initial.viewEnd, 0.5, 1000, NOW)
    expect(clampedMax.viewEnd - clampedMax.viewStart).toBe(MAX_VIEW_SPAN_MS)
  })

  test('panViewport clamps to 30-day retention floor and live lead ceiling', () => {
    const initial = computeLiveViewport(NOW, HOUR)
    const tooFarRight = panViewport(initial.viewStart, initial.viewEnd, 10 * HOUR, NOW)
    expect(tooFarRight.viewEnd).toBeCloseTo(NOW + HOUR * 0.04, 0)

    const tooFarLeft = panViewport(initial.viewStart, initial.viewEnd, -40 * 24 * HOUR, NOW)
    expect(tooFarLeft.viewStart).toBe(NOW - MAX_RETENTION_MS)
    expect(tooFarLeft.viewEnd - tooFarLeft.viewStart).toBe(HOUR)
  })

  test('computeFetchBounds caps historical windows and needsRefetch triggers on right-edge pans', () => {
    const vp = computeLiveViewport(NOW, HOUR)
    const bounds = computeFetchBounds(vp.viewStart, vp.viewEnd, NOW)
    expect(bounds.fromMs).toBeLessThan(vp.viewStart)
    expect(bounds.toMs).toBe(NOW)

    expect(needsRefetch(vp.viewStart, vp.viewEnd, bounds.fromMs, bounds.toMs, NOW)).toBe(false)
    expect(needsRefetch(bounds.fromMs - 1, vp.viewEnd, bounds.fromMs, bounds.toMs, NOW)).toBe(true)
    expect(needsRefetch(vp.viewStart, vp.viewEnd, bounds.fromMs, NOW - 10_000, NOW)).toBe(true)

    // Near-live viewport extends toMs to nowMs when right padding reaches nowMs.
    const nearNow = computeFetchBounds(NOW - 70 * MINUTE, NOW - 10 * MINUTE, NOW)
    expect(nearNow.toMs).toBe(NOW)

    // Historical viewport (e.g. 7 days ago) stays bounded around viewEnd + 25% pad.
    const histStart = NOW - 7 * 24 * HOUR
    const histEnd = histStart + HOUR
    const histBounds = computeFetchBounds(histStart, histEnd, NOW)
    expect(histBounds.fromMs).toBe(histStart - 15 * MINUTE)
    expect(histBounds.toMs).toBe(histEnd + 15 * MINUTE)
    expect(histBounds.toMs).toBeLessThan(NOW)

    // Panning right inside fetched historical bounds does not refetch, but crossing fetchedTo does immediately.
    expect(
      needsRefetch(
        histStart + 10 * MINUTE,
        histEnd + 10 * MINUTE,
        histBounds.fromMs,
        histBounds.toMs,
        NOW,
      ),
    ).toBe(false)
    expect(
      needsRefetch(
        histStart + 16 * MINUTE,
        histBounds.toMs + 1_000,
        histBounds.fromMs,
        histBounds.toMs,
        NOW,
      ),
    ).toBe(true)
  })
})

describe('pulse stage, marker and handoff classification', () => {
  test('resolveSpanVisualKind distinguishes coordinator, reviewer and worker working spans', () => {
    expect(resolveSpanVisualKind({ stage: 'working' }, 'coordinator')).toBe('coord')
    expect(resolveSpanVisualKind({ stage: 'working' }, 'reviewer')).toBe('reviewing')
    expect(resolveSpanVisualKind({ stage: 'working' }, 'worker')).toBe('working')
    expect(resolveSpanVisualKind({ stage: 'review' }, 'worker')).toBe('waiting_review')
    expect(resolveSpanVisualKind({ stage: 'answer' }, 'worker')).toBe('waiting_user')
    expect(resolveSpanVisualKind({ stage: 'ci' }, 'worker')).toBe('waiting_ci')
    expect(resolveSpanVisualKind({ stage: 'halted' }, 'worker')).toBe('blocked')
    expect(resolveSpanVisualKind({ stage: 'queued' }, 'worker')).toBe('queued')
    expect(resolveSpanVisualKind({ stage: 'waiting' }, 'worker')).toBe('waiting')
    expect(resolveSpanVisualKind({ stage: 'aside' }, 'worker')).toBe('aside')
  })

  test('classifyHandoffTone and classifyMarkerTone map outcomes consistently', () => {
    expect(classifyHandoffTone({ kind: 'review_handoff', label: '' })).toBe('request')
    expect(classifyHandoffTone({ kind: 'review_submitted', label: 'pass · LGTM' })).toBe('pass')
    expect(classifyHandoffTone({ kind: 'review_submitted', label: 'changes · fix race' })).toBe(
      'changes',
    )

    expect(classifyMarkerTone({ kind: 'pr_merged', label: '' })).toBe('success')
    expect(classifyMarkerTone({ kind: 'ci_failed', label: '' })).toBe('danger')
    expect(classifyMarkerTone({ kind: 'attention_opened', label: '' })).toBe('warning')
    expect(classifyMarkerTone({ kind: 'review_handoff', label: '' })).toBe('review')
    expect(classifyMarkerTone({ kind: 'review_submitted', label: 'pass' })).toBe('success')
    expect(classifyMarkerTone({ kind: 'review_submitted', label: 'changes' })).toBe('warning')
    expect(classifyMarkerTone({ kind: 'pr_opened', label: '' })).toBe('primary')
    expect(classifyMarkerTone({ kind: 'artifact_updated', label: '' })).toBe('ci')
  })
})

describe('filterAndPartitionLanes', () => {
  const lanes: PulseLane[] = [
    lane({ conversationId: 'crd_1', role: 'coordinator', stage: 'idle', title: '协调器' }),
    lane({
      conversationId: 'thr_author',
      title: '作者任务',
      stage: 'review',
    }),
    lane({
      conversationId: 'thr_reviewer',
      title: '审阅任务',
      role: 'reviewer',
      reviewOf: 'thr_author',
      stage: 'working',
    }),
    lane({
      conversationId: 'thr_needs',
      title: '等待审批',
      stage: 'answer',
      attentionId: 'att_1',
    }),
    lane({
      conversationId: 'thr_done',
      title: '已合并',
      stage: 'done',
      completed: true,
    }),
  ]

  test('pins coordinator, nests reviewer under author, separates needs/active and folds completed', () => {
    const res = filterAndPartitionLanes(lanes, 'all', 'stage')
    expect(res.coordinator?.conversationId).toBe('crd_1')
    expect(res.counts).toEqual({ all: 3, needs: 1, active: 2, completed: 1 })
    expect(res.groups.map((g) => g.kind)).toEqual(['needs', 'active'])
    expect(res.groups[0]!.rows.map((r) => r.lane.conversationId)).toEqual(['thr_needs'])
    expect(res.groups[1]!.rows.map((r) => [r.lane.conversationId, r.nested])).toEqual([
      ['thr_author', false],
      ['thr_reviewer', true],
    ])
    expect(res.completed.map((r) => r.lane.conversationId)).toEqual(['thr_done'])
  })

  test('filters by needs and active', () => {
    const needsOnly = filterAndPartitionLanes(lanes, 'needs', 'stage')
    expect(needsOnly.visibleCount).toBe(1)
    expect(needsOnly.groups.flatMap((g) => g.rows.map((r) => r.lane.conversationId))).toEqual([
      'thr_needs',
    ])

    const activeOnly = filterAndPartitionLanes(lanes, 'active', 'stage')
    expect(activeOnly.visibleCount).toBe(2)
    expect(activeOnly.groups.flatMap((g) => g.rows.map((r) => r.lane.conversationId))).toEqual([
      'thr_author',
      'thr_reviewer',
    ])
  })

  test('groups lanes by plan node order and collects unlinked lanes last', () => {
    const nodes = new Map<string, PulseNodeMeta>([
      ['thr_author', { nodeId: 'node_b', nodeTitle: '切片 B', order: 1 }],
      ['thr_needs', { nodeId: 'node_a', nodeTitle: '切片 A', order: 0 }],
    ])
    const withUnlinked = [...lanes, lane({ conversationId: 'thr_free', title: '临时调研' })]
    const grouped = filterAndPartitionLanes(withUnlinked, 'all', 'node', nodes)

    expect(grouped.groups.map((g) => [g.kind, g.title ?? null])).toEqual([
      ['node', '切片 A'],
      ['node', '切片 B'],
      ['unlinked', null],
    ])
    expect(grouped.groups[1]!.rows.map((r) => [r.lane.conversationId, r.nested])).toEqual([
      ['thr_author', false],
      ['thr_reviewer', true],
    ])
  })
})

describe('buildConcurrencyBars and axis ticks', () => {
  test('renders bars from backend points, marks full bins and omits future bins after nowMs', () => {
    const vp = computeLiveViewport(NOW, HOUR)
    const concurrency: PulseConcurrency = {
      slots: 3,
      current: 3,
      peak: 3,
      points: [
        { at: NOW - 40 * MINUTE, active: 1 },
        { at: NOW - 20 * MINUTE, active: 3 },
      ],
    }
    const bars = buildConcurrencyBars(concurrency, [], vp.viewStart, vp.viewEnd, NOW, 40)
    expect(bars.length).toBeGreaterThan(0)
    expect(bars.every((bar) => bar.midAt <= NOW)).toBe(true)
    expect(bars.some((bar) => bar.full && bar.active === 3)).toBe(true)
    expect(bars.some((bar) => !bar.full && bar.active === 1)).toBe(true)
  })

  test('falls back to span sweep when concurrency points are empty', () => {
    const vp = computeLiveViewport(NOW, HOUR)
    const spans = [
      span({ id: 1, conversationId: 'thr_1', startedAt: NOW - 30 * MINUTE, endedAt: null }),
      span({ id: 2, conversationId: 'thr_2', startedAt: NOW - 15 * MINUTE, endedAt: null }),
    ]
    const bars = buildConcurrencyBars(
      { slots: 2, current: 2, peak: 2, points: [] },
      spans,
      vp.viewStart,
      vp.viewEnd,
      NOW,
      20,
    )
    expect(bars.some((bar) => bar.full && bar.active === 2)).toBe(true)
  })

  test('buildAxisTicks returns clean monotonic ticks inside 0..97%', () => {
    const vp = computeLiveViewport(NOW, HOUR)
    const ticks = buildAxisTicks(vp.viewStart, vp.viewEnd)
    expect(ticks.length).toBeGreaterThanOrEqual(4)
    expect(ticks.every((tick) => tick.pct >= 0 && tick.pct <= 97)).toBe(true)
    for (let i = 1; i < ticks.length; i++) {
      expect(ticks[i]!.at).toBeGreaterThan(ticks[i - 1]!.at)
    }
  })

  test('formatDurationCompact and isPulseRelevantEvent handle common inputs', () => {
    expect(formatDurationCompact(18_000)).toBe('18s')
    expect(formatDurationCompact(5 * MINUTE)).toBe('5m')
    expect(formatDurationCompact(2 * HOUR + 15 * MINUTE)).toBe('2h 15m')
    expect(isPulseRelevantEvent('turn.started')).toBe(true)
    expect(isPulseRelevantEvent('conversation.wait_set')).toBe(true)
    expect(isPulseRelevantEvent('conversation.wait_ended')).toBe(true)
    expect(isPulseRelevantEvent('branch.merged')).toBe(true)
    expect(isPulseRelevantEvent('branch.conflicted')).toBe(true)
    expect(isPulseRelevantEvent('branch.merge_undone')).toBe(true)
    expect(isPulseRelevantEvent('todo.updated')).toBe(true)
    expect(isPulseRelevantEvent('message.posted')).toBe(true)
    expect(isPulseRelevantEvent('routine.ran')).toBe(true)
    expect(isPulseRelevantEvent('branch.changes_synced')).toBe(false)
    expect(isPulseRelevantEvent('turn.step')).toBe(false)
    expect(isPulseRelevantEvent('message.read')).toBe(false)
    expect(isPulseRelevantEvent('message.reacted')).toBe(false)
    expect(isPulseRelevantEvent('message.edited')).toBe(false)
  })
})
