<script setup lang="ts">
import { DouTooltip } from 'dou-ui'
import {
  nextTick,
  onBeforeUnmount,
  onMounted,
  shallowRef,
  useTemplateRef,
  watch,
  type Component,
} from 'vue'
import { t } from '../i18n'
import type { ListKey } from './layout'

const props = defineProps<{
  tabs: { key: ListKey; label: string; icon: Component }[]
  active?: string | number
  dragAcrossGroups: boolean
}>()
const emit = defineEmits<{
  select: [key: ListKey]
  reorder: [key: ListKey, index: number]
  drag: [event: PointerEvent, key: ListKey]
}>()

// Dock owns the panels and cross-group drag. Mount the visible buttons beside its
// scrolling tab list, leaving their panel registrations in the dock.
const anchor = useTemplateRef<HTMLElement>('anchor')
const row = useTemplateRef<HTMLElement>('row')
const header = shallowRef<HTMLElement | null>(null)
const portal = shallowRef<HTMLElement | null>(null)
const controls = shallowRef<Record<string, string | null>>({})
const dragging = shallowRef(false)
const dropSlot = shallowRef<number | null>(null)

function buttons() {
  return [...(row.value?.querySelectorAll<HTMLButtonElement>('button') ?? [])]
}

let restoreScroll = 0
watch([() => props.active, () => props.tabs], ([active]) => {
  cancelAnimationFrame(restoreScroll)
  if (!props.tabs.some((tab) => tab.key === active)) return
  const list = header.value?.querySelector<HTMLElement>(':scope > .dou-tabs__list')
  const left = list?.scrollLeft
  if (!list || !left) return
  // Dock tries to reveal the hidden tab on selection. Keep the normal row in place.
  restoreScroll = requestAnimationFrame(() => (list.scrollLeft = left))
})

function onKeydown(event: KeyboardEvent) {
  if (
    !(event.target instanceof HTMLButtonElement) ||
    event.ctrlKey ||
    event.metaKey ||
    event.shiftKey
  )
    return
  const pinned = buttons()
  const key = props.tabs[pinned.indexOf(event.target)]?.key
  const { altKey, key: pressed } = event
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(pressed)) return
  if (altKey && !key) return
  const regular = header.value?.querySelectorAll<HTMLButtonElement>(
    ':scope > .dou-tabs__list > button:not(.is-disabled, :has(.pane-pinned-label))',
  )
  const tabs = altKey ? pinned : [...pinned, ...(regular ?? [])]
  const index = tabs.indexOf(event.target)
  if (index < 0) return
  const delta = pressed === 'ArrowLeft' ? -1 : 1
  const target = pressed === 'Home' ? 0 : pressed === 'End' ? tabs.length - 1 : index + delta
  const next = tabs[(target + tabs.length) % tabs.length]
  if (!next || (!key && !pinned.includes(next))) return
  event.preventDefault()
  event.stopPropagation()
  if (altKey && key) {
    if (target >= 0 && target < tabs.length) {
      emit('reorder', key, target)
      const button = event.target
      void nextTick(() => button.focus())
    }
  } else {
    next.focus()
    next.click()
  }
}

let gesture: AbortController | undefined
let suppressClickUntil = 0

function select(key: ListKey) {
  if (performance.now() >= suppressClickUntil) emit('select', key)
}

function press(event: PointerEvent, key: ListKey) {
  if (event.button !== 0) return
  gesture?.abort()
  dragging.value = false
  dropSlot.value = null
  const { pointerId, clientX, clientY } = event
  const { signal } = (gesture = new AbortController())
  const from = props.tabs.findIndex((tab) => tab.key === key)
  if (props.dragAcrossGroups) emit('drag', event, key)

  function dragStarted(point: PointerEvent) {
    return props.dragAcrossGroups
      ? document.documentElement.classList.contains('dou-tabs-dragging')
      : dragging.value || Math.hypot(point.clientX - clientX, point.clientY - clientY) > 4
  }

  function insertion(point: PointerEvent) {
    const rect = row.value?.getBoundingClientRect()
    if (
      !rect ||
      point.clientX < rect.left ||
      point.clientX > rect.right ||
      point.clientY < rect.top ||
      point.clientY > rect.bottom
    )
      return null
    const index = buttons().findIndex((button) => {
      const { left, width } = button.getBoundingClientRect()
      return point.clientX < left + width / 2
    })
    const slot = index < 0 ? props.tabs.length : index
    return slot === from || slot === from + 1 ? null : slot
  }
  function end() {
    gesture?.abort()
    dragging.value = false
    dropSlot.value = null
  }
  window.addEventListener(
    'pointermove',
    (point) => {
      if (point.pointerId !== pointerId) return
      dragging.value = dragStarted(point)
      dropSlot.value = dragging.value ? insertion(point) : null
    },
    { signal },
  )
  window.addEventListener(
    'pointerup',
    (point) => {
      if (point.pointerId !== pointerId) return
      const dragged = dragStarted(point)
      const slot = dragged ? insertion(point) : null
      end()
      if (dragged) suppressClickUntil = performance.now() + 100
      if (slot === null) return
      // Apply the fixed group order after Dock has processed the same release.
      queueMicrotask(() => emit('reorder', key, slot > from ? slot - 1 : slot))
    },
    { signal, capture: true },
  )
  window.addEventListener(
    'pointercancel',
    (point) => {
      if (point.pointerId === pointerId) end()
    },
    { signal },
  )
  window.addEventListener(
    'keydown',
    (keyEvent) => {
      if (keyEvent.key === 'Escape') end()
    },
    { signal, capture: true },
  )
}

onMounted(async () => {
  const parent = anchor.value?.closest<HTMLElement>('.dou-tabs__header')
  if (!parent) return
  header.value = parent
  const host = document.createElement('div')
  host.className = 'side-pinned-host'
  parent.prepend(host)
  portal.value = host
  parent.addEventListener('keydown', onKeydown, true)
  await nextTick()
  controls.value = Object.fromEntries(
    [...(header.value?.querySelectorAll<HTMLElement>('[data-pinned-label]') ?? [])].map((label) => [
      label.dataset.pinnedLabel,
      label.closest('button')?.getAttribute('aria-controls') ?? null,
    ]),
  )
})
onBeforeUnmount(() => {
  cancelAnimationFrame(restoreScroll)
  gesture?.abort()
  header.value?.removeEventListener('keydown', onKeydown, true)
  portal.value?.remove()
})
</script>

<template>
  <span ref="anchor" hidden />
  <Teleport v-if="portal" :to="portal">
    <div
      ref="row"
      class="side-pinned-tabs"
      :class="{ 'is-dragging-pin': dragging }"
      role="tablist"
      :aria-label="t('side.pinnedTabs')"
    >
      <DouTooltip v-for="(tab, index) in tabs" :key="tab.key" :content="tab.label">
        <button
          type="button"
          class="dou-tabs__tab pinned-tab"
          :class="{
            'is-selected': active === tab.key,
            'drop-before': dropSlot === index,
            'drop-after': index === tabs.length - 1 && dropSlot === tabs.length,
          }"
          role="tab"
          :aria-label="tab.label"
          :aria-selected="active === tab.key"
          :aria-controls="controls[tab.key] ?? undefined"
          :tabindex="
            active === tab.key || (index === 0 && !tabs.some((item) => item.key === active))
              ? 0
              : -1
          "
          aria-keyshortcuts="Alt+ArrowLeft Alt+ArrowRight Alt+Home Alt+End"
          @pointerdown="press($event, tab.key)"
          @click="select(tab.key)"
        >
          <component :is="tab.icon" aria-hidden="true" />
        </button>
      </DouTooltip>
    </div>
  </Teleport>
</template>

<style scoped>
.side-pinned-tabs {
  display: flex;
  gap: var(--dou-tabs-gap);
  align-items: center;
  padding-block: 4px 5px;
}

.pinned-tab {
  position: relative;
  font-size: 16px;
  touch-action: none;
}

.pinned-tab.is-selected::after {
  position: absolute;
  inset: auto 0 -6px;
  height: 2px;
  border-radius: 1px;
  background: var(--dou-tabs-indicator);
  content: '';
}

.pinned-tab.drop-before::before,
.pinned-tab.drop-after::before {
  position: absolute;
  inset-block: 3px;
  width: 2px;
  border-radius: 1px;
  background: var(--dou-color-primary);
  content: '';
}

.pinned-tab.drop-before::before {
  inset-inline-start: -2px;
}

.pinned-tab.drop-after::before {
  inset-inline-end: -2px;
}
</style>
