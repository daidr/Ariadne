<script setup lang="ts">
import { computed, onBeforeUnmount, shallowRef, useTemplateRef, watch } from 'vue'
import { duration, t } from '../i18n'
import {
  placement,
  type TimeRange,
  type TimelineMode,
  type TimelineModel,
  type TimelineSpan,
} from './timeline'

const props = defineProps<{
  model: TimelineModel
  mode: TimelineMode
  selectedKey: string | null
  hoveredKey: string | null
}>()

const emit = defineEmits<{
  select: [key: string]
  hover: [key: string | null]
}>()

/** The stretch the user marked out; the table brings out its rows. */
const range = defineModel<TimeRange | null>('range', { required: true })

const LANE = 12
const track = useTemplateRef<HTMLDivElement>('track')
/** The part in view once zoomed in; `null` shows it all. */
const viewport = shallowRef<TimeRange | null>(null)
/** The range while it is being dragged out. */
const draft = shallowRef<TimeRange | null>(null)
const panning = shallowRef(false)

let drag: { pointerId: number; x: number; at: number; key: string | null } | null = null
let pan: { pointerId: number; x: number; start: number; moved: boolean } | null = null
let edgeFrame: number | null = null
let edgeDirection = 0
let edgeX = 0

const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value))

const full = computed(() => Math.max(1, props.model.end - props.model.start))
const domain = computed(() => {
  const { model } = props
  if (!viewport.value) return { start: model.start, length: full.value }
  const length = clamp(viewport.value.end - viewport.value.start, 1, full.value)
  return { start: clamp(viewport.value.start, model.start, model.end - length), length }
})
/** The whole timeline drawn wider than the view and shifted, so zooming moves no span. */
const projection = computed(() => ({
  left: `${(-(domain.value.start - props.model.start) / domain.value.length) * 100}%`,
  width: `${(full.value / domain.value.length) * 100}%`,
}))
const height = computed(() => `${props.model.lanes * LANE + 8}px`)

const shown = computed(() => {
  const end = domain.value.start + domain.value.length
  return props.model.spans.filter(
    (span) =>
      span.key === props.selectedKey || (span.end >= domain.value.start && span.start <= end),
  )
})
const active = computed(() => draft.value ?? range.value)
const selectionStyle = computed(() => {
  const marked = active.value
  if (!marked) return {}
  const end = domain.value.start + domain.value.length
  const from = clamp(marked.start, domain.value.start, end)
  const to = clamp(marked.end, from, end)
  return {
    left: `${((from - domain.value.start) / domain.value.length) * 100}%`,
    width: `${((to - from) / domain.value.length) * 100}%`,
  }
})

function spanStyle(span: TimelineSpan) {
  const { left, width } = placement(props.model, span)
  return {
    left: `${left * 100}%`,
    width: span.instant ? '2px' : `max(2px, ${width * 100}%)`,
    top: `${span.lane * LANE + 4}px`,
    // A share of real time means nothing on steps of equal width.
    '--waited': span.waited === null || props.mode === 'sequence' ? '0%' : `${span.waited * 100}%`,
  }
}

const turnStyle = (at: number) => ({
  left: `${placement(props.model, { start: at, end: at }).left * 100}%`,
})

function bandStyle(lane: number, lanes: number) {
  return { top: `${lane * LANE + 4}px`, height: `${lanes * LANE - 4}px` }
}

function label(span: TimelineSpan): string {
  const { row } = span
  const took = row.end === null ? t('trace.running') : duration(row.end - row.start)
  return [row.event, took, row.content].filter(Boolean).join(' · ')
}

const outside = (span: TimelineSpan) =>
  active.value !== null && (span.start > active.value.end || span.end < active.value.start)

// A new conversation's trace starts in full view; new rows of the same one keep the zoom.
watch(
  () => props.mode,
  () => reset(),
)
// A row picked in the table comes into view.
watch(
  () => props.selectedKey,
  (key) => {
    if (!key || !viewport.value) return
    const span = props.model.spans.find((item) => item.key === key)
    if (!span) return
    const { start, length } = domain.value
    const margin = length * 0.06
    if (span.start >= start + margin && span.end <= start + length - margin) return
    const from = clamp(
      (span.start + span.end) / 2 - length / 2,
      props.model.start,
      props.model.end - length,
    )
    viewport.value = { start: from, end: from + length }
  },
)

function fractionAt(clientX: number): number {
  const rect = track.value?.getBoundingClientRect()
  if (!rect) return 0
  return clamp((clientX - rect.left) / Math.max(1, rect.width), 0, 1)
}

const timeAt = (clientX: number) => domain.value.start + fractionAt(clientX) * domain.value.length

function moveView(start: number) {
  const { length } = domain.value
  const from = clamp(start, props.model.start, props.model.end - length)
  viewport.value = { start: from, end: from + length }
}

function onWheel(event: WheelEvent) {
  // A sideways scroll (a trackpad, or Shift with the wheel) moves along; up and down zoom.
  const sideways = event.shiftKey ? event.deltaY : event.deltaX
  if (Math.abs(sideways) > Math.abs(event.deltaY) || event.shiftKey) {
    if (!viewport.value) return
    const rect = track.value?.getBoundingClientRect()
    moveView(domain.value.start + (sideways / Math.max(1, rect?.width ?? 1)) * domain.value.length)
    return
  }
  const anchor = fractionAt(event.clientX)
  const smallest = Math.min(props.mode === 'sequence' ? 4 : 50, full.value)
  const length = clamp(domain.value.length * Math.exp(event.deltaY * 0.0015), smallest, full.value)
  if (length >= full.value * 0.999) {
    viewport.value = null
    return
  }
  const at = domain.value.start + anchor * domain.value.length
  const start = clamp(at - anchor * length, props.model.start, props.model.end - length)
  viewport.value = { start, end: start + length }
}

const keyAt = (event: PointerEvent) =>
  (event.target instanceof Element
    ? event.target.closest<HTMLElement>('[data-key]')?.dataset.key
    : null) ?? null

function onPointerDown(event: PointerEvent) {
  // Right button (or the middle one) drags the view along; left marks out a range.
  if (event.button === 2 || event.button === 1) {
    pan = { pointerId: event.pointerId, x: event.clientX, start: domain.value.start, moved: false }
    panning.value = true
    track.value?.setPointerCapture(event.pointerId)
    return
  }
  if (event.button !== 0) return
  const at = timeAt(event.clientX)
  drag = { pointerId: event.pointerId, x: event.clientX, at, key: keyAt(event) }
  draft.value = { start: at, end: at }
  track.value?.setPointerCapture(event.pointerId)
}

function draftTo(clientX: number) {
  if (!drag) return
  const at = timeAt(clientX)
  draft.value = at < drag.at ? { start: at, end: drag.at } : { start: drag.at, end: at }
}

function onPointerMove(event: PointerEvent) {
  if (pan?.pointerId === event.pointerId) {
    if (Math.abs(event.clientX - pan.x) >= 3) pan.moved = true
    if (!viewport.value) return
    const rect = track.value?.getBoundingClientRect()
    const shift = (event.clientX - pan.x) / Math.max(1, rect?.width ?? 1)
    moveView(pan.start - shift * domain.value.length)
    return
  }
  if (drag?.pointerId !== event.pointerId) return
  draftTo(event.clientX)
  edgePan(event.clientX)
}

function stopEdgePan() {
  edgeDirection = 0
  if (edgeFrame !== null) cancelAnimationFrame(edgeFrame)
  edgeFrame = null
}

// Dragging a range against an edge of a zoomed view moves the view along.
function runEdgePan() {
  edgeFrame = null
  if (!drag || !viewport.value || edgeDirection === 0) return
  moveView(viewport.value.start + edgeDirection * domain.value.length * 0.018)
  draftTo(edgeX)
  edgeFrame = requestAnimationFrame(runEdgePan)
}

function edgePan(clientX: number) {
  const rect = track.value?.getBoundingClientRect()
  if (!rect || !viewport.value) return stopEdgePan()
  edgeX = clientX
  const edge = Math.min(32, rect.width * 0.08)
  edgeDirection = clientX <= rect.left + edge ? -1 : clientX >= rect.right - edge ? 1 : 0
  if (edgeDirection === 0) stopEdgePan()
  else if (edgeFrame === null) edgeFrame = requestAnimationFrame(runEdgePan)
}

function onPointerUp(event: PointerEvent) {
  stopEdgePan()
  if (pan?.pointerId === event.pointerId) {
    if (!pan.moved) range.value = null
    pan = null
    panning.value = false
    return
  }
  if (drag?.pointerId !== event.pointerId) return
  const moved = Math.abs(event.clientX - drag.x) >= 3
  const marked = draft.value
  const key = drag.key
  drag = null
  draft.value = null
  // A click: on a span picks its row, elsewhere drops the range.
  if (!moved) {
    range.value = null
    if (key) emit('select', key)
    return
  }
  if (marked) range.value = marked
}

function onPointerCancel() {
  stopEdgePan()
  drag = null
  pan = null
  draft.value = null
  panning.value = false
}

function reset() {
  stopEdgePan()
  viewport.value = null
  range.value = null
  draft.value = null
  drag = null
  pan = null
  panning.value = false
}

onBeforeUnmount(stopEdgePan)
defineExpose({ reset })
</script>

<template>
  <section class="timeline" :style="{ height }" :aria-label="t('trace.timeline.label')">
    <div class="labels" aria-hidden="true">
      <span
        v-for="band in model.bands"
        :key="band.group"
        class="label"
        :style="bandStyle(band.lane, band.lanes)"
        >{{ t(`trace.timeline.${band.group}`) }}</span
      >
    </div>
    <div
      ref="track"
      class="track"
      :data-panning="panning || undefined"
      tabindex="0"
      :aria-label="t('trace.timeline.help')"
      @wheel.prevent="onWheel"
      @pointerdown="onPointerDown"
      @pointermove="onPointerMove"
      @pointerup="onPointerUp"
      @pointercancel="onPointerCancel"
      @contextmenu.prevent
      @dblclick.prevent="reset"
      @keydown.esc.prevent="reset"
    >
      <div
        v-for="band in model.bands.slice(1)"
        :key="band.group"
        class="band-line"
        :style="{ top: `${band.lane * LANE + 2}px` }"
      />
      <div
        v-if="active"
        class="selection"
        :data-dragging="draft !== null || undefined"
        :style="selectionStyle"
      />
      <div class="projection" :style="projection">
        <span
          v-for="turn in model.turns.slice(1)"
          :key="turn.turnId"
          class="turn"
          :style="turnStyle(turn.at)"
        />
        <button
          v-for="span in shown"
          :key="span.key"
          type="button"
          tabindex="-1"
          :data-key="span.key"
          :class="[
            'span',
            `span--${span.group}`,
            `span--${span.row.kind}`,
            {
              'span--instant': span.instant,
              'span--running': span.running,
              'span--danger': span.row.tone === 'danger',
              'span--warning': span.row.tone === 'warning',
              'span--selected': span.key === selectedKey,
              'span--hovered': span.key === hoveredKey,
              'span--outside': outside(span),
            },
          ]"
          :style="spanStyle(span)"
          :title="label(span)"
          :aria-label="label(span)"
          @pointerenter="emit('hover', span.key)"
          @pointerleave="emit('hover', null)"
        />
      </div>
    </div>
  </section>
</template>

<style scoped>
.timeline {
  display: grid;
  grid-template-columns: 60px minmax(0, 1fr);
  flex: none;
  border-bottom: 1px solid var(--dou-color-border);
  user-select: none;
}

.labels {
  position: relative;
  border-right: 1px solid var(--dou-color-border-subtle);
}

.label {
  position: absolute;
  right: 6px;
  display: flex;
  align-items: center;
  color: var(--dou-color-text-tertiary);
  font-size: var(--dou-font-size-xs);
  line-height: 1;
}

.track {
  position: relative;
  overflow: hidden;
  cursor: crosshair;
  touch-action: none;
}

.track[data-panning='true'] {
  cursor: grabbing;
}

.track:focus-visible {
  outline: 1px solid var(--dou-color-focus);
  outline-offset: -1px;
}

.projection {
  position: absolute;
  inset: 0 auto 0 0;
}

.band-line {
  position: absolute;
  right: 0;
  left: 0;
  border-top: 1px dashed var(--dou-color-border-subtle);
  pointer-events: none;
}

.turn {
  position: absolute;
  top: 0;
  bottom: 0;
  width: 1px;
  background: var(--dou-color-border-strong);
  pointer-events: none;
}

.span {
  position: absolute;
  height: 8px;
  min-width: 2px;
  padding: 0;
  border: 0;
  border-radius: 2px;
  cursor: pointer;
  opacity: 0.85;
}

/* Waiting for the first output is the lighter part of a request. */
.span--request {
  background: linear-gradient(
    to right,
    var(--dou-color-primary-soft) 0,
    var(--dou-color-primary-soft) var(--waited),
    var(--dou-color-primary) var(--waited)
  );
}

.span--research {
  background: linear-gradient(
    to right,
    color-mix(in srgb, var(--dou-color-chart-5) 30%, transparent) 0,
    color-mix(in srgb, var(--dou-color-chart-5) 30%, transparent) var(--waited),
    var(--dou-color-chart-5) var(--waited)
  );
}

.span--tool {
  background: var(--dou-color-success);
}

.span--retry,
.span--warning {
  background: var(--dou-color-warning);
}

.span--danger {
  background: var(--dou-color-danger);
}

.span--instant {
  height: 12px;
  margin-top: -2px;
  background: var(--dou-color-text-tertiary);
}

.span--instant.span--warning {
  background: var(--dou-color-warning);
}

.span--running::after {
  position: absolute;
  inset: 0;
  border-radius: inherit;
  background: repeating-linear-gradient(
    -45deg,
    transparent 0 4px,
    color-mix(in srgb, var(--dou-color-bg-surface) 40%, transparent) 4px 8px
  );
  content: '';
}

.span--outside {
  opacity: 0.2;
}

.span:hover,
.span--hovered,
.span--selected {
  z-index: 2;
  opacity: 1;
  box-shadow:
    0 0 0 1px var(--dou-color-bg-surface),
    0 0 0 2px var(--dou-color-primary);
}

.selection {
  position: absolute;
  z-index: 1;
  top: 0;
  bottom: 0;
  min-width: 1px;
  background: color-mix(in srgb, var(--dou-color-primary) 10%, transparent);
  border-inline: 2px solid var(--dou-color-primary);
  pointer-events: none;
}
</style>
