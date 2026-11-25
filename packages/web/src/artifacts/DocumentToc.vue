<script setup lang="ts">
import { useLayer } from 'dou-ui'
import { computed, nextTick, onBeforeUnmount, ref, useTemplateRef, watch } from 'vue'
import { t } from '../i18n'
import {
  sampleTocMiniBars,
  TOC_PEEK_WIDTH,
  type DocumentTocHeading,
  type DocumentTocMode,
  type VerticalRange,
} from './document-toc'

const props = defineProps<{
  headings: readonly DocumentTocHeading[]
  activeId: string | null
  mode: DocumentTocMode
  width: number
  collided: boolean
}>()

const emit = defineEmits<{
  select: [id: string]
  'wheel-passthrough': [event: WheelEvent]
}>()

type PeekMode = 'closed' | 'hover' | 'pinned'

const HOVER_OPEN_DELAY_MS = 100
const HOVER_CLOSE_DELAY_MS = 180

const root = useTemplateRef<HTMLElement>('root')
const railSlot = useTemplateRef<HTMLElement>('railSlot')
const panel = useTemplateRef<HTMLElement>('panel')
const scrollBox = useTemplateRef<HTMLElement>('scrollBox')
const track = useTemplateRef<HTMLElement>('track')
const railButton = useTemplateRef<HTMLButtonElement>('railButton')

const peekMode = ref<PeekMode>('closed')
let openTimer: ReturnType<typeof setTimeout> | null = null
let closeTimer: ReturnType<typeof setTimeout> | null = null
let pointerInsideTimer: ReturnType<typeof setTimeout> | null = null
let transferringFocus = false
let pointerInsideRoot = false

const visible = computed(() => props.mode !== 'hidden' && props.headings.length > 0)
const isCollapsed = computed(() => props.mode === 'rail' || props.collided)
const peeking = computed(() => peekMode.value !== 'closed')
const isHoverPeeking = computed(
  () => visible.value && isCollapsed.value && peekMode.value === 'hover',
)
const panelOpen = computed(
  () => visible.value && ((props.mode === 'docked' && !props.collided) || peeking.value),
)
const isFloating = computed(() => visible.value && isCollapsed.value && peeking.value)
const showRail = computed(() => visible.value && isCollapsed.value && !peeking.value)
const panelWidth = computed(() => (props.width > 0 ? props.width : TOC_PEEK_WIDTH))
const miniBars = computed(() => sampleTocMiniBars(props.headings, props.activeId))

function cancelOpenPeek() {
  if (!openTimer) return
  clearTimeout(openTimer)
  openTimer = null
}

function cancelClosePeek() {
  if (!closeTimer) return
  clearTimeout(closeTimer)
  closeTimer = null
}

function clearPointerInsideTimer() {
  if (!pointerInsideTimer) return
  clearTimeout(pointerInsideTimer)
  pointerInsideTimer = null
}

function blurIfInside(container: HTMLElement | null) {
  const active = document.activeElement
  if (active instanceof HTMLElement && container?.contains(active)) {
    transferringFocus = true
    try {
      active.blur()
    } finally {
      transferringFocus = false
    }
  }
}

function focusPanelItem() {
  void nextTick(() => {
    if (!panelOpen.value) return
    const target =
      panel.value?.querySelector<HTMLButtonElement>('.document-toc__item.is-active') ??
      panel.value?.querySelector<HTMLButtonElement>('.document-toc__item')
    target?.focus({ preventScroll: true })
  })
}

function closePeek(returnFocus = false) {
  cancelOpenPeek()
  cancelClosePeek()
  if (peekMode.value === 'closed') return
  blurIfInside(panel.value)
  peekMode.value = 'closed'
  if (returnFocus) {
    void nextTick(() => {
      if (showRail.value) {
        railButton.value?.focus({ preventScroll: true })
      }
    })
  }
}

function resetPeek() {
  cancelOpenPeek()
  cancelClosePeek()
  if (peekMode.value === 'closed') return
  blurIfInside(panel.value)
  peekMode.value = 'closed'
}

function scheduleClosePeek(event?: PointerEvent) {
  if (event?.pointerType === 'touch') return
  if (!isHoverPeeking.value || closeTimer) return
  closeTimer = setTimeout(() => {
    closeTimer = null
    if (!isHoverPeeking.value) return
    if (panel.value?.contains(document.activeElement)) return
    blurIfInside(panel.value)
    peekMode.value = 'closed'
  }, HOVER_CLOSE_DELAY_MS)
}

function onRailPointerEnter(event: PointerEvent) {
  if (event.pointerType === 'touch') return
  if (!visible.value || !isCollapsed.value || peekMode.value !== 'closed') return
  cancelClosePeek()
  cancelOpenPeek()
  openTimer = setTimeout(() => {
    openTimer = null
    if (!visible.value || !isCollapsed.value || peekMode.value !== 'closed') return
    peekMode.value = 'hover'
  }, HOVER_OPEN_DELAY_MS)
}

function onRailPointerLeave(event: PointerEvent) {
  if (event.pointerType === 'touch') return
  cancelOpenPeek()
}

function onPanelPointerLeave(event: PointerEvent) {
  if (!isHoverPeeking.value || event.pointerType === 'touch') return
  const next = event.relatedTarget as Node | null
  if (next && root.value?.contains(next)) return
  scheduleClosePeek(event)
}

function isPointInFloatingZone(x: number, y: number): boolean {
  const rootEl = root.value
  if (!rootEl) return false
  const rootRect = rootEl.getBoundingClientRect()
  const railRect = railSlot.value?.getBoundingClientRect()
  const panelEl = panel.value
  const panelLeft = rootRect.left + 6
  const panelRight = panelLeft + (panelEl?.offsetWidth || panelWidth.value)
  const panelTop = rootRect.top
  const panelHeight = panelEl?.offsetHeight ?? 0
  const panelBottom = panelTop + panelHeight
  const inPanelTarget =
    panelHeight > 0 && x >= rootRect.left && x <= panelRight && y >= panelTop && y <= panelBottom
  const inRail =
    !!railRect &&
    railRect.width > 0 &&
    railRect.height > 0 &&
    x >= railRect.left &&
    x <= railRect.right &&
    y >= railRect.top &&
    y <= railRect.bottom
  return inPanelTarget || inRail
}

function onHoverPointerMove(event: PointerEvent) {
  if (!isHoverPeeking.value || event.pointerType === 'touch') return
  const target = event.target as Node | null
  if (
    (target && root.value?.contains(target)) ||
    isPointInFloatingZone(event.clientX, event.clientY)
  ) {
    cancelClosePeek()
    return
  }
  scheduleClosePeek(event)
}

function onDocumentPointerLeave(event: PointerEvent) {
  if (!isHoverPeeking.value || event.pointerType === 'touch') return
  scheduleClosePeek(event)
}

function pinPeek() {
  if (!visible.value || !isCollapsed.value) return
  cancelOpenPeek()
  cancelClosePeek()
  peekMode.value = 'pinned'
}

function onRailClick(event: MouseEvent) {
  if (!visible.value || !isCollapsed.value) return
  cancelOpenPeek()
  cancelClosePeek()
  const wasFocused = document.activeElement === railButton.value
  const keyboardActivated = wasFocused && event.detail === 0
  if (peekMode.value === 'pinned') {
    closePeek(keyboardActivated)
    return
  }
  if (wasFocused) {
    blurIfInside(railButton.value)
  }
  peekMode.value = 'pinned'
  if (keyboardActivated) {
    focusPanelItem()
  }
}

function onRootPointerDown() {
  pointerInsideRoot = true
  clearPointerInsideTimer()
  pointerInsideTimer = setTimeout(() => {
    pointerInsideRoot = false
    pointerInsideTimer = null
  }, 0)
}

function onRootFocusOut(event: FocusEvent) {
  if (transferringFocus || !isFloating.value) return
  const next = event.relatedTarget as Node | null
  if (next && root.value?.contains(next)) return
  if (!next && pointerInsideRoot) return
  closePeek()
}

function pick(id: string) {
  emit('select', id)
  if (isFloating.value) {
    closePeek()
  }
}

useLayer({
  open: isFloating,
  elements: () => [root.value],
  onEscape(event) {
    event.preventDefault()
    const focusInsideToc = !!root.value?.contains(document.activeElement)
    closePeek(focusInsideToc)
  },
  onOutside() {
    closePeek()
  },
})

watch([isCollapsed, visible], ([collapsed, shown]) => {
  if (!collapsed || !shown) {
    resetPeek()
  }
})

watch(
  panelOpen,
  (open) => {
    if (!open) {
      blurIfInside(panel.value)
    }
  },
  { flush: 'sync' },
)

watch(
  showRail,
  (shown) => {
    if (!shown) {
      blurIfInside(railButton.value)
    }
  },
  { flush: 'sync' },
)

watch(isHoverPeeking, (hovering, _prev, onCleanup) => {
  if (!hovering) return
  document.addEventListener('pointermove', onHoverPointerMove, { passive: true })
  document.documentElement.addEventListener('pointerleave', onDocumentPointerLeave)
  onCleanup(() => {
    document.removeEventListener('pointermove', onHoverPointerMove)
    document.documentElement.removeEventListener('pointerleave', onDocumentPointerLeave)
  })
})

const marker = ref<{ top: number; height: number } | null>(null)

function syncMarker() {
  const container = track.value
  if (!container) {
    marker.value = null
    return
  }
  const activeEl = container.querySelector<HTMLElement>('.document-toc__item.is-active')
  if (!activeEl) {
    marker.value = null
    return
  }
  marker.value = {
    top: activeEl.offsetTop,
    height: activeEl.offsetHeight,
  }
  const scroller = scrollBox.value
  if (!scroller || scroller.scrollHeight <= scroller.clientHeight + 1) return
  const itemTop = activeEl.offsetTop
  const itemBottom = itemTop + activeEl.offsetHeight
  const pad = 12
  if (itemTop < scroller.scrollTop + pad) {
    scroller.scrollTop = Math.max(0, itemTop - pad)
  } else if (itemBottom > scroller.scrollTop + scroller.clientHeight - pad) {
    scroller.scrollTop = itemBottom - scroller.clientHeight + pad
  }
}

watch(
  [() => props.activeId, () => props.headings, panelOpen, panelWidth],
  () => {
    void nextTick(syncMarker)
  },
  { immediate: true, flush: 'post' },
)

function onPanelWheel(event: WheelEvent) {
  if (!panelOpen.value || event.deltaY === 0) return
  const scroller = scrollBox.value
  if (scroller && scroller.scrollHeight > scroller.clientHeight + 1) {
    const canScrollUp = event.deltaY < 0 && scroller.scrollTop > 0
    const canScrollDown =
      event.deltaY > 0 && scroller.scrollTop + scroller.clientHeight < scroller.scrollHeight - 1
    if (canScrollUp || canScrollDown) return
  }
  emit('wheel-passthrough', event)
}

/** Returns the TOC panel's un-transformed vertical bounds in viewport coordinates for collision checks. */
function verticalRange(): VerticalRange | null {
  const container = root.value
  const nav = panel.value
  if (!container || !nav) return null
  const top = container.getBoundingClientRect().top
  const height = nav.offsetHeight
  return height > 0 ? { top, bottom: top + height } : null
}

onBeforeUnmount(() => {
  cancelOpenPeek()
  cancelClosePeek()
  clearPointerInsideTimer()
  document.removeEventListener('pointermove', onHoverPointerMove)
  document.documentElement.removeEventListener('pointerleave', onDocumentPointerLeave)
})

defineExpose({ verticalRange })
</script>

<template>
  <div
    v-if="visible"
    ref="root"
    class="document-toc"
    :class="{
      'is-collapsed': isCollapsed,
      'is-collided': collided,
      'is-peeking': peeking,
      'is-floating': isFloating,
    }"
    :style="{ '--toc-width': `${panelWidth}px` }"
    @pointerdown.capture="onRootPointerDown"
    @focusout="onRootFocusOut"
  >
    <div ref="railSlot" class="document-toc__rail-slot">
      <button
        ref="railButton"
        type="button"
        class="document-toc__rail"
        :inert="!showRail"
        :tabindex="showRail ? 0 : -1"
        :aria-hidden="!showRail"
        :aria-label="t('artifact.document.showToc')"
        :aria-expanded="peeking"
        :title="t('artifact.document.showToc')"
        @pointerenter="onRailPointerEnter"
        @pointerleave="onRailPointerLeave"
        @click="onRailClick"
      >
        <span
          v-for="bar in miniBars"
          :key="bar.id"
          class="document-toc__rail-bar"
          :class="{ 'is-active': bar.active }"
          :style="{ '--toc-depth': bar.depth }"
          aria-hidden="true"
        />
      </button>
      <div
        v-if="isFloating"
        class="document-toc__rail-shield"
        aria-hidden="true"
        @pointerenter="cancelClosePeek"
        @pointerleave="onPanelPointerLeave"
        @click.stop="pinPeek"
      />
    </div>

    <nav
      ref="panel"
      class="document-toc__panel"
      :inert="!panelOpen"
      :aria-label="t('artifact.document.toc')"
      :aria-hidden="!panelOpen"
      @pointerenter="cancelClosePeek"
      @pointerleave="onPanelPointerLeave"
      @wheel="onPanelWheel"
    >
      <div class="document-toc__header">{{ t('artifact.document.toc') }}</div>
      <div ref="scrollBox" class="document-toc__scroll">
        <div ref="track" class="document-toc__track">
          <span
            v-if="marker"
            class="document-toc__marker"
            :style="{
              transform: `translateY(${marker.top}px)`,
              height: `${marker.height}px`,
            }"
            aria-hidden="true"
          />
          <ul class="document-toc__list">
            <li v-for="heading in headings" :key="heading.id" class="document-toc__entry">
              <button
                type="button"
                class="document-toc__item"
                :class="{ 'is-active': heading.id === activeId }"
                :style="{ '--toc-depth': heading.depth }"
                :tabindex="panelOpen ? 0 : -1"
                :aria-current="heading.id === activeId ? 'location' : undefined"
                :title="heading.text"
                @click="pick(heading.id)"
              >
                <span class="document-toc__label">{{ heading.text }}</span>
              </button>
            </li>
          </ul>
        </div>
      </div>
    </nav>
  </div>
</template>

<style scoped>
.document-toc {
  position: absolute;
  top: 24px;
  bottom: 48px;
  left: 0;
  z-index: 4;
  pointer-events: none;
}

.document-toc.is-floating {
  z-index: 10;
}

.document-toc__rail-slot {
  position: absolute;
  top: 2px;
  left: 6px;
  display: flex;
  max-height: calc(100% - 2px);
  pointer-events: none;
}

.document-toc__rail {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 6px;
  min-width: 34px;
  max-height: 100%;
  padding: 10px 10px 10px 6px;
  border: none;
  border-radius: var(--dou-radius-control);
  background: transparent;
  cursor: pointer;
  opacity: 0;
  pointer-events: none;
  transform: translateX(-8px);
  transition:
    opacity var(--dou-motion-duration-normal) var(--dou-motion-easing),
    transform var(--dou-motion-duration-normal) var(--dou-motion-easing),
    background-color var(--dou-motion-duration-fast) var(--dou-motion-easing);
}

.document-toc__rail-shield {
  position: absolute;
  inset: 0;
  pointer-events: auto;
  cursor: pointer;
}

.document-toc.is-collapsed:not(.is-peeking) .document-toc__rail {
  opacity: 1;
  pointer-events: auto;
  transform: translateX(0);
}

.document-toc__rail:hover,
.document-toc__rail:focus-visible {
  background: color-mix(in oklab, var(--dou-color-text) 6%, transparent);
}

.document-toc__rail:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: -2px;
}

.document-toc__rail-bar {
  display: block;
  width: max(8px, calc(16px - min(var(--toc-depth, 0), 2) * 4px));
  height: 2px;
  border-radius: 1px;
  background: color-mix(in oklab, var(--dou-color-text) 24%, transparent);
  transition:
    background-color var(--dou-motion-duration-fast) var(--dou-motion-easing),
    width var(--dou-motion-duration-fast) var(--dou-motion-easing);
}

.document-toc__rail:hover .document-toc__rail-bar {
  background: color-mix(in oklab, var(--dou-color-text) 38%, transparent);
}

.document-toc__rail-bar.is-active,
.document-toc__rail:hover .document-toc__rail-bar.is-active {
  background: var(--dou-color-text);
}

.document-toc__panel {
  position: absolute;
  top: 0;
  left: 16px;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  width: var(--toc-width, 200px);
  max-height: 100%;
  padding: 2px 0;
  border: 1px solid transparent;
  border-radius: var(--dou-radius-container);
  background: transparent;
  pointer-events: auto;
  opacity: 1;
  transform: translateX(0);
  transition:
    transform var(--dou-motion-duration-normal) var(--dou-motion-easing),
    opacity var(--dou-motion-duration-normal) var(--dou-motion-easing),
    background-color var(--dou-motion-duration-fast) var(--dou-motion-easing),
    border-color var(--dou-motion-duration-fast) var(--dou-motion-easing),
    box-shadow var(--dou-motion-duration-fast) var(--dou-motion-easing);
}

.document-toc.is-collapsed:not(.is-peeking) .document-toc__panel {
  opacity: 0;
  pointer-events: none;
  transform: translateX(calc(-100% - 24px));
}

.document-toc.is-floating .document-toc__panel {
  left: 6px;
  padding: 10px 10px 10px 12px;
  border-color: var(--dou-color-border);
  background: color-mix(in oklab, var(--dou-color-bg-surface) 94%, transparent);
  backdrop-filter: blur(12px);
  box-shadow: var(--dou-shadow-md, 0 8px 24px rgb(0 0 0 / 0.12));
}

.document-toc__header {
  flex: none;
  margin-bottom: 6px;
  padding-left: 12px;
  color: var(--dou-color-text-tertiary);
  font-size: 12px;
  font-weight: var(--dou-font-weight-medium);
  line-height: 1.4;
  user-select: none;
}

.document-toc__scroll {
  min-height: 0;
  overflow-y: auto;
  scrollbar-width: thin;
  overscroll-behavior: contain;
}

.document-toc__track {
  position: relative;
  --toc-indent: 12px;
}

.document-toc__track::before {
  content: '';
  position: absolute;
  top: 0;
  bottom: 0;
  left: 0;
  width: 1px;
  background: var(--dou-color-border);
}

.document-toc__marker {
  position: absolute;
  top: 0;
  left: -0.5px;
  z-index: 1;
  width: 2px;
  border-radius: 1px;
  background: var(--dou-color-text);
  transition:
    transform var(--dou-motion-duration-normal) var(--dou-motion-easing),
    height var(--dou-motion-duration-normal) var(--dou-motion-easing);
}

.document-toc__list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.document-toc__entry {
  margin: 0;
  padding: 0;
}

.document-toc__item {
  display: block;
  width: 100%;
  box-sizing: border-box;
  margin: 0;
  padding: 4px 6px 4px calc(12px + min(var(--toc-depth, 0), 3) * var(--toc-indent));
  border: none;
  border-radius: var(--dou-radius-control);
  background: transparent;
  color: var(--dou-color-text-tertiary);
  font-family: inherit;
  font-size: var(--dou-font-size-sm);
  font-weight: inherit;
  line-height: 1.5;
  text-align: start;
  cursor: pointer;
  transition:
    color var(--dou-motion-duration-fast) var(--dou-motion-easing),
    background-color var(--dou-motion-duration-fast) var(--dou-motion-easing);
}

.document-toc__item:hover {
  color: var(--dou-color-text);
  background: color-mix(in oklab, var(--dou-color-text) 4%, transparent);
}

.document-toc__item:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: -2px;
}

.document-toc__item.is-active {
  color: var(--dou-color-text);
  font-weight: var(--dou-font-weight-medium);
}

.document-toc__label {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
