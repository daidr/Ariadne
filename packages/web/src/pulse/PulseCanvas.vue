<script setup lang="ts">
import type {
  ChangeTotals as ChangeTotalsData,
  LiveCommand,
  PulseConcurrency,
  PulseHandoff,
  PulseLane,
  PulseMarker,
  PulseSpan,
  ThreadView,
} from '@ariadne/core'
import { ChevronDownIcon, ChevronRightIcon, CornerDownRightIcon, FolderIcon } from '@dou-ui/icons'
import { DouButton, DouStatusDot, DouTag, DouTooltip } from 'dou-ui'
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
import ChangeTotals from '../components/ChangeTotals.vue'
import { locale, t } from '../i18n'
import { branchChanges } from '../review/merge-offered'
import {
  buildAxisTicks,
  buildConcurrencyBars,
  clamp,
  classifyHandoffTone,
  classifyMarkerTone,
  computeAxisHeaderLayout,
  countLiveSlotOccupancy,
  DEFAULT_LABEL_COL_WIDTH,
  formatClockShort,
  formatDurationCompact,
  isNeedsLane,
  liveCommandLabel,
  MAX_LABEL_COL_WIDTH,
  MIN_LABEL_COL_WIDTH,
  percentOf,
  resolveLaneStatusDot,
  resolveSpanToneGroup,
  resolveSpanVisualKind,
  spansMultipleDays,
  type PulseDensity,
  type PulseHandoffTone,
  type PulseLaneLines,
  type PulseLaneRow,
  type PulsePartitionedLanes,
  type PulseRibbonBar,
  type PulseScope,
  type PulseSpanToneGroup,
  type PulseSpanVisualKind,
} from './model'

const FALLBACK_TRACK_WIDTH = 800

const props = withDefaults(
  defineProps<{
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
    laneLines?: PulseLaneLines
    showConcurrency: boolean
    showHandoffs: boolean
    showReturnLine: boolean
    showCompleted: boolean
    selectedLaneId: string | null
    selectedSpanId: number | null
    scope?: PulseScope
    threads: ReadonlyMap<string, ThreadView>
    liveCommands: Readonly<Record<string, LiveCommand>>
  }>(),
  {
    laneLines: 'single',
  },
)

const labelWidth = defineModel<number>('labelWidth', { default: DEFAULT_LABEL_COL_WIDTH })

const emit = defineEmits<{
  'update:showCompleted': [value: boolean]
  toggleConcurrency: []
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
const showLaneMeta = computed(() => !isCompact.value && props.laneLines === 'double')
const laneHeightPx = computed(() => {
  if (isCompact.value) return 32
  return showLaneMeta.value ? 46 : 38
})
const spanHeightPx = computed(() => (isCompact.value ? 16 : 20))

const trackWidthPx = ref(FALLBACK_TRACK_WIDTH)

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

const nowPct = computed(() => percentOf(props.nowMs, props.viewStart, props.viewEnd))
const showNowLine = computed(() => nowPct.value >= 0 && nowPct.value <= 100)

const returnGeometry = computed(() => {
  if (!props.showReturnLine || props.lastReturnedAt === null) return null
  const retPct = percentOf(props.lastReturnedAt, props.viewStart, props.viewEnd)
  const endPct = Math.min(100, Math.max(retPct, nowPct.value))
  if (endPct <= 0 || retPct >= 100) return null
  const showLine = retPct >= 0 && retPct <= 100
  const bandLeft = Math.max(0, retPct)
  const bandWidth = Math.max(0, endPct - bandLeft)
  return {
    at: props.lastReturnedAt,
    retPct,
    showLine,
    bandLeft,
    bandWidth,
    timeText: formatClockShort(props.lastReturnedAt, props.viewStart, props.viewEnd, locale.value),
  }
})

const axisHeaderLayout = computed(() =>
  computeAxisHeaderLayout({
    ticks: axisTicks.value,
    trackWidthPx: trackWidthPx.value,
    nowPct: showNowLine.value ? nowPct.value : null,
    returnPct: returnGeometry.value?.showLine ? returnGeometry.value.retPct : null,
    spansDays: spansMultipleDays(props.viewStart, props.viewEnd),
  }),
)

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
  toneGroup: PulseSpanToneGroup
  leftPct: number
  widthPct: number
  running: boolean
  showText: boolean
  inlineText: string
  liveCommand: string | null
  endAt: number
}

function liveCommandText(lane: PulseLane): string | null {
  return liveCommandLabel(lane, props.liveCommands, 64)
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
    const toneGroup = resolveSpanToneGroup(visualKind)
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
      toneGroup,
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

function laneDot(lane: PulseLane) {
  return resolveLaneStatusDot(lane)
}

interface LaneMetaSummary {
  project: string | null
  pr: string | null
  diff: ChangeTotalsData | null
  progress: string | null
  fallback: string | null
}

function laneMeta(lane: PulseLane): LaneMetaSummary {
  const liveCmd = liveCommandText(lane)
  const project =
    props.scope === 'all' && lane.completed && lane.projectName ? lane.projectName : null
  if (lane.role === 'coordinator') {
    return {
      project,
      pr: null,
      diff: null,
      progress: null,
      fallback: liveCmd ? `$ ${liveCmd}` : (lane.lastHeadline ?? t('pulse.stage.coord')),
    }
  }
  const thread = props.threads.get(lane.conversationId)
  const pr = thread?.pullRequest?.number ? `#${thread.pullRequest.number}` : null
  const diff = thread ? branchChanges(thread) : null
  const progress =
    thread?.progress && thread.progress.total > 0 && thread.progress.done < thread.progress.total
      ? `${thread.progress.done}/${thread.progress.total}`
      : null
  const fallback =
    pr || diff || progress ? null : liveCmd ? `$ ${liveCmd}` : (lane.lastHeadline ?? null)
  return { project, pr, diff, progress, fallback }
}

function laneAlertBadge(lane: PulseLane): { tone: 'warning' | 'danger'; text: string } | null {
  if (lane.stage === 'answer' || lane.attentionId !== null) {
    const spans = props.spansByConversation.get(lane.conversationId) ?? []
    const openSpan = spans.findLast((span) => span.endedAt === null)
    const since = openSpan?.startedAt ?? lane.activeAt
    return { tone: 'warning', text: formatDurationCompact(Math.max(0, props.nowMs - since)) }
  }
  if (lane.stage === 'stopped') {
    return { tone: 'warning', text: t('thread.stage.stopped') }
  }
  if (lane.stage === 'blocked' || lane.stage === 'halted' || lane.stage === 'silent') {
    const failures = props.threads.get(lane.conversationId)?.pullRequest?.checks.failed.length ?? 0
    return { tone: 'danger', text: failures > 0 ? `×${failures}` : '!' }
  }
  return null
}

function laneRowClasses(row: PulseLaneRow) {
  const lane = row.lane
  return {
    'is-coordinator': lane.role === 'coordinator',
    'is-nested': row.nested,
    'is-needs-me': lane.stage === 'answer' || lane.stage === 'stopped' || lane.attentionId !== null,
    'is-blocked': lane.stage === 'blocked' || lane.stage === 'halted' || lane.stage === 'silent',
    'is-selected': props.selectedLaneId === lane.conversationId,
  }
}

function groupHeaderTitle(group: PulsePartitionedLanes['groups'][number]): string {
  if (group.kind === 'needs') return t('pulse.group.needsTitle')
  if (group.kind === 'active') return t('pulse.group.activeTitle')
  if (group.kind === 'project') return group.title ?? group.projectId ?? ''
  if (group.kind === 'node') return t('pulse.group.nodePrefix', { title: group.title ?? '' })
  return t('pulse.group.unlinkedNode')
}

function spanTooltipMeta(item: RenderedSpan): string {
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
  return t('pulse.tip.duration', { start: startText, end: endText, duration: durText })
}

function markerTooltipSub(item: RenderedMarker): string {
  const kindLabel = t(`pulse.marker.${item.marker.kind}`)
  return item.marker.label ? `${kindLabel} · ${item.marker.label}` : kindLabel
}

function ribbonTooltipSub(bar: PulseRibbonBar): string {
  const time = formatClockShort(bar.midAt, props.viewStart, props.viewEnd, locale.value)
  return bar.full ? t('pulse.ribbon.tipFull', { time }) : t('pulse.ribbon.tipNormal', { time })
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

function updateTrackWidth() {
  const el = axisTrackEl.value
  if (el && el.clientWidth > 0) {
    trackWidthPx.value = el.clientWidth
    return
  }
  const inner = lanesInnerEl.value
  if (inner && inner.clientWidth > labelWidth.value) {
    trackWidthPx.value = inner.clientWidth - labelWidth.value
  }
}

function computeHandoffArcsNow() {
  handoffFrame = null
  updateTrackWidth()
  if (!props.showHandoffs || !props.handoffs.length) {
    handoffArcs.value = []
    return
  }

  const inner = lanesInnerEl.value
  const currentLabelW = labelWidth.value
  const trackWidth = Math.max(
    1,
    inner && inner.clientWidth > currentLabelW
      ? inner.clientWidth - currentLabelW
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
    () => props.laneLines,
    () => props.showCompleted,
    () => props.showHandoffs,
    () => props.viewStart,
    () => props.viewEnd,
    labelWidth,
  ],
  async () => {
    await nextTick()
    computeHandoffArcsNow()
  },
  { immediate: true, flush: 'post' },
)

let resizeObserver: ResizeObserver | null = null
onMounted(() => {
  if (typeof ResizeObserver !== 'undefined') {
    resizeObserver = new ResizeObserver(() => scheduleHandoffRecalc())
    if (lanesInnerEl.value) resizeObserver.observe(lanesInnerEl.value)
    if (axisTrackEl.value) resizeObserver.observe(axisTrackEl.value)
  }
  computeHandoffArcsNow()
})

// --- Draggable label-column divider (aligned with @dou-ui/gantt) ---
const resizingCol = ref(false)
let colResizeState: { pointerId: number; startX: number; startWidth: number } | null = null

function onColDividerPointerDown(event: PointerEvent) {
  if (event.button !== 0) return
  event.preventDefault()
  colResizeState = {
    pointerId: event.pointerId,
    startX: event.clientX,
    startWidth: labelWidth.value,
  }
  resizingCol.value = true
  window.addEventListener('pointermove', onColDividerMove)
  window.addEventListener('pointerup', stopColResize)
  window.addEventListener('pointercancel', stopColResize)
}

function onColDividerMove(event: PointerEvent) {
  if (!colResizeState || event.pointerId !== colResizeState.pointerId) return
  const next = Math.round(
    clamp(
      colResizeState.startWidth + (event.clientX - colResizeState.startX),
      MIN_LABEL_COL_WIDTH,
      MAX_LABEL_COL_WIDTH,
    ),
  )
  if (next !== labelWidth.value) labelWidth.value = next
}

function stopColResize() {
  if (!colResizeState) return
  colResizeState = null
  resizingCol.value = false
  window.removeEventListener('pointermove', onColDividerMove)
  window.removeEventListener('pointerup', stopColResize)
  window.removeEventListener('pointercancel', stopColResize)
}

function onColDividerKeydown(event: KeyboardEvent) {
  if (event.key === 'ArrowLeft') {
    event.preventDefault()
    labelWidth.value = clamp(labelWidth.value - 12, MIN_LABEL_COL_WIDTH, MAX_LABEL_COL_WIDTH)
  } else if (event.key === 'ArrowRight') {
    event.preventDefault()
    labelWidth.value = clamp(labelWidth.value + 12, MIN_LABEL_COL_WIDTH, MAX_LABEL_COL_WIDTH)
  } else if (event.key === 'Home') {
    event.preventDefault()
    labelWidth.value = DEFAULT_LABEL_COL_WIDTH
  }
}

// --- Pointer drag pan & wheel zoom ---
let panState: {
  pointerId: number
  startX: number
  lastX: number
  width: number
  moved: boolean
} | null = null
let suppressTrackClickUntil = 0
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
    startX: event.clientX,
    lastX: event.clientX,
    width: Math.max(1, rect.width),
    moved: false,
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
  if (Math.abs(event.clientX - panState.startX) >= 4) panState.moved = true
  panState.lastX = event.clientX
  const deltaMs = -(dx / panState.width) * (props.viewEnd - props.viewStart)
  emit('pan', deltaMs)
}

function stopPan() {
  if (!panState) return
  if (panState.moved) suppressTrackClickUntil = Date.now() + 250
  panState = null
  panning.value = false
  window.removeEventListener('pointermove', onWindowPointerMove)
  window.removeEventListener('pointerup', onWindowPointerUp)
  window.removeEventListener('pointercancel', onWindowPointerUp)
}

function onWindowPointerUp(event: PointerEvent) {
  if (!panState || event.pointerId !== panState.pointerId) return
  stopPan()
}

function onLaneTrackClick(laneId: string) {
  if (Date.now() < suppressTrackClickUntil) return
  emit('select', laneId, null)
}

function onScrollWheel(event: WheelEvent) {
  const rect = getTrackRect()
  if (!rect || rect.width <= 0) return

  if (event.ctrlKey || event.metaKey) {
    event.preventDefault()
    const ratio = clamp((event.clientX - rect.left) / rect.width, 0, 1)
    const factor = event.deltaY > 0 ? 1.25 : 0.8
    emit('zoom', ratio, factor)
    return
  }

  const horizontalDelta =
    Math.abs(event.deltaX) > Math.abs(event.deltaY)
      ? event.deltaX
      : event.shiftKey
        ? event.deltaY
        : 0
  if (horizontalDelta !== 0) {
    event.preventDefault()
    const deltaMs = (horizontalDelta / rect.width) * (props.viewEnd - props.viewStart)
    emit('pan', deltaMs)
  }
}

function onLaneOpenClick(lane: PulseLane) {
  emit('select', lane.conversationId, null)
  emit('openLane', lane)
}

function onScrollPointerLeave() {
  emit('hoveringChange', false)
}

onBeforeUnmount(() => {
  stopPan()
  stopColResize()
  resizeObserver?.disconnect()
  if (handoffFrame !== null) cancelAnimationFrame(handoffFrame)
})
</script>

<template>
  <div
    class="pulse-canvas"
    data-pulse-canvas
    :class="{ 'is-panning': panning, 'is-resizing-col': resizingCol }"
    :style="{
      '--pulse-label-w': `${labelWidth}px`,
      '--pulse-lane-h': `${laneHeightPx}px`,
      '--pulse-span-h': `${spanHeightPx}px`,
    }"
  >
    <div
      ref="scrollEl"
      class="pulse-lanes-scroll"
      @wheel="onScrollWheel"
      @pointerenter="emit('hoveringChange', true)"
      @pointerleave="onScrollPointerLeave"
    >
      <div class="pulse-sticky-head">
        <!-- Optional concurrency watermark ribbon -->
        <div v-if="showConcurrency" class="pulse-grid-row pulse-ribbon-row" data-pulse-ribbon>
          <div class="pulse-label-col pulse-ribbon-label">
            <span class="pulse-axis-title">{{ t('pulse.ribbon.label') }}</span>
            <button
              type="button"
              class="pulse-slot-trigger"
              :title="t('pulse.ribbon.badgeTitle', { usage: slotBadgeText })"
              @click="emit('toggleConcurrency')"
            >
              <DouTag
                size="sm"
                round
                :tone="slotFull ? 'warning' : 'primary'"
                class="pulse-mono"
                data-pulse-slot-badge
              >
                {{ slotBadgeText }}
              </DouTag>
            </button>
          </div>
          <div class="pulse-track-col pulse-ribbon-track">
            <DouTooltip
              v-for="bar in ribbonBars"
              :key="bar.index"
              follow-cursor
              :show-delay="80"
              :hide-delay="40"
            >
              <div
                class="pulse-ribbon-bar"
                :class="bar.full ? 'is-full' : 'is-normal'"
                :style="{
                  left: `${bar.leftPct}%`,
                  width: `${bar.widthPct}%`,
                  height: `${bar.heightPx}px`,
                }"
              />
              <template #content>
                <div class="pulse-tip-content">
                  <div class="pulse-tip-head">
                    {{ t('pulse.ribbon.tipTitle', { active: bar.active, slots: bar.slots }) }}
                  </div>
                  <div class="pulse-tip-sub">{{ ribbonTooltipSub(bar) }}</div>
                </div>
              </template>
            </DouTooltip>
          </div>
        </div>

        <!-- Time axis header -->
        <div class="pulse-grid-row pulse-axis-row">
          <div class="pulse-label-col pulse-axis-label">
            <span class="pulse-axis-title">{{ t('pulse.axis.lanes') }}</span>
            <button
              v-if="!showConcurrency"
              type="button"
              class="pulse-slot-trigger"
              :title="t('pulse.ribbon.badgeTitle', { usage: slotBadgeText })"
              @click="emit('toggleConcurrency')"
            >
              <DouTag
                size="sm"
                round
                :tone="slotFull ? 'warning' : 'primary'"
                class="pulse-mono"
                data-pulse-slot-badge
              >
                <template v-if="liveSlotActive > 0" #leading>
                  <DouStatusDot
                    :tone="slotFull ? 'warning' : 'primary'"
                    size="sm"
                    :ripple="!slotFull"
                    aria-hidden="true"
                  />
                </template>
                {{ slotBadgeText }}
              </DouTag>
            </button>
          </div>
          <div
            ref="axisTrackEl"
            class="pulse-track-col pulse-axis-track"
            @pointerdown="onTrackPointerDown"
            @dblclick="emit('resetWindow')"
          >
            <div
              v-for="tick in axisHeaderLayout.visibleLabelTicks"
              :key="tick.at"
              class="pulse-axis-tick pulse-mono"
              :style="{ left: `${tick.pct}%` }"
            >
              {{ formatClockShort(tick.at, viewStart, viewEnd, locale) }}
            </div>
            <div
              v-if="returnGeometry && axisHeaderLayout.showReturnBadge"
              class="pulse-axis-return-badge pulse-mono"
              :class="`is-${axisHeaderLayout.returnAlign}`"
              data-pulse-return-badge
              :style="{ left: `${returnGeometry.retPct}%` }"
            >
              {{ t('pulse.axis.returnBadge', { time: returnGeometry.timeText }) }}
            </div>
            <div
              v-if="showNowLine"
              class="pulse-axis-now-mark"
              aria-hidden="true"
              :style="{ left: `${nowPct}%` }"
            />
            <div
              v-if="showNowLine"
              class="pulse-axis-now-badge pulse-mono"
              :class="`is-${axisHeaderLayout.nowAlign}`"
              data-pulse-now-badge
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

        <!-- Optional author <-> reviewer handoff Bezier arcs -->
        <svg
          v-if="showHandoffs && handoffArcs.length"
          class="pulse-handoff-svg"
          data-pulse-handoff-svg
          :viewBox="`0 0 ${handoffSvgSize.width} ${handoffSvgSize.height}`"
          :style="{ height: `${handoffSvgSize.height}px` }"
        >
          <g v-for="arc in handoffArcs" :key="arc.key" data-pulse-handoff-arc>
            <DouTooltip follow-cursor :show-delay="80" :hide-delay="40">
              <path class="pulse-handoff-hit" :d="arc.d" />
              <template #content>
                <div class="pulse-tip-content">
                  <div class="pulse-tip-head">{{ t(`pulse.handoff.${arc.tone}`) }}</div>
                  <div v-if="arc.handoff.label" class="pulse-tip-sub">{{ arc.handoff.label }}</div>
                  <div class="pulse-tip-meta pulse-mono">
                    {{
                      t('pulse.tip.at', {
                        time: formatClockShort(arc.handoff.at, viewStart, viewEnd, locale),
                      })
                    }}
                  </div>
                </div>
              </template>
            </DouTooltip>
            <path class="pulse-handoff-path" :class="`is-${arc.tone}`" :d="arc.d" />
            <circle
              class="pulse-handoff-dot"
              :class="`is-${arc.tone}`"
              :cx="arc.x"
              :cy="arc.y2"
              r="3.5"
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
              <DouStatusDot
                :tone="laneDot(partitioned.coordinator).tone"
                :ripple="laneDot(partitioned.coordinator).ripple"
                size="sm"
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
              <DouTag
                v-if="laneAlertBadge(partitioned.coordinator)"
                size="sm"
                round
                :tone="laneAlertBadge(partitioned.coordinator)!.tone"
                class="pulse-mono"
              >
                {{ laneAlertBadge(partitioned.coordinator)!.text }}
              </DouTag>
            </div>
            <div v-if="showLaneMeta" class="pulse-lane-meta">
              <span v-if="laneMeta(partitioned.coordinator).fallback" class="pulse-meta-fallback">
                {{ laneMeta(partitioned.coordinator).fallback }}
              </span>
            </div>
          </div>
          <div
            class="pulse-track-col pulse-lane-track"
            @pointerdown="onTrackPointerDown"
            @click="onLaneTrackClick(partitioned.coordinator.conversationId)"
            @dblclick="emit('resetWindow')"
          >
            <DouTooltip
              v-for="item in spansForLane(partitioned.coordinator)"
              :key="item.span.id"
              follow-cursor
              :show-delay="80"
              :hide-delay="40"
            >
              <div
                class="pulse-span"
                :class="[
                  `is-${item.visualKind}`,
                  `is-tone-${item.toneGroup}`,
                  {
                    'pulse-span-running': item.running,
                    'is-selected': selectedSpanId === item.span.id,
                  },
                ]"
                :data-span-id="item.span.id"
                :style="{ left: `${item.leftPct}%`, width: `${item.widthPct}%` }"
                @click.stop="emit('select', partitioned.coordinator.conversationId, item.span.id)"
              >
                <span v-if="item.showText" class="pulse-span-text">{{ item.inlineText }}</span>
              </div>
              <template #content>
                <div class="pulse-tip-content">
                  <div class="pulse-tip-head">
                    {{ laneTitle(partitioned.coordinator) }} ·
                    {{ t(`pulse.stage.${item.visualKind}`) }}
                  </div>
                  <div v-if="item.span.label" class="pulse-tip-sub">{{ item.span.label }}</div>
                  <div v-if="item.liveCommand" class="pulse-tip-detail pulse-mono">
                    {{ t('pulse.tip.liveCommand', { command: item.liveCommand }) }}
                  </div>
                  <div class="pulse-tip-meta pulse-mono">{{ spanTooltipMeta(item) }}</div>
                </div>
              </template>
            </DouTooltip>
            <DouTooltip
              v-for="item in markersForLane(partitioned.coordinator)"
              :key="`${item.marker.seq}-${item.marker.kind}`"
              follow-cursor
              :show-delay="80"
              :hide-delay="40"
            >
              <div
                class="pulse-marker"
                :class="`is-${item.tone}`"
                :data-marker-kind="item.marker.kind"
                :style="{ left: `${item.pct}%` }"
                @click.stop="emit('select', partitioned.coordinator.conversationId, null)"
              />
              <template #content>
                <div class="pulse-tip-content">
                  <div class="pulse-tip-head">{{ laneTitle(partitioned.coordinator) }}</div>
                  <div class="pulse-tip-sub">{{ markerTooltipSub(item) }}</div>
                  <div class="pulse-tip-meta pulse-mono">
                    {{
                      t('pulse.tip.at', {
                        time: formatClockShort(item.marker.at, viewStart, viewEnd, locale),
                      })
                    }}
                  </div>
                </div>
              </template>
            </DouTooltip>
          </div>
        </div>

        <!-- Grouped active lanes -->
        <template v-for="group in partitioned.groups" :key="group.key">
          <div class="pulse-grid-row pulse-group-row" :data-pulse-group="group.key">
            <div class="pulse-label-col pulse-group-label">
              <FolderIcon
                v-if="group.kind === 'project'"
                class="pulse-group-icon"
                aria-hidden="true"
              />
              <DouStatusDot
                v-else-if="group.kind === 'needs' || group.kind === 'active'"
                :tone="group.kind === 'needs' ? 'warning' : 'primary'"
                size="sm"
                aria-hidden="true"
              />
              <span class="pulse-group-title">{{ groupHeaderTitle(group) }}</span>
              <DouTag
                v-if="group.kind === 'project' && group.isCurrent"
                size="sm"
                round
                tone="primary"
              >
                {{ t('pulse.group.projectCurrent') }}
              </DouTag>
              <DouTag
                size="sm"
                round
                class="pulse-mono"
                :tone="
                  group.kind === 'needs'
                    ? 'warning'
                    : group.kind === 'active'
                      ? 'primary'
                      : 'neutral'
                "
              >
                {{ group.rows.length }}
              </DouTag>
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
                <CornerDownRightIcon
                  v-if="row.nested"
                  :size="13"
                  class="pulse-nested-prefix"
                  aria-hidden="true"
                />
                <DouStatusDot
                  :tone="laneDot(row.lane).tone"
                  :ripple="laneDot(row.lane).ripple"
                  size="sm"
                  aria-hidden="true"
                />
                <button
                  type="button"
                  class="pulse-lane-title"
                  :title="
                    row.lane.role === 'coordinator'
                      ? t('pulse.openCoordinator')
                      : t('pulse.openThread', { title: laneTitle(row.lane) })
                  "
                  @click.stop="onLaneOpenClick(row.lane)"
                >
                  {{ laneTitle(row.lane) }}
                </button>
                <DouTag
                  v-if="laneAlertBadge(row.lane)"
                  size="sm"
                  round
                  :tone="laneAlertBadge(row.lane)!.tone"
                  class="pulse-mono"
                >
                  {{ laneAlertBadge(row.lane)!.text }}
                </DouTag>
              </div>
              <div v-if="showLaneMeta" class="pulse-lane-meta">
                <template v-for="meta in [laneMeta(row.lane)]" :key="row.lane.conversationId">
                  <span v-if="meta.pr" class="pulse-mono">{{ meta.pr }}</span>
                  <ChangeTotals
                    v-if="meta.diff"
                    :totals="meta.diff"
                    :thread-id="row.lane.conversationId"
                  />
                  <span v-if="meta.progress" class="pulse-mono">{{ meta.progress }}</span>
                  <span v-if="meta.fallback" class="pulse-meta-fallback">{{ meta.fallback }}</span>
                </template>
              </div>
            </div>
            <div
              class="pulse-track-col pulse-lane-track"
              @pointerdown="onTrackPointerDown"
              @click="onLaneTrackClick(row.lane.conversationId)"
              @dblclick="emit('resetWindow')"
            >
              <DouTooltip
                v-for="item in spansForLane(row.lane)"
                :key="item.span.id"
                follow-cursor
                :show-delay="80"
                :hide-delay="40"
              >
                <div
                  class="pulse-span"
                  :class="[
                    `is-${item.visualKind}`,
                    `is-tone-${item.toneGroup}`,
                    {
                      'pulse-span-running': item.running,
                      'is-selected': selectedSpanId === item.span.id,
                    },
                  ]"
                  :data-span-id="item.span.id"
                  :style="{ left: `${item.leftPct}%`, width: `${item.widthPct}%` }"
                  @click.stop="emit('select', row.lane.conversationId, item.span.id)"
                >
                  <span v-if="item.showText" class="pulse-span-text">{{ item.inlineText }}</span>
                </div>
                <template #content>
                  <div class="pulse-tip-content">
                    <div class="pulse-tip-head">
                      {{ laneTitle(row.lane) }} · {{ t(`pulse.stage.${item.visualKind}`) }}
                    </div>
                    <div v-if="item.span.label" class="pulse-tip-sub">{{ item.span.label }}</div>
                    <div v-if="item.liveCommand" class="pulse-tip-detail pulse-mono">
                      {{ t('pulse.tip.liveCommand', { command: item.liveCommand }) }}
                    </div>
                    <div class="pulse-tip-meta pulse-mono">{{ spanTooltipMeta(item) }}</div>
                  </div>
                </template>
              </DouTooltip>
              <DouTooltip
                v-for="item in markersForLane(row.lane)"
                :key="`${item.marker.seq}-${item.marker.kind}`"
                follow-cursor
                :show-delay="80"
                :hide-delay="40"
              >
                <div
                  class="pulse-marker"
                  :class="`is-${item.tone}`"
                  :data-marker-kind="item.marker.kind"
                  :style="{ left: `${item.pct}%` }"
                  @click.stop="emit('select', row.lane.conversationId, null)"
                />
                <template #content>
                  <div class="pulse-tip-content">
                    <div class="pulse-tip-head">{{ laneTitle(row.lane) }}</div>
                    <div class="pulse-tip-sub">{{ markerTooltipSub(item) }}</div>
                    <div class="pulse-tip-meta pulse-mono">
                      {{
                        t('pulse.tip.at', {
                          time: formatClockShort(item.marker.at, viewStart, viewEnd, locale),
                        })
                      }}
                    </div>
                  </div>
                </template>
              </DouTooltip>
            </div>
          </div>
        </template>

        <slot v-if="!partitioned.groups.length" name="empty" />

        <!-- Completed collapsible section -->
        <template v-if="partitioned.completed.length">
          <div class="pulse-completed-bar">
            <DouButton
              variant="ghost"
              size="sm"
              class="pulse-completed-toggle"
              data-pulse-completed-toggle
              :aria-expanded="showCompleted"
              @click="emit('update:showCompleted', !showCompleted)"
            >
              <template #leading>
                <ChevronDownIcon v-if="showCompleted" aria-hidden="true" />
                <ChevronRightIcon v-else aria-hidden="true" />
              </template>
              <span class="pulse-completed-left">
                <span>{{
                  t('pulse.completed.summary', { count: partitioned.completed.length })
                }}</span>
                <span v-if="completedPreviewTitles" class="pulse-completed-names">
                  （{{ completedPreviewTitles }}）
                </span>
              </span>
            </DouButton>
          </div>

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
                  <CornerDownRightIcon
                    v-if="row.nested"
                    :size="13"
                    class="pulse-nested-prefix"
                    aria-hidden="true"
                  />
                  <DouStatusDot
                    :tone="laneDot(row.lane).tone"
                    :ripple="laneDot(row.lane).ripple"
                    size="sm"
                    aria-hidden="true"
                  />
                  <button
                    type="button"
                    class="pulse-lane-title"
                    :title="t('pulse.openThread', { title: laneTitle(row.lane) })"
                    @click.stop="onLaneOpenClick(row.lane)"
                  >
                    {{ laneTitle(row.lane) }}
                  </button>
                </div>
                <div v-if="showLaneMeta" class="pulse-lane-meta">
                  <template v-for="meta in [laneMeta(row.lane)]" :key="row.lane.conversationId">
                    <span v-if="meta.project" class="pulse-meta-project">{{ meta.project }}</span>
                    <span v-if="meta.pr" class="pulse-mono">{{ meta.pr }}</span>
                    <ChangeTotals
                      v-if="meta.diff"
                      :totals="meta.diff"
                      :thread-id="row.lane.conversationId"
                    />
                    <span v-if="meta.fallback" class="pulse-meta-fallback">{{
                      meta.fallback
                    }}</span>
                  </template>
                </div>
              </div>
              <div
                class="pulse-track-col pulse-lane-track"
                @pointerdown="onTrackPointerDown"
                @click="onLaneTrackClick(row.lane.conversationId)"
                @dblclick="emit('resetWindow')"
              >
                <DouTooltip
                  v-for="item in spansForLane(row.lane)"
                  :key="item.span.id"
                  follow-cursor
                  :show-delay="80"
                  :hide-delay="40"
                >
                  <div
                    class="pulse-span"
                    :class="[
                      `is-${item.visualKind}`,
                      `is-tone-${item.toneGroup}`,
                      {
                        'pulse-span-running': item.running,
                        'is-selected': selectedSpanId === item.span.id,
                      },
                    ]"
                    :data-span-id="item.span.id"
                    :style="{ left: `${item.leftPct}%`, width: `${item.widthPct}%` }"
                    @click.stop="emit('select', row.lane.conversationId, item.span.id)"
                  >
                    <span v-if="item.showText" class="pulse-span-text">{{ item.inlineText }}</span>
                  </div>
                  <template #content>
                    <div class="pulse-tip-content">
                      <div class="pulse-tip-head">
                        {{ laneTitle(row.lane) }} · {{ t(`pulse.stage.${item.visualKind}`) }}
                      </div>
                      <div v-if="item.span.label" class="pulse-tip-sub">{{ item.span.label }}</div>
                      <div class="pulse-tip-meta pulse-mono">{{ spanTooltipMeta(item) }}</div>
                    </div>
                  </template>
                </DouTooltip>
                <DouTooltip
                  v-for="item in markersForLane(row.lane)"
                  :key="`${item.marker.seq}-${item.marker.kind}`"
                  follow-cursor
                  :show-delay="80"
                  :hide-delay="40"
                >
                  <div
                    class="pulse-marker"
                    :class="`is-${item.tone}`"
                    :data-marker-kind="item.marker.kind"
                    :style="{ left: `${item.pct}%` }"
                    @click.stop="emit('select', row.lane.conversationId, null)"
                  />
                  <template #content>
                    <div class="pulse-tip-content">
                      <div class="pulse-tip-head">{{ laneTitle(row.lane) }}</div>
                      <div class="pulse-tip-sub">{{ markerTooltipSub(item) }}</div>
                      <div class="pulse-tip-meta pulse-mono">
                        {{
                          t('pulse.tip.at', {
                            time: formatClockShort(item.marker.at, viewStart, viewEnd, locale),
                          })
                        }}
                      </div>
                    </div>
                  </template>
                </DouTooltip>
              </div>
            </div>
          </template>
        </template>
      </div>
    </div>

    <!-- Draggable left-column divider (aligned with @dou-ui/gantt) -->
    <div
      class="pulse-col-divider"
      data-pulse-col-divider
      role="separator"
      aria-orientation="vertical"
      tabindex="0"
      :aria-label="t('pulse.axis.resize')"
      :aria-valuenow="labelWidth"
      :aria-valuemin="MIN_LABEL_COL_WIDTH"
      :aria-valuemax="MAX_LABEL_COL_WIDTH"
      @pointerdown="onColDividerPointerDown"
      @dblclick="labelWidth = DEFAULT_LABEL_COL_WIDTH"
      @keydown="onColDividerKeydown"
    />
  </div>
</template>

<style scoped>
.pulse-canvas,
.pulse-canvas * {
  box-sizing: border-box;
}

.pulse-canvas {
  --pulse-return-tint: color-mix(in srgb, var(--dou-color-primary) 5%, transparent);
  --pulse-label-w: 248px;
  position: relative;
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  background: var(--dou-color-bg-surface);
}

:global(html[data-dou-theme='dark'] .pulse-canvas) {
  --pulse-return-tint: color-mix(in srgb, var(--dou-color-primary) 9%, transparent);
}

.pulse-canvas.is-panning {
  user-select: none;
  cursor: grabbing;
}

.pulse-canvas.is-resizing-col {
  user-select: none;
  cursor: col-resize;
}

.pulse-lanes-scroll {
  position: relative;
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overflow-x: hidden;
}

.pulse-sticky-head {
  position: sticky;
  top: 0;
  z-index: 20;
  flex: none;
  background: var(--dou-color-bg-canvas);
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

/* Draggable divider aligned with @dou-ui/gantt */
.pulse-col-divider {
  position: absolute;
  top: 0;
  bottom: 0;
  left: var(--pulse-label-w);
  z-index: 25;
  width: 7px;
  margin-left: -4px;
  cursor: col-resize;
}

.pulse-col-divider::after {
  content: '';
  position: absolute;
  top: 0;
  bottom: 0;
  left: 3px;
  width: 2px;
  background: transparent;
  transition: background-color 0.14s ease;
}

.pulse-col-divider:hover::after,
.pulse-col-divider:focus-visible::after,
.pulse-canvas.is-resizing-col .pulse-col-divider::after {
  background: var(--dou-color-primary);
}

.pulse-col-divider:focus-visible {
  outline: none;
}

.pulse-slot-trigger {
  display: inline-flex;
  align-items: center;
  flex: none;
  padding: 0;
  border: 0;
  background: transparent;
  cursor: pointer;
  border-radius: 9999px;
}

.pulse-slot-trigger:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: 2px;
}

/* Concurrency Ribbon */
.pulse-ribbon-row {
  height: 28px;
  border-bottom: 1px solid var(--dou-color-border-subtle);
  background: var(--dou-color-bg-canvas);
}

.pulse-ribbon-label {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 0 12px;
  background: var(--dou-color-bg-canvas);
  font-size: 11.5px;
  color: var(--dou-color-text-secondary);
  white-space: nowrap;
  overflow: hidden;
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

/* Axis Header: zero wrap, collision-free ticks & badges */
.pulse-axis-row {
  height: 32px;
  border-bottom: 1px solid var(--dou-color-border);
  background: var(--dou-color-bg-canvas);
}

.pulse-axis-label {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 0 12px;
  background: var(--dou-color-bg-canvas);
  font-size: 12px;
  font-weight: 500;
  color: var(--dou-color-text-secondary);
  white-space: nowrap;
  overflow: hidden;
}

.pulse-axis-title {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
  padding-left: 6px;
  border-left: 1px solid var(--dou-color-border-subtle);
  font-size: 11px;
  color: var(--dou-color-text-tertiary);
  white-space: nowrap;
  pointer-events: none;
}

.pulse-axis-return-badge {
  position: absolute;
  top: 50%;
  z-index: 3;
  height: 18px;
  padding: 0 6px;
  display: inline-flex;
  align-items: center;
  border-radius: 4px;
  border: 1px solid color-mix(in srgb, var(--dou-color-primary) 35%, transparent);
  background: var(--dou-color-bg-canvas);
  color: var(--dou-color-primary-text);
  font-size: 10.5px;
  font-weight: 500;
  white-space: nowrap;
  pointer-events: none;
}

.pulse-axis-return-badge.is-left {
  transform: translate(4px, -50%);
}

.pulse-axis-return-badge.is-right {
  transform: translate(calc(-100% - 4px), -50%);
}

.pulse-axis-now-mark {
  position: absolute;
  top: 0;
  bottom: 0;
  z-index: 5;
  width: 1.5px;
  background: var(--dou-color-primary);
  pointer-events: none;
}

.pulse-axis-now-mark::before {
  content: '';
  position: absolute;
  top: 0;
  left: -3px;
  width: 7px;
  height: 5px;
  background: var(--dou-color-primary);
  clip-path: polygon(0 0, 100% 0, 50% 100%);
}

.pulse-axis-now-badge {
  position: absolute;
  top: 50%;
  z-index: 4;
  height: 18px;
  padding: 0 6px;
  display: inline-flex;
  align-items: center;
  border-radius: 4px;
  border: 1px solid color-mix(in srgb, var(--dou-color-primary) 38%, transparent);
  background: var(--dou-color-primary-soft);
  color: var(--dou-color-primary-text);
  font-size: 10.5px;
  font-weight: 600;
  white-space: nowrap;
  pointer-events: none;
}

.pulse-axis-now-badge.is-right {
  transform: translate(calc(-100% - 4px), -50%);
}

.pulse-axis-now-badge.is-left {
  transform: translate(4px, -50%);
}

/* Lanes container & background overlays */
.pulse-lanes-inner {
  position: relative;
  flex: 1 0 auto;
}

.pulse-grid-overlay {
  position: absolute;
  inset: 0 0 0 var(--pulse-label-w);
  pointer-events: none;
  z-index: 1;
  overflow: hidden;
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
  border-left: 1px dashed color-mix(in srgb, var(--dou-color-primary) 48%, transparent);
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
  stroke: var(--dou-color-primary);
}

.pulse-handoff-path.is-changes {
  stroke: var(--dou-color-warning);
}

.pulse-handoff-path.is-pass {
  stroke: var(--dou-color-success);
}

.pulse-handoff-dot {
  pointer-events: none;
}

.pulse-handoff-dot.is-request {
  fill: var(--dou-color-primary);
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

.pulse-group-icon {
  flex: none;
  width: 13px;
  height: 13px;
  color: var(--dou-color-text-tertiary);
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
  height: var(--pulse-lane-h, 38px);
  border-bottom: 1px solid var(--dou-color-border-subtle);
  transition: background-color 0.12s ease;
}

.pulse-lane-row.is-coordinator .pulse-label-col {
  background: color-mix(in srgb, var(--dou-color-bg-muted) 40%, var(--dou-color-bg-surface));
}

.pulse-lane-row:hover .pulse-label-col,
.pulse-lane-row:hover .pulse-track-col {
  background: color-mix(in srgb, var(--dou-color-bg-muted) 42%, transparent);
}

.pulse-lane-row.is-selected .pulse-label-col,
.pulse-lane-row.is-selected .pulse-track-col {
  background: var(--dou-color-primary-soft);
}

.pulse-lane-row.is-needs-me .pulse-label-col {
  box-shadow: inset 3px 0 0 var(--dou-color-warning);
}

.pulse-lane-row.is-blocked .pulse-label-col {
  box-shadow: inset 3px 0 0 var(--dou-color-danger);
}

.pulse-lane-row.is-completed {
  opacity: 0.76;
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
  padding-left: 18px;
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
  flex: none;
  color: var(--dou-color-text-tertiary);
}

.pulse-lane-row.is-nested .pulse-lane-meta {
  padding-left: 33px;
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

.pulse-meta-project {
  flex: none;
  max-width: 96px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--dou-color-text-secondary);
}

.pulse-meta-fallback {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}

.pulse-lane-track {
  height: 100%;
  user-select: none;
  cursor: grab;
}

.pulse-lane-track:active {
  cursor: grabbing;
}

/* 4-group quiet semantic span palette (aligned with @dou-ui/gantt) */
.pulse-span {
  position: absolute;
  top: 50%;
  z-index: 2;
  display: flex;
  align-items: center;
  height: var(--pulse-span-h, 20px);
  padding: 0 7px;
  border-radius: 5px;
  border: 1px solid transparent;
  transform: translateY(-50%);
  overflow: hidden;
  font-size: 11px;
  font-weight: 500;
  white-space: nowrap;
  cursor: pointer;
  transition:
    border-color 0.12s ease,
    box-shadow 0.12s ease;
}

.pulse-span.is-tone-active {
  background: var(--dou-color-primary-soft);
  border-color: color-mix(in srgb, var(--dou-color-primary) 42%, transparent);
  color: var(--dou-color-primary-text);
}

.pulse-span.is-tone-attention {
  background: var(--dou-color-warning-soft);
  border-color: color-mix(in srgb, var(--dou-color-warning) 48%, transparent);
  color: var(--dou-color-warning-text);
}

.pulse-span.is-tone-danger {
  background: var(--dou-color-danger-soft);
  border-color: color-mix(in srgb, var(--dou-color-danger) 48%, transparent);
  color: var(--dou-color-danger-text);
}

.pulse-span.is-tone-passive {
  background: var(--dou-color-bg-muted);
  border: 1px dashed var(--dou-color-border-strong);
  color: var(--dou-color-text-secondary);
}

.pulse-span.is-coord {
  background: color-mix(in srgb, var(--dou-color-neutral) 7%, var(--dou-color-bg-surface));
  border: 1px solid var(--dou-color-border-strong);
  color: var(--dou-color-text-secondary);
}

.pulse-span:hover {
  z-index: 6;
  border-color: var(--dou-color-primary);
}

.pulse-span.is-selected {
  z-index: 7;
  border-color: var(--dou-color-primary);
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--dou-color-focus) 32%, transparent);
}

/* Calm right-edge breathing indicator for running spans */
.pulse-span-running::after {
  content: '';
  position: absolute;
  top: 2px;
  bottom: 2px;
  right: 2px;
  width: 3px;
  border-radius: 2px;
  background: currentColor;
  opacity: 0.65;
  pointer-events: none;
  animation: pulse-edge-breathe 1.6s ease-in-out infinite;
}

@keyframes pulse-edge-breathe {
  0%,
  100% {
    opacity: 0.3;
  }
  50% {
    opacity: 0.85;
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
  transform: translate(-50%, -50%) rotate(45deg) scale(1.3);
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

.pulse-marker.is-primary,
.pulse-marker.is-ci,
.pulse-marker.is-review {
  background: var(--dou-color-primary);
}

/* Completed collapsible bar */
.pulse-completed-bar {
  position: relative;
  z-index: 3;
  border-bottom: 1px solid var(--dou-color-border-subtle);
  background: var(--dou-color-bg-surface);
  padding: 2px 6px;
}

.pulse-completed-toggle {
  width: 100%;
  justify-content: flex-start;
  color: var(--dou-color-text-secondary);
  font-weight: 400;
}

.pulse-completed-left {
  display: inline-flex;
  align-items: center;
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
}

.pulse-completed-names {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--dou-color-text-tertiary);
}

/* Tooltip inner structure (inherits DouTooltip surface & text colors) */
.pulse-tip-content {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-width: 260px;
}

.pulse-tip-head {
  font-size: 12px;
  font-weight: 600;
}

.pulse-tip-sub {
  font-size: 11.5px;
  opacity: 0.84;
  word-break: break-word;
}

.pulse-tip-detail {
  margin-top: 2px;
  font-size: 11px;
  opacity: 0.92;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pulse-tip-meta {
  margin-top: 2px;
  font-size: 11px;
  opacity: 0.72;
}

@media (prefers-reduced-motion: reduce) {
  .pulse-span-running::after {
    animation: none;
  }
}
</style>
