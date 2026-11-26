<script setup lang="ts">
import type {
  LiveCommand,
  PulseConcurrency,
  PulseHandoff,
  PulseLane,
  PulseMarker,
  PulseSpan,
  ThreadView,
} from '@ariadne/core'
import { ChevronDownIcon, ChevronRightIcon } from '@dou-ui/icons'
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  shallowRef,
  useTemplateRef,
  watch,
} from 'vue'
import { locale, t } from '../i18n'
import { branchChanges } from '../review/merge-offered'
import {
  buildAxisTicks,
  buildConcurrencyBars,
  clamp,
  classifyHandoffTone,
  classifyMarkerTone,
  countLiveSlotOccupancy,
  formatClockShort,
  formatDurationCompact,
  isNeedsLane,
  percentOf,
  resolveSpanVisualKind,
  type PulseDensity,
  type PulseHandoffTone,
  type PulseLaneRow,
  type PulsePartitionedLanes,
  type PulseRibbonBar,
  type PulseSpanVisualKind,
  type PulseTooltipData,
} from './model'

const LABEL_COL_WIDTH = 258
const FALLBACK_TRACK_WIDTH = 800

const props = defineProps<{
  partitioned: PulsePartitionedLanes
  spansByConversation: ReadonlyMap<string, PulseSpan[]>
  markersByConversation: ReadonlyMap<string, PulseMarker[]>
  handoffs: readonly PulseHandoff[]
  concurrency: PulseConcurrency
  allSpans: readonly PulseSpan[]
  lastReturnedAt: number | null
  nowMs: number
  viewStart: number
  viewEnd: number
  density: PulseDensity
  showConcurrency: boolean
  showHandoffs: boolean
  showReturnLine: boolean
  showCompleted: boolean
  selectedLaneId: string | null
  selectedSpanId: number | null
  threads: ReadonlyMap<string, ThreadView>
  liveCommands: Readonly<Record<string, LiveCommand>>
}>()

const emit = defineEmits<{
  'update:showCompleted': [value: boolean]
  select: [laneId: string, spanId: number | null]
  openLane: [lane: PulseLane]
  zoom: [anchorRatio: number, factor: number]
  pan: [deltaMs: number]
  resetWindow: []
  hoveringChange: [hovering: boolean]
}>()

const scrollEl = useTemplateRef<HTMLDivElement>('scrollEl')
const axisTrackEl = useTemplateRef<HTMLDivElement>('axisTrackEl')
const lanesInnerEl = useTemplateRef<HTMLDivElement>('lanesInnerEl')

const isCompact = computed(() => props.density === 'compact')
const laneHeightPx = computed(() => (isCompact.value ? 34 : 44))
const spanHeightPx = computed(() => (isCompact.value ? 15 : 19))

const ribbonBars = computed(() =>
  props.showConcurrency
    ? buildConcurrencyBars(
        props.concurrency,
        props.allSpans,
        props.viewStart,
        props.viewEnd,
        props.nowMs,
        48,
      )
    : [],
)

const liveSlotActive = computed(() => {
  const fromOpenSpans = countLiveSlotOccupancy(props.allSpans)
  return Math.max(fromOpenSpans, props.concurrency.current)
})

const slotFull = computed(
  () => props.concurrency.slots > 0 && liveSlotActive.value >= props.concurrency.slots,
)

const slotBadgeText = computed(() => {
  const slots = props.concurrency.slots
  if (slots <= 0) return String(liveSlotActive.value)
  return slotFull.value
    ? t('pulse.ribbon.full', { active: liveSlotActive.value, slots })
    : t('pulse.ribbon.usage', { active: liveSlotActive.value, slots })
})

const axisTicks = computed(() => buildAxisTicks(props.viewStart, props.viewEnd))

const windowRangeText = computed(() => {
  const startText = formatClockShort(props.viewStart, props.viewStart, props.viewEnd, locale.value)
  const endAt = Math.min(props.viewEnd, props.nowMs)
  const endText = formatClockShort(
    Math.max(props.viewStart, endAt),
    props.viewStart,
    props.viewEnd,
    locale.value,
  )
  return `${startText} — ${endText}`
})

const nowPct = computed(() => percentOf(props.nowMs, props.viewStart, props.viewEnd))
const showNowLine = computed(() => nowPct.value >= 0 && nowPct.value <= 100)

const returnGeometry = computed(() => {
  if (!props.showReturnLine || props.lastReturnedAt === null) return null
  const retPct = percentOf(props.lastReturnedAt, props.viewStart, props.viewEnd)
  const endPct = Math.min(100, Math.max(retPct, nowPct.value))
  if (endPct <= 0 || retPct >= 100) return null
  const showLine = retPct >= 0 && retPct <= 100
  const showBadge = retPct >= 0 && retPct <= 96
  const bandLeft = Math.max(0, retPct)
  const bandWidth = Math.max(0, endPct - bandLeft)
  return {
    at: props.lastReturnedAt,
    retPct,
    showLine,
    showBadge,
    bandLeft,
    bandWidth,
    timeText: formatClockShort(props.lastReturnedAt, props.viewStart, props.viewEnd, locale.value),
  }
})

const nowTimeText = computed(() =>
  formatClockShort(props.nowMs, props.viewStart, props.viewEnd, locale.value),
)

const completedPreviewTitles = computed(() => {
  const titles = props.partitioned.completed
    .slice(0, 3)
    .map((row) => row.lane.title || row.lane.conversationId)
  if (props.partitioned.completed.length > 3) titles.push('…')
  return titles.join('、')
})

interface RenderedSpan {
  span: PulseSpan
  visualKind: PulseSpanVisualKind
  leftPct: number
  widthPct: number
  running: boolean
  showText: boolean
  inlineText: string
  liveCommand: string | null
  endAt: number
}

function liveCommandText(lane: PulseLane): string | null {
  const raw = props.liveCommands[lane.conversationId]?.command ?? lane.liveCommand
  if (!raw) return null
  const firstLine = raw.split('\n')[0]?.trim() ?? ''
  if (!firstLine) return null
  return firstLine.length > 64 ? `${firstLine.slice(0, 64)}…` : firstLine
}

function spansForLane(lane: PulseLane): RenderedSpan[] {
  const list = props.spansByConversation.get(lane.conversationId) ?? []
  const out: RenderedSpan[] = []
  const liveCmd = liveCommandText(lane)
  for (const span of list) {
    const endAt = span.endedAt ?? props.nowMs
    if (endAt < props.viewStart || span.startedAt > props.viewEnd) continue
    const leftPct = Math.max(0, percentOf(span.startedAt, props.viewStart, props.viewEnd))
    const rightPct = Math.min(100, percentOf(endAt, props.viewStart, props.viewEnd))
    const widthPct = Math.max(0.65, rightPct - leftPct)
    const running = span.endedAt === null
    const visualKind = resolveSpanVisualKind(span, lane.role)
    const stageText = t(`pulse.stage.${visualKind}`)
    const baseLabel =
      span.label || (running && span.stage === 'working' && liveCmd ? liveCmd : stageText)
    const inlineText =
      running && visualKind === 'waiting_user'
        ? `${baseLabel} ${formatDurationCompact(endAt - span.startedAt)}`
        : baseLabel
    out.push({
      span,
      visualKind,
      leftPct,
      widthPct,
      running,
      showText: widthPct > 6.5,
      inlineText,
      liveCommand: running && span.stage === 'working' ? liveCmd : null,
      endAt,
    })
  }
  return out
}

interface RenderedMarker {
  marker: PulseMarker
  pct: number
  tone: ReturnType<typeof classifyMarkerTone>
}

function markersForLane(lane: PulseLane): RenderedMarker[] {
  const list = props.markersByConversation.get(lane.conversationId) ?? []
  const out: RenderedMarker[] = []
  for (const marker of list) {
    if (marker.kind === 'user_returned') continue
    if (marker.at < props.viewStart || marker.at > props.viewEnd) continue
    out.push({
      marker,
      pct: percentOf(marker.at, props.viewStart, props.viewEnd),
      tone: classifyMarkerTone(marker),
    })
  }
  return out
}

function laneTitle(lane: PulseLane): string {
  if (lane.role === 'coordinator') return t('coordinator.title')
  return lane.title || lane.conversationId
}

function laneDotClass(lane: PulseLane): string {
  if (lane.role === 'coordinator') return 'is-neutral'
  if (lane.stage === 'answer' || lane.attentionId !== null) return 'is-warning'
  if (lane.stage === 'blocked' || lane.stage === 'halted') return 'is-danger'
  if (lane.completed || lane.stage === 'done') return 'is-success'
  if (lane.stage === 'review') return 'is-ci'
  if (lane.role === 'reviewer' && lane.stage === 'working') return 'is-review is-pulse'
  if (lane.stage === 'working') return 'is-primary is-pulse'
  return 'is-neutral'
}

interface LaneMetaSummary {
  pr: string | null
  diff: { added: number; removed: number } | null
  progress: string | null
  fallback: string | null
}

function laneMeta(lane: PulseLane): LaneMetaSummary {
  const liveCmd = liveCommandText(lane)
  if (lane.role === 'coordinator') {
    return {
      pr: null,
      diff: null,
      progress: null,
      fallback: liveCmd ? `$ ${liveCmd}` : (lane.lastHeadline ?? t('pulse.stage.coord')),
    }
  }
  const thread = props.threads.get(lane.conversationId)
  const pr = thread?.pullRequest?.number ? `#${thread.pullRequest.number}` : null
  const changes = thread ? branchChanges(thread) : null
  const diff = changes ? { added: changes.added, removed: changes.removed } : null
  const progress =
    thread?.progress && thread.progress.total > 0 && thread.progress.done < thread.progress.total
      ? `${thread.progress.done}/${thread.progress.total}`
      : null
  const fallback =
    pr || diff || progress ? null : liveCmd ? `$ ${liveCmd}` : (lane.lastHeadline ?? null)
  return { pr, diff, progress, fallback }
}

function laneAlertBadge(lane: PulseLane): { tone: 'warning' | 'danger'; text: string } | null {
  if (lane.stage === 'answer' || lane.attentionId !== null) {
    const spans = props.spansByConversation.get(lane.conversationId) ?? []
    const openSpan = spans.findLast((span) => span.endedAt === null)
    const since = openSpan?.startedAt ?? lane.activeAt
    return { tone: 'warning', text: formatDurationCompact(Math.max(0, props.nowMs - since)) }
  }
  if (lane.stage === 'blocked' || lane.stage === 'halted') {
    const failures = props.threads.get(lane.conversationId)?.pullRequest?.checks.failed.length ?? 0
    return { tone: 'danger', text: failures > 0 ? `×${failures}` : '!' }
  }
  return null
}

function laneRowClasses(row: PulseLaneRow): Record<string, boolean> {
  const { lane, nested } = row
  return {
    'is-compact': isCompact.value,
    'is-coordinator': lane.role === 'coordinator',
    'is-nested': nested,
    'is-needs-me': lane.stage === 'answer' || lane.attentionId !== null,
    'is-blocked': lane.stage === 'blocked' || lane.stage === 'halted',
    'is-selected': props.selectedLaneId === lane.conversationId,
  }
}

function groupHeaderTitle(group: PulsePartitionedLanes['groups'][number]): string {
  if (group.kind === 'needs') return t('pulse.group.needsTitle')
  if (group.kind === 'active') return t('pulse.group.activeTitle')
  if (group.kind === 'node') return t('pulse.group.nodePrefix', { title: group.title ?? '' })
  return t('pulse.group.unlinkedNode')
}

// --- Handoff SVG arc calculation ---
interface HandoffArc {
  key: string
  handoff: PulseHandoff
  tone: PulseHandoffTone
  d: string
  x: number
  y2: number
}

const handoffSvgSize = shallowRef({ width: FALLBACK_TRACK_WIDTH, height: 0 })
const handoffArcs = shallowRef<HandoffArc[]>([])
let handoffFrame: number | null = null

function computeHandoffArcsNow() {
  handoffFrame = null
  if (!props.showHandoffs || !props.handoffs.length) {
    handoffArcs.value = []
    return
  }

  const inner = lanesInnerEl.value
  const trackWidth = Math.max(
    1,
    inner && inner.clientWidth > LABEL_COL_WIDTH
      ? inner.clientWidth - LABEL_COL_WIDTH
      : FALLBACK_TRACK_WIDTH,
  )

  const laneCenters = new Map<string, number>()
  const visibleOrder: string[] = []
  if (props.partitioned.coordinator) visibleOrder.push(props.partitioned.coordinator.conversationId)
  for (const group of props.partitioned.groups) {
    for (const row of group.rows) visibleOrder.push(row.lane.conversationId)
  }
  if (props.showCompleted) {
    for (const row of props.partitioned.completed) visibleOrder.push(row.lane.conversationId)
  }

  // Use offsetHeight (flow height) rather than scrollHeight so the absolutely
  // positioned handoff SVG cannot inflate subsequent height measurements.
  let measuredHeight = inner?.offsetHeight ?? 0
  if (inner) {
    for (const el of inner.querySelectorAll<HTMLElement>('[data-lane-id]')) {
      const id = el.dataset.laneId
      if (!id) continue
      if (el.offsetHeight > 0) {
        laneCenters.set(id, el.offsetTop + el.offsetHeight / 2)
        measuredHeight = Math.max(measuredHeight, el.offsetTop + el.offsetHeight)
      }
    }
  }

  // Deterministic fallback for headless DOM environments without layout geometry.
  if (laneCenters.size === 0 && visibleOrder.length > 0) {
    const stride = laneHeightPx.value
    visibleOrder.forEach((id, idx) => {
      laneCenters.set(id, idx * stride + stride / 2)
    })
    measuredHeight = visibleOrder.length * stride
  }

  handoffSvgSize.value = { width: trackWidth, height: Math.max(1, measuredHeight) }

  const arcs: HandoffArc[] = []
  for (const handoff of props.handoffs) {
    if (handoff.at < props.viewStart || handoff.at > props.viewEnd) continue
    const y1 = laneCenters.get(handoff.fromConversationId)
    const y2 = laneCenters.get(handoff.toConversationId)
    if (y1 === undefined || y2 === undefined) continue
    const x = clamp(
      (percentOf(handoff.at, props.viewStart, props.viewEnd) / 100) * trackWidth,
      4,
      Math.max(4, trackWidth - 4),
    )
    const bend = y2 >= y1 ? 16 : -16
    const d = `M ${x} ${y1} C ${x + bend} ${y1}, ${x + bend} ${y2}, ${x} ${y2}`
    arcs.push({
      key: `${handoff.seq}:${handoff.fromConversationId}:${handoff.toConversationId}`,
      handoff,
      tone: classifyHandoffTone(handoff),
      d,
      x,
      y2,
    })
  }
  handoffArcs.value = arcs
}

function scheduleHandoffRecalc() {
  if (typeof requestAnimationFrame !== 'function') {
    computeHandoffArcsNow()
    return
  }
  if (handoffFrame !== null) cancelAnimationFrame(handoffFrame)
  handoffFrame = requestAnimationFrame(computeHandoffArcsNow)
}

watch(
  [
    () => props.handoffs,
    () => props.partitioned,
    () => props.density,
    () => props.showCompleted,
    () => props.showHandoffs,
    () => props.viewStart,
    () => props.viewEnd,
  ],
  async () => {
    await nextTick()
    computeHandoffArcsNow()
  },
  { immediate: true, flush: 'post' },
)

let resizeObserver: ResizeObserver | null = null
onMounted(() => {
  if (typeof ResizeObserver !== 'undefined' && lanesInnerEl.value) {
    resizeObserver = new ResizeObserver(() => scheduleHandoffRecalc())
    resizeObserver.observe(lanesInnerEl.value)
  }
  computeHandoffArcsNow()
})

// --- Pointer drag pan & wheel zoom ---
let panState: { pointerId: number; lastX: number; width: number } | null = null
const panning = ref(false)

function getTrackRect(): DOMRect | null {
  return axisTrackEl.value?.getBoundingClientRect() ?? null
}

function onTrackPointerDown(event: PointerEvent) {
  if (event.button !== 0) return
  if (
    event.target instanceof Element &&
    event.target.closest('.pulse-span, .pulse-marker, .pulse-handoff-hit, button, a')
  ) {
    return
  }
  const rect = getTrackRect()
  if (!rect) return
  panState = {
    pointerId: event.pointerId,
    lastX: event.clientX,
    width: Math.max(1, rect.width),
  }
  panning.value = true
  window.addEventListener('pointermove', onWindowPointerMove)
  window.addEventListener('pointerup', onWindowPointerUp)
  window.addEventListener('pointercancel', onWindowPointerUp)
}

function onWindowPointerMove(event: PointerEvent) {
  if (!panState || event.pointerId !== panState.pointerId) return
  const dx = event.clientX - panState.lastX
  if (dx === 0) return
  panState.lastX = event.clientX
  const deltaMs = -(dx / panState.width) * (props.viewEnd - props.viewStart)
  emit('pan', deltaMs)
}

function stopPan() {
  if (!panState) return
  panState = null
  panning.value = false
  window.removeEventListener('pointermove', onWindowPointerMove)
  window.removeEventListener('pointerup', onWindowPointerUp)
  window.removeEventListener('pointercancel', onWindowPointerUp)
}

function onWindowPointerUp(event: PointerEvent) {
  if (panState && event.pointerId === panState.pointerId) stopPan()
}

function onScrollWheel(event: WheelEvent) {
  const rect = getTrackRect()
  if (!rect) return
  const sideways = event.shiftKey ? event.deltaY : event.deltaX
  if (Math.abs(sideways) > Math.abs(event.deltaY) || event.shiftKey) {
    event.preventDefault()
    const deltaMs = (sideways / Math.max(1, rect.width)) * (props.viewEnd - props.viewStart)
    emit('pan', deltaMs)
    return
  }
  const overTrack =
    event.ctrlKey ||
    event.metaKey ||
    (event.target instanceof Element && !!event.target.closest('.pulse-track-col'))
  if (!overTrack || event.clientX < rect.left) return
  event.preventDefault()
  const anchorRatio = clamp((event.clientX - rect.left) / Math.max(1, rect.width), 0, 1)
  const factor = Math.exp(event.deltaY * 0.0015)
  emit('zoom', anchorRatio, factor)
}

// --- Floating hover tooltip ---
const tooltip = shallowRef<PulseTooltipData | null>(null)
const tooltipPos = shallowRef({ left: 0, top: 0 })

function positionTooltip(event: MouseEvent | PointerEvent) {
  const maxLeft = typeof window !== 'undefined' ? window.innerWidth - 308 : 900
  const maxTop = typeof window !== 'undefined' ? window.innerHeight - 110 : 600
  tooltipPos.value = {
    left: Math.max(8, Math.min(maxLeft, event.clientX + 12)),
    top: Math.max(8, Math.min(maxTop, event.clientY + 14)),
  }
}

function showTooltip(data: PulseTooltipData, event: MouseEvent | PointerEvent) {
  tooltip.value = data
  positionTooltip(event)
}

function hideTooltip() {
  tooltip.value = null
}

function showSpanTooltip(lane: PulseLane, item: RenderedSpan, event: MouseEvent) {
  const startText = formatClockShort(
    item.span.startedAt,
    props.viewStart,
    props.viewEnd,
    locale.value,
  )
  const endText = item.running
    ? t('pulse.tip.running')
    : formatClockShort(item.endAt, props.viewStart, props.viewEnd, locale.value)
  const durText = formatDurationCompact(item.endAt - item.span.startedAt)
  showTooltip(
    {
      title: `${laneTitle(lane)} · ${t(`pulse.stage.${item.visualKind}`)}`,
      subtitle: item.span.label || undefined,
      meta: t('pulse.tip.duration', { start: startText, end: endText, duration: durText }),
      detail: item.liveCommand
        ? t('pulse.tip.liveCommand', { command: item.liveCommand })
        : undefined,
    },
    event,
  )
}

function showMarkerTooltip(lane: PulseLane, item: RenderedMarker, event: MouseEvent) {
  const kindLabel = t(`pulse.marker.${item.marker.kind}`)
  showTooltip(
    {
      title: laneTitle(lane),
      subtitle: item.marker.label ? `${kindLabel} · ${item.marker.label}` : kindLabel,
      meta: t('pulse.tip.at', {
        time: formatClockShort(item.marker.at, props.viewStart, props.viewEnd, locale.value),
      }),
    },
    event,
  )
}

function showRibbonTooltip(bar: PulseRibbonBar, event: MouseEvent) {
  const time = formatClockShort(bar.midAt, props.viewStart, props.viewEnd, locale.value)
  showTooltip(
    {
      title: t('pulse.ribbon.tipTitle', { active: bar.active, slots: bar.slots }),
      subtitle: bar.full
        ? t('pulse.ribbon.tipFull', { time })
        : t('pulse.ribbon.tipNormal', { time }),
    },
    event,
  )
}

function showHandoffTooltip(arc: HandoffArc, event: MouseEvent) {
  const toneLabel = t(`pulse.handoff.${arc.tone}`)
  showTooltip(
    {
      title: toneLabel,
      subtitle: arc.handoff.label || undefined,
      meta: t('pulse.tip.at', {
        time: formatClockShort(arc.handoff.at, props.viewStart, props.viewEnd, locale.value),
      }),
    },
    event,
  )
}

function onLaneOpenClick(lane: PulseLane) {
  emit('select', lane.conversationId, null)
  emit('openLane', lane)
}

function onScrollPointerLeave() {
  emit('hoveringChange', false)
  hideTooltip()
}

onBeforeUnmount(() => {
  stopPan()
  resizeObserver?.disconnect()
  if (handoffFrame !== null) cancelAnimationFrame(handoffFrame)
})
</script>

<template>
  <div
    class="pulse-canvas"
    :class="{ 'is-panning': panning }"
    :style="{ '--pulse-lane-h': `${laneHeightPx}px`, '--pulse-span-h': `${spanHeightPx}px` }"
  >
    <div
      ref="scrollEl"
      class="pulse-lanes-scroll"
      @wheel="onScrollWheel"
      @pointerenter="emit('hoveringChange', true)"
      @pointerleave="onScrollPointerLeave"
    >
      <div class="pulse-sticky-head">
        <!-- Concurrency slot watermark ribbon -->
        <div v-if="showConcurrency" class="pulse-grid-row pulse-ribbon-row" data-pulse-ribbon>
          <div class="pulse-label-col pulse-ribbon-label">
            <span>{{ t('pulse.ribbon.label') }}</span>
            <span
              class="pulse-badge pulse-mono"
              :class="slotFull ? 'is-warning' : 'is-primary'"
              data-pulse-slot-badge
            >
              {{ slotBadgeText }}
            </span>
          </div>
          <div class="pulse-track-col pulse-ribbon-track">
            <div
              v-for="bar in ribbonBars"
              :key="bar.index"
              class="pulse-ribbon-bar"
              :class="bar.full ? 'is-full' : 'is-normal'"
              :style="{
                left: `${bar.leftPct}%`,
                width: `${bar.widthPct}%`,
                height: `${bar.heightPx}px`,
              }"
              @pointerenter="showRibbonTooltip(bar, $event)"
              @pointermove="positionTooltip($event)"
              @pointerleave="hideTooltip()"
            />
          </div>
        </div>

        <!-- Time axis header -->
        <div class="pulse-grid-row pulse-axis-row">
          <div class="pulse-label-col pulse-axis-label">
            <span>{{ t('pulse.axis.lanes') }}</span>
            <span class="pulse-mono" data-pulse-window-range>{{ windowRangeText }}</span>
          </div>
          <div
            ref="axisTrackEl"
            class="pulse-track-col pulse-axis-track"
            @pointerdown="onTrackPointerDown"
            @dblclick="emit('resetWindow')"
          >
            <div
              v-for="tick in axisTicks"
              :key="tick.at"
              class="pulse-axis-tick pulse-mono"
              :style="{ left: `${tick.pct}%` }"
            >
              {{ formatClockShort(tick.at, viewStart, viewEnd, locale) }}
            </div>
            <div
              v-if="returnGeometry?.showBadge"
              class="pulse-axis-return-badge pulse-mono"
              data-pulse-return-badge
              :style="{ left: `${clamp(returnGeometry.retPct, 6, 92)}%` }"
            >
              {{ t('pulse.axis.returnBadge', { time: returnGeometry.timeText }) }}
            </div>
            <div
              v-if="showNowLine"
              class="pulse-axis-now-badge pulse-mono"
              data-pulse-now-badge
              :class="{ 'is-right-aligned': nowPct > 91 }"
              :style="{ left: `${nowPct}%` }"
            >
              {{ t('pulse.axis.nowBadge', { time: nowTimeText }) }}
            </div>
          </div>
        </div>
      </div>

      <div ref="lanesInnerEl" class="pulse-lanes-inner">
        <!-- Vertical grid, return band & live now line -->
        <div class="pulse-grid-overlay" aria-hidden="true">
          <div
            v-for="tick in axisTicks"
            :key="tick.at"
            class="pulse-grid-line"
            :style="{ left: `${tick.pct}%` }"
          />
          <div
            v-if="returnGeometry"
            class="pulse-return-band"
            :class="{ 'has-border': returnGeometry.showLine }"
            data-pulse-return-band
            :style="{
              left: `${returnGeometry.bandLeft}%`,
              width: `${returnGeometry.bandWidth}%`,
            }"
          />
          <div
            v-if="showNowLine"
            class="pulse-now-line"
            data-pulse-now-line
            :style="{ left: `${nowPct}%` }"
          />
        </div>

        <!-- Author <-> reviewer handoff Bezier arcs -->
        <svg
          v-if="showHandoffs && handoffArcs.length"
          class="pulse-handoff-svg"
          data-pulse-handoff-svg
          :viewBox="`0 0 ${handoffSvgSize.width} ${handoffSvgSize.height}`"
          :style="{ height: `${handoffSvgSize.height}px` }"
        >
          <g v-for="arc in handoffArcs" :key="arc.key" data-pulse-handoff-arc>
            <path
              class="pulse-handoff-hit"
              :d="arc.d"
              @pointerenter="showHandoffTooltip(arc, $event)"
              @pointermove="positionTooltip($event)"
              @pointerleave="hideTooltip()"
            />
            <path class="pulse-handoff-path" :class="`is-${arc.tone}`" :d="arc.d" />
            <circle
              class="pulse-handoff-dot"
              :class="`is-${arc.tone}`"
              :cx="arc.x"
              :cy="arc.y2"
              r="3.5"
              @pointerenter="showHandoffTooltip(arc, $event)"
              @pointermove="positionTooltip($event)"
              @pointerleave="hideTooltip()"
            />
          </g>
        </svg>

        <!-- Coordinator pinned lane -->
        <div
          v-if="partitioned.coordinator"
          class="pulse-grid-row pulse-lane-row"
          :class="laneRowClasses({ lane: partitioned.coordinator, nested: false })"
          :data-lane-id="partitioned.coordinator.conversationId"
        >
          <div
            class="pulse-label-col pulse-lane-label"
            @click="emit('select', partitioned.coordinator.conversationId, null)"
          >
            <div class="pulse-lane-head">
              <span
                class="pulse-dot"
                :class="laneDotClass(partitioned.coordinator)"
                aria-hidden="true"
              />
              <button
                type="button"
                class="pulse-lane-title"
                :title="t('pulse.openCoordinator')"
                @click.stop="onLaneOpenClick(partitioned.coordinator)"
              >
                {{ laneTitle(partitioned.coordinator) }}
              </button>
              <span
                v-if="laneAlertBadge(partitioned.coordinator)"
                class="pulse-badge pulse-mono"
                :class="`is-${laneAlertBadge(partitioned.coordinator)!.tone}`"
              >
                {{ laneAlertBadge(partitioned.coordinator)!.text }}
              </span>
            </div>
            <div v-if="!isCompact" class="pulse-lane-meta">
              <span v-if="laneMeta(partitioned.coordinator).fallback">
                {{ laneMeta(partitioned.coordinator).fallback }}
              </span>
            </div>
          </div>
          <div
            class="pulse-track-col pulse-lane-track"
            @pointerdown="onTrackPointerDown"
            @dblclick="emit('resetWindow')"
          >
            <div
              v-for="item in spansForLane(partitioned.coordinator)"
              :key="item.span.id"
              class="pulse-span"
              :class="[
                `is-${item.visualKind}`,
                {
                  'pulse-span-running': item.running,
                  'is-selected': selectedSpanId === item.span.id,
                },
              ]"
              :data-span-id="item.span.id"
              :style="{ left: `${item.leftPct}%`, width: `${item.widthPct}%` }"
              @click.stop="emit('select', partitioned.coordinator.conversationId, item.span.id)"
              @pointerenter="showSpanTooltip(partitioned.coordinator, item, $event)"
              @pointermove="positionTooltip($event)"
              @pointerleave="hideTooltip()"
            >
              <span v-if="item.showText" class="pulse-span-text">{{ item.inlineText }}</span>
            </div>
            <div
              v-for="item in markersForLane(partitioned.coordinator)"
              :key="`${item.marker.seq}-${item.marker.kind}`"
              class="pulse-marker"
              :class="`is-${item.tone}`"
              :data-marker-kind="item.marker.kind"
              :style="{ left: `${item.pct}%` }"
              @click.stop="emit('select', partitioned.coordinator.conversationId, null)"
              @pointerenter="showMarkerTooltip(partitioned.coordinator, item, $event)"
              @pointermove="positionTooltip($event)"
              @pointerleave="hideTooltip()"
            />
          </div>
        </div>

        <!-- Grouped active lanes -->
        <template v-for="group in partitioned.groups" :key="group.key">
          <div class="pulse-grid-row pulse-group-row" :data-pulse-group="group.key">
            <div class="pulse-label-col pulse-group-label">
              <span
                v-if="group.kind === 'needs' || group.kind === 'active'"
                class="pulse-dot"
                :class="group.kind === 'needs' ? 'is-warning' : 'is-primary'"
                aria-hidden="true"
              />
              <span class="pulse-group-title">{{ groupHeaderTitle(group) }}</span>
              <span
                class="pulse-badge pulse-mono"
                :class="
                  group.kind === 'needs'
                    ? 'is-warning'
                    : group.kind === 'active'
                      ? 'is-primary'
                      : 'is-neutral'
                "
              >
                {{ group.rows.length }}
              </span>
            </div>
            <div class="pulse-track-col pulse-group-track" aria-hidden="true" />
          </div>

          <div
            v-for="row in group.rows"
            :key="row.lane.conversationId"
            class="pulse-grid-row pulse-lane-row"
            :class="laneRowClasses(row)"
            :data-lane-id="row.lane.conversationId"
            :data-lane-needs="isNeedsLane(row.lane) ? 'true' : undefined"
          >
            <div
              class="pulse-label-col pulse-lane-label"
              @click="emit('select', row.lane.conversationId, null)"
            >
              <div class="pulse-lane-head">
                <span class="pulse-dot" :class="laneDotClass(row.lane)" aria-hidden="true" />
                <button
                  type="button"
                  class="pulse-lane-title"
                  :title="t('pulse.openThread', { title: laneTitle(row.lane) })"
                  @click.stop="onLaneOpenClick(row.lane)"
                >
                  <span v-if="row.nested" class="pulse-nested-prefix" aria-hidden="true">↳</span>
                  {{ laneTitle(row.lane) }}
                </button>
                <span
                  v-if="laneAlertBadge(row.lane)"
                  class="pulse-badge pulse-mono"
                  :class="`is-${laneAlertBadge(row.lane)!.tone}`"
                >
                  {{ laneAlertBadge(row.lane)!.text }}
                </span>
              </div>
              <div v-if="!isCompact" class="pulse-lane-meta">
                <template v-for="meta in [laneMeta(row.lane)]" :key="row.lane.conversationId">
                  <span v-if="meta.pr" class="pulse-mono">{{ meta.pr }}</span>
                  <span v-if="meta.diff" class="pulse-mono pulse-diff">
                    <span class="pulse-diff-add">+{{ meta.diff.added }}</span>
                    <span class="pulse-diff-del">-{{ meta.diff.removed }}</span>
                  </span>
                  <span v-if="meta.progress" class="pulse-mono">{{ meta.progress }}</span>
                  <span v-if="meta.fallback" class="pulse-meta-fallback">{{ meta.fallback }}</span>
                </template>
              </div>
            </div>
            <div
              class="pulse-track-col pulse-lane-track"
              @pointerdown="onTrackPointerDown"
              @dblclick="emit('resetWindow')"
            >
              <div
                v-for="item in spansForLane(row.lane)"
                :key="item.span.id"
                class="pulse-span"
                :class="[
                  `is-${item.visualKind}`,
                  {
                    'pulse-span-running': item.running,
                    'is-selected': selectedSpanId === item.span.id,
                  },
                ]"
                :data-span-id="item.span.id"
                :style="{ left: `${item.leftPct}%`, width: `${item.widthPct}%` }"
                @click.stop="emit('select', row.lane.conversationId, item.span.id)"
                @pointerenter="showSpanTooltip(row.lane, item, $event)"
                @pointermove="positionTooltip($event)"
                @pointerleave="hideTooltip()"
              >
                <span v-if="item.showText" class="pulse-span-text">{{ item.inlineText }}</span>
              </div>
              <div
                v-for="item in markersForLane(row.lane)"
                :key="`${item.marker.seq}-${item.marker.kind}`"
                class="pulse-marker"
                :class="`is-${item.tone}`"
                :data-marker-kind="item.marker.kind"
                :style="{ left: `${item.pct}%` }"
                @click.stop="emit('select', row.lane.conversationId, null)"
                @pointerenter="showMarkerTooltip(row.lane, item, $event)"
                @pointermove="positionTooltip($event)"
                @pointerleave="hideTooltip()"
              />
            </div>
          </div>
        </template>

        <slot v-if="!partitioned.groups.length" name="empty" />

        <!-- Completed collapsible section -->
        <template v-if="partitioned.completed.length">
          <button
            type="button"
            class="pulse-completed-toggle"
            data-pulse-completed-toggle
            :aria-expanded="showCompleted"
            @click="emit('update:showCompleted', !showCompleted)"
          >
            <span class="pulse-completed-left">
              <ChevronDownIcon
                v-if="showCompleted"
                class="pulse-completed-icon"
                aria-hidden="true"
              />
              <ChevronRightIcon v-else class="pulse-completed-icon" aria-hidden="true" />
              <span>{{
                t('pulse.completed.summary', { count: partitioned.completed.length })
              }}</span>
              <span v-if="completedPreviewTitles" class="pulse-completed-names">
                （{{ completedPreviewTitles }}）
              </span>
            </span>
          </button>

          <template v-if="showCompleted">
            <div
              v-for="row in partitioned.completed"
              :key="row.lane.conversationId"
              class="pulse-grid-row pulse-lane-row is-completed"
              :class="laneRowClasses(row)"
              :data-lane-id="row.lane.conversationId"
              data-lane-completed="true"
            >
              <div
                class="pulse-label-col pulse-lane-label"
                @click="emit('select', row.lane.conversationId, null)"
              >
                <div class="pulse-lane-head">
                  <span class="pulse-dot" :class="laneDotClass(row.lane)" aria-hidden="true" />
                  <button
                    type="button"
                    class="pulse-lane-title"
                    :title="t('pulse.openThread', { title: laneTitle(row.lane) })"
                    @click.stop="onLaneOpenClick(row.lane)"
                  >
                    <span v-if="row.nested" class="pulse-nested-prefix" aria-hidden="true">↳</span>
                    {{ laneTitle(row.lane) }}
                  </button>
                </div>
                <div v-if="!isCompact" class="pulse-lane-meta">
                  <template v-for="meta in [laneMeta(row.lane)]" :key="row.lane.conversationId">
                    <span v-if="meta.pr" class="pulse-mono">{{ meta.pr }}</span>
                    <span v-if="meta.diff" class="pulse-mono pulse-diff">
                      <span class="pulse-diff-add">+{{ meta.diff.added }}</span>
                      <span class="pulse-diff-del">-{{ meta.diff.removed }}</span>
                    </span>
                    <span v-if="meta.fallback" class="pulse-meta-fallback">{{
                      meta.fallback
                    }}</span>
                  </template>
                </div>
              </div>
              <div
                class="pulse-track-col pulse-lane-track"
                @pointerdown="onTrackPointerDown"
                @dblclick="emit('resetWindow')"
              >
                <div
                  v-for="item in spansForLane(row.lane)"
                  :key="item.span.id"
                  class="pulse-span"
                  :class="[
                    `is-${item.visualKind}`,
                    {
                      'pulse-span-running': item.running,
                      'is-selected': selectedSpanId === item.span.id,
                    },
                  ]"
                  :data-span-id="item.span.id"
                  :style="{ left: `${item.leftPct}%`, width: `${item.widthPct}%` }"
                  @click.stop="emit('select', row.lane.conversationId, item.span.id)"
                  @pointerenter="showSpanTooltip(row.lane, item, $event)"
                  @pointermove="positionTooltip($event)"
                  @pointerleave="hideTooltip()"
                >
                  <span v-if="item.showText" class="pulse-span-text">{{ item.inlineText }}</span>
                </div>
                <div
                  v-for="item in markersForLane(row.lane)"
                  :key="`${item.marker.seq}-${item.marker.kind}`"
                  class="pulse-marker"
                  :class="`is-${item.tone}`"
                  :data-marker-kind="item.marker.kind"
                  :style="{ left: `${item.pct}%` }"
                  @click.stop="emit('select', row.lane.conversationId, null)"
                  @pointerenter="showMarkerTooltip(row.lane, item, $event)"
                  @pointermove="positionTooltip($event)"
                  @pointerleave="hideTooltip()"
                />
              </div>
            </div>
          </template>
        </template>
      </div>
    </div>

    <!-- Floating tooltip -->
    <div
      v-if="tooltip"
      class="pulse-tooltip"
      role="tooltip"
      :style="{ left: `${tooltipPos.left}px`, top: `${tooltipPos.top}px` }"
    >
      <div class="pulse-tip-head">{{ tooltip.title }}</div>
      <div v-if="tooltip.subtitle" class="pulse-tip-sub">{{ tooltip.subtitle }}</div>
      <div v-if="tooltip.detail" class="pulse-tip-detail pulse-mono">{{ tooltip.detail }}</div>
      <div v-if="tooltip.meta" class="pulse-tip-meta pulse-mono">{{ tooltip.meta }}</div>
    </div>
  </div>
</template>

<style scoped>
.pulse-canvas,
.pulse-canvas * {
  box-sizing: border-box;
}

.pulse-canvas {
  --pulse-ci-soft: color-mix(in srgb, var(--dou-color-chart-4) 11%, var(--dou-color-bg-surface));
  --pulse-review-soft: color-mix(
    in srgb,
    var(--dou-color-chart-6) 11%,
    var(--dou-color-bg-surface)
  );
  --pulse-review-fill: var(--dou-color-chart-6);
  --pulse-stripe: rgba(255, 255, 255, 0.34);
  --pulse-return-tint: color-mix(in srgb, var(--dou-color-primary) 5%, transparent);
  --pulse-label-w: 258px;
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  background: var(--dou-color-bg-canvas);
}

:global(html[data-dou-theme='dark'] .pulse-canvas) {
  --pulse-ci-soft: color-mix(in srgb, var(--dou-color-chart-4) 18%, var(--dou-color-bg-surface));
  --pulse-review-soft: color-mix(
    in srgb,
    var(--dou-color-chart-6) 20%,
    var(--dou-color-bg-surface)
  );
  --pulse-review-fill: color-mix(
    in srgb,
    var(--dou-color-chart-6) 76%,
    var(--dou-color-bg-surface)
  );
  --pulse-stripe: rgba(255, 255, 255, 0.16);
  --pulse-return-tint: color-mix(in srgb, var(--dou-color-primary) 9%, transparent);
}

.pulse-canvas.is-panning {
  user-select: none;
  cursor: grabbing;
}

.pulse-lanes-scroll {
  position: relative;
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overflow-x: hidden;
}

.pulse-sticky-head {
  position: sticky;
  top: 0;
  z-index: 20;
  background: var(--dou-color-bg-surface);
}

.pulse-grid-row {
  display: grid;
  grid-template-columns: var(--pulse-label-w) minmax(0, 1fr);
}

.pulse-label-col {
  min-width: 0;
  border-right: 1px solid var(--dou-color-border);
  background: var(--dou-color-bg-surface);
}

.pulse-track-col {
  position: relative;
  min-width: 0;
  overflow: hidden;
}

.pulse-mono {
  font-family: var(--dou-font-family-mono);
  font-variant-numeric: tabular-nums;
}

.pulse-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  height: 18px;
  padding: 0 6px;
  border-radius: 9999px;
  font-size: 11px;
  font-weight: 500;
  line-height: 1;
}

.pulse-badge.is-primary {
  background: var(--dou-color-primary-soft);
  color: var(--dou-color-primary-text);
}

.pulse-badge.is-warning {
  background: var(--dou-color-warning-soft);
  color: var(--dou-color-warning-text);
}

.pulse-badge.is-danger {
  background: var(--dou-color-danger-soft);
  color: var(--dou-color-danger-text);
}

.pulse-badge.is-neutral {
  background: var(--dou-color-bg-muted);
  color: var(--dou-color-text-secondary);
}

/* Concurrency Ribbon */
.pulse-ribbon-row {
  height: 28px;
  border-bottom: 1px solid var(--dou-color-border-subtle);
  background: var(--dou-color-bg-surface);
}

.pulse-ribbon-label {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 0 12px;
  font-size: 11.5px;
  color: var(--dou-color-text-secondary);
}

.pulse-ribbon-track {
  height: 100%;
  background: var(--dou-color-bg-canvas);
}

.pulse-ribbon-bar {
  position: absolute;
  bottom: 3px;
  border-radius: 2px 2px 0 0;
  transition: height 0.2s ease;
}

.pulse-ribbon-bar.is-normal {
  background: color-mix(in srgb, var(--dou-color-primary) 26%, transparent);
  border-top: 1.5px solid color-mix(in srgb, var(--dou-color-primary) 68%, transparent);
}

.pulse-ribbon-bar.is-full {
  background: color-mix(in srgb, var(--dou-color-warning) 30%, transparent);
  border-top: 1.5px solid var(--dou-color-warning);
}

/* Axis Header */
.pulse-axis-row {
  height: 30px;
  border-bottom: 1px solid var(--dou-color-border);
  background: var(--dou-color-bg-surface);
}

.pulse-axis-label {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 0 12px;
  font-size: 11.5px;
  font-weight: 500;
  color: var(--dou-color-text-tertiary);
}

.pulse-axis-track {
  height: 100%;
  user-select: none;
  cursor: grab;
}

.pulse-axis-track:active {
  cursor: grabbing;
}

.pulse-axis-tick {
  position: absolute;
  top: 0;
  bottom: 0;
  display: flex;
  align-items: center;
  padding-left: 5px;
  border-left: 1px solid var(--dou-color-border-subtle);
  font-size: 11px;
  color: var(--dou-color-text-tertiary);
  pointer-events: none;
}

.pulse-axis-return-badge {
  position: absolute;
  top: 50%;
  z-index: 3;
  transform: translate(-50%, -50%);
  padding: 1px 6px;
  border-radius: 9999px;
  border: 1px solid color-mix(in srgb, var(--dou-color-primary) 28%, transparent);
  background: color-mix(in srgb, var(--dou-color-primary) 12%, var(--dou-color-bg-surface));
  color: var(--dou-color-primary-text);
  font-size: 10.5px;
  font-weight: 500;
  white-space: nowrap;
  pointer-events: none;
}

.pulse-axis-now-badge {
  position: absolute;
  top: 50%;
  z-index: 4;
  transform: translate(-50%, -50%);
  padding: 1px 6px;
  border-radius: 9999px;
  background: var(--dou-color-primary);
  color: var(--dou-color-primary-on);
  font-size: 10.5px;
  font-weight: 600;
  white-space: nowrap;
  pointer-events: none;
}

.pulse-axis-now-badge.is-right-aligned {
  transform: translate(-90%, -50%);
}

/* Swimlanes area & overlays */
.pulse-lanes-inner {
  position: relative;
  min-height: calc(100% - 58px);
}

.pulse-grid-overlay {
  position: absolute;
  top: 0;
  bottom: 0;
  left: var(--pulse-label-w);
  right: 0;
  z-index: 1;
  overflow: hidden;
  pointer-events: none;
}

.pulse-grid-line {
  position: absolute;
  top: 0;
  bottom: 0;
  width: 1px;
  background: var(--dou-color-border-subtle);
}

.pulse-return-band {
  position: absolute;
  top: 0;
  bottom: 0;
  background: var(--pulse-return-tint);
}

.pulse-return-band.has-border {
  border-left: 1.5px dashed color-mix(in srgb, var(--dou-color-primary) 50%, transparent);
}

.pulse-now-line {
  position: absolute;
  top: 0;
  bottom: 0;
  z-index: 5;
  width: 1.5px;
  background: var(--dou-color-primary);
}

/* Handoff SVG */
.pulse-handoff-svg {
  position: absolute;
  top: 0;
  left: var(--pulse-label-w);
  width: calc(100% - var(--pulse-label-w));
  z-index: 4;
  pointer-events: none;
  overflow: visible;
}

.pulse-handoff-hit {
  fill: none;
  stroke: transparent;
  stroke-width: 10;
  pointer-events: stroke;
  cursor: pointer;
}

.pulse-handoff-path {
  fill: none;
  stroke-width: 1.5;
  stroke-dasharray: 3 2.5;
  opacity: 0.85;
  pointer-events: none;
}

.pulse-handoff-path.is-request {
  stroke: var(--dou-color-chart-6);
}

.pulse-handoff-path.is-changes {
  stroke: var(--dou-color-warning);
}

.pulse-handoff-path.is-pass {
  stroke: var(--dou-color-success);
}

.pulse-handoff-dot {
  pointer-events: all;
  cursor: pointer;
}

.pulse-handoff-dot.is-request {
  fill: var(--dou-color-chart-6);
}

.pulse-handoff-dot.is-changes {
  fill: var(--dou-color-warning);
}

.pulse-handoff-dot.is-pass {
  fill: var(--dou-color-success);
}

/* Group header rows */
.pulse-group-row {
  position: relative;
  z-index: 2;
  height: 28px;
  border-bottom: 1px solid var(--dou-color-border-subtle);
  background: var(--dou-color-bg-canvas);
}

.pulse-group-label {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 0 12px;
  background: var(--dou-color-bg-canvas);
  font-size: 11.5px;
  font-weight: 600;
  color: var(--dou-color-text-secondary);
  user-select: none;
}

.pulse-group-title {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pulse-group-track {
  min-width: 0;
}

/* Lane rows */
.pulse-lane-row {
  position: relative;
  z-index: 2;
  height: var(--pulse-lane-h, 44px);
  border-bottom: 1px solid var(--dou-color-border-subtle);
  transition: background-color 0.12s ease;
}

.pulse-lane-row:hover .pulse-label-col,
.pulse-lane-row:hover .pulse-track-col {
  background: color-mix(in srgb, var(--dou-color-bg-muted) 42%, transparent);
}

.pulse-lane-row.is-selected .pulse-label-col,
.pulse-lane-row.is-selected .pulse-track-col {
  background: var(--dou-color-primary-soft);
}

.pulse-lane-row.is-coordinator .pulse-label-col {
  background: color-mix(in srgb, var(--dou-color-bg-muted) 35%, var(--dou-color-bg-surface));
}

.pulse-lane-row.is-needs-me .pulse-label-col {
  box-shadow: inset 3px 0 0 var(--dou-color-warning);
}

.pulse-lane-row.is-blocked .pulse-label-col {
  box-shadow: inset 3px 0 0 var(--dou-color-danger);
}

.pulse-lane-label {
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 2px;
  height: 100%;
  padding: 0 12px;
  cursor: pointer;
}

.pulse-lane-row.is-nested .pulse-lane-label {
  padding-left: 26px;
}

.pulse-lane-head {
  display: flex;
  align-items: center;
  gap: 7px;
  min-width: 0;
}

.pulse-lane-title {
  flex: 1;
  min-width: 0;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--dou-color-text);
  font: inherit;
  font-size: 12.5px;
  font-weight: 500;
  text-align: left;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
}

.pulse-lane-title:hover {
  color: var(--dou-color-primary-text);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.pulse-lane-title:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: 1px;
  border-radius: 3px;
}

.pulse-lane-row.is-nested .pulse-lane-title {
  font-size: 12px;
  font-weight: 400;
  color: var(--dou-color-text-secondary);
}

.pulse-nested-prefix {
  margin-right: 3px;
  color: var(--dou-color-text-tertiary);
}

.pulse-lane-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  padding-left: 14px;
  font-size: 11px;
  color: var(--dou-color-text-tertiary);
  overflow: hidden;
  white-space: nowrap;
}

.pulse-meta-fallback {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}

.pulse-diff {
  display: inline-flex;
  gap: 4px;
}

.pulse-diff-add {
  color: var(--dou-color-success-text);
}

.pulse-diff-del {
  color: var(--dou-color-danger-text);
}

.pulse-lane-track {
  height: 100%;
  user-select: none;
  cursor: grab;
}

.pulse-lane-track:active {
  cursor: grabbing;
}

/* Status dots */
.pulse-dot {
  position: relative;
  display: inline-block;
  flex: none;
  width: 7px;
  height: 7px;
  border-radius: 9999px;
}

.pulse-dot.is-primary {
  background: var(--dou-color-primary);
  color: var(--dou-color-primary);
}

.pulse-dot.is-warning {
  background: var(--dou-color-warning);
  color: var(--dou-color-warning);
}

.pulse-dot.is-danger {
  background: var(--dou-color-danger);
  color: var(--dou-color-danger);
}

.pulse-dot.is-success {
  background: var(--dou-color-success);
  color: var(--dou-color-success);
}

.pulse-dot.is-ci {
  background: var(--dou-color-chart-4);
  color: var(--dou-color-chart-4);
}

.pulse-dot.is-review {
  background: var(--dou-color-chart-6);
  color: var(--dou-color-chart-6);
}

.pulse-dot.is-neutral {
  background: var(--dou-color-text-tertiary);
  color: var(--dou-color-text-tertiary);
}

.pulse-dot.is-pulse::after {
  content: '';
  position: absolute;
  inset: -3px;
  border-radius: 9999px;
  border: 1.5px solid currentColor;
  opacity: 0;
  animation: pulse-ripple 1.8s cubic-bezier(0.2, 0, 0, 1) infinite;
}

@keyframes pulse-ripple {
  0% {
    transform: scale(0.6);
    opacity: 0.65;
  }
  100% {
    transform: scale(1.65);
    opacity: 0;
  }
}

/* Spans */
.pulse-span {
  position: absolute;
  top: 50%;
  z-index: 2;
  display: flex;
  align-items: center;
  height: var(--pulse-span-h, 19px);
  padding: 0 6px;
  border-radius: 4px;
  border: 1px solid transparent;
  transform: translateY(-50%);
  overflow: hidden;
  font-size: 11px;
  font-weight: 500;
  white-space: nowrap;
  cursor: pointer;
  transition: box-shadow 0.12s ease;
}

.pulse-span:hover {
  z-index: 6;
  box-shadow:
    0 0 0 1.5px var(--dou-color-bg-surface),
    0 0 0 3px var(--dou-color-primary);
}

.pulse-span.is-selected {
  z-index: 7;
  box-shadow:
    0 0 0 1.5px var(--dou-color-bg-surface),
    0 0 0 3px var(--dou-color-primary);
}

.pulse-span.is-working {
  background: var(--dou-color-primary);
  color: var(--dou-color-primary-on);
}

.pulse-span.is-reviewing {
  background: var(--pulse-review-fill);
  color: #fff;
}

.pulse-span.is-waiting_review {
  background: var(--pulse-review-soft);
  color: var(--dou-color-chart-6);
  border: 1px dashed color-mix(in srgb, var(--dou-color-chart-6) 55%, transparent);
}

.pulse-span.is-waiting_user {
  background: var(--dou-color-warning-soft);
  color: var(--dou-color-warning-text);
  border-color: color-mix(in srgb, var(--dou-color-warning) 55%, transparent);
}

.pulse-span.is-waiting_ci {
  background: var(--pulse-ci-soft);
  color: var(--dou-color-chart-4);
  border: 1px dashed color-mix(in srgb, var(--dou-color-chart-4) 70%, transparent);
}

.pulse-span.is-blocked {
  background: var(--dou-color-danger-soft);
  color: var(--dou-color-danger-text);
  border-color: color-mix(in srgb, var(--dou-color-danger) 55%, transparent);
}

.pulse-span.is-queued {
  background: var(--dou-color-bg-muted);
  color: var(--dou-color-text-tertiary);
  border: 1px dashed var(--dou-color-border-strong);
}

.pulse-span.is-waiting {
  background: var(--dou-color-bg-muted);
  color: var(--dou-color-text-secondary);
  border: 1px dotted var(--dou-color-border-strong);
}

.pulse-span.is-aside {
  background: var(--dou-color-neutral-soft);
  color: var(--dou-color-text-tertiary);
  border: 1px dotted var(--dou-color-border);
}

.pulse-span.is-coord {
  background: color-mix(in srgb, var(--dou-color-neutral) 12%, transparent);
  color: var(--dou-color-text-secondary);
  border-color: var(--dou-color-border-strong);
}

.pulse-span-running::after {
  content: '';
  position: absolute;
  inset: 0;
  border-radius: inherit;
  pointer-events: none;
  background: repeating-linear-gradient(-45deg, transparent 0 5px, var(--pulse-stripe) 5px 10px);
  background-size: 28px 28px;
  animation: pulse-stripe-move 1.4s linear infinite;
}

@keyframes pulse-stripe-move {
  from {
    background-position: 0 0;
  }
  to {
    background-position: 28px 0;
  }
}

.pulse-span-text {
  position: relative;
  z-index: 1;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* Milestone markers */
.pulse-marker {
  position: absolute;
  top: 50%;
  z-index: 5;
  width: 9px;
  height: 9px;
  border-radius: 2px;
  border: 1.5px solid var(--dou-color-bg-surface);
  transform: translate(-50%, -50%) rotate(45deg);
  cursor: pointer;
  transition: transform 0.12s ease;
}

.pulse-marker:hover {
  z-index: 8;
  transform: translate(-50%, -50%) rotate(45deg) scale(1.35);
}

.pulse-marker.is-success {
  background: var(--dou-color-success);
}

.pulse-marker.is-danger {
  background: var(--dou-color-danger);
}

.pulse-marker.is-warning {
  background: var(--dou-color-warning);
}

.pulse-marker.is-primary {
  background: var(--dou-color-primary);
}

.pulse-marker.is-ci {
  background: var(--dou-color-chart-4);
}

.pulse-marker.is-review {
  background: var(--dou-color-chart-6);
}

/* Completed toggle bar */
.pulse-completed-toggle {
  position: relative;
  z-index: 3;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  width: 100%;
  padding: 9px 14px;
  border: 0;
  border-bottom: 1px solid var(--dou-color-border-subtle);
  background: var(--dou-color-bg-surface);
  color: var(--dou-color-text-secondary);
  font: inherit;
  font-size: 12px;
  text-align: left;
  cursor: pointer;
  transition:
    background-color 0.12s ease,
    color 0.12s ease;
}

.pulse-completed-toggle:hover {
  background: var(--dou-color-bg-muted);
  color: var(--dou-color-text);
}

.pulse-completed-toggle:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: -2px;
}

.pulse-completed-left {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
}

.pulse-completed-icon {
  flex: none;
  width: 14px;
  height: 14px;
}

.pulse-completed-names {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--dou-color-text-tertiary);
}

/* Floating tooltip */
.pulse-tooltip {
  position: fixed;
  z-index: 60;
  max-width: 300px;
  padding: 8px 10px;
  border: 1px solid var(--dou-color-border);
  border-radius: 8px;
  background: var(--dou-color-bg-surface);
  box-shadow: var(--dou-shadow-md);
  pointer-events: none;
}

.pulse-tip-head {
  font-size: 12px;
  font-weight: 600;
  color: var(--dou-color-text);
  margin-bottom: 2px;
}

.pulse-tip-sub {
  font-size: 11.5px;
  color: var(--dou-color-text-secondary);
  word-break: break-word;
}

.pulse-tip-detail {
  margin-top: 4px;
  font-size: 11px;
  color: var(--dou-color-primary-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pulse-tip-meta {
  margin-top: 4px;
  font-size: 11px;
  color: var(--dou-color-text-tertiary);
}

@media (prefers-reduced-motion: reduce) {
  .pulse-span-running::after,
  .pulse-dot.is-pulse::after {
    animation: none;
  }
}
</style>
