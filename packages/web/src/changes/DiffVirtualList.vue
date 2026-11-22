<script lang="ts">
export interface DiffVirtualListExpose {
  scrollToIndex: (index: number, align?: 'start' | 'center' | 'end' | 'nearest') => void
  scrollTo: (offset: number) => void
  el: () => HTMLElement | null
}
</script>

<script setup lang="ts" generic="T">
import {
  computed,
  onBeforeUnmount,
  onMounted,
  onUpdated,
  ref,
  shallowRef,
  useTemplateRef,
} from 'vue'

/**
 * Flow-based virtual list for changed files.
 *
 * Unlike transform-positioned virtual lists, visible items stay in normal document flow
 * between top and bottom padding so `position: sticky` on each file header sticks cleanly
 * to this scroll container for every file, not just the first one.
 */
const props = withDefaults(
  defineProps<{
    items: readonly T[]
    itemKey: keyof T | ((item: T, index: number) => string | number)
    estimateSize?: number | ((item: T, index: number) => number)
    overscan?: number
  }>(),
  {
    estimateSize: 96,
    overscan: 1,
  },
)

const emit = defineEmits<{
  scroll: [event: Event]
}>()

const elRef = useTemplateRef<HTMLElement>('el')
const scrollTop = ref(0)
const viewportSize = ref(0)

const known = new Map<string | number, number>()
const measureVersion = shallowRef(0)

function keyOf(item: T, index: number): string | number {
  if (typeof props.itemKey === 'function') return props.itemKey(item, index)
  return item[props.itemKey] as string | number
}

function estimateOf(index: number): number {
  const { estimateSize } = props
  if (typeof estimateSize === 'function') return estimateSize(props.items[index]!, index)
  return estimateSize
}

function sizeOf(index: number): number {
  void measureVersion.value
  return known.get(keyOf(props.items[index]!, index)) ?? estimateOf(index)
}

interface Offsets {
  total: number
  start: (index: number) => number
  find: (y: number) => number
}

const offsets = computed<Offsets>(() => {
  const count = props.items.length
  const starts = new Float64Array(count + 1)
  for (let i = 0; i < count; i++) {
    starts[i + 1] = starts[i]! + sizeOf(i)
  }
  const start = (index: number) => starts[Math.max(0, Math.min(index, count))] ?? 0
  function find(y: number): number {
    if (count === 0) return 0
    let low = 0
    let high = count - 1
    while (low < high) {
      const mid = (low + high + 1) >> 1
      if (starts[mid]! <= y) low = mid
      else high = mid - 1
    }
    return Math.max(0, low)
  }
  return { total: starts[count] ?? 0, start, find }
})

const range = computed(() => {
  const count = props.items.length
  if (!count) return { from: 0, to: 0 }
  const { find } = offsets.value
  const first = find(scrollTop.value)
  const last = find(scrollTop.value + Math.max(viewportSize.value, 1) - 1)
  return {
    from: Math.max(0, first - props.overscan),
    to: Math.min(count, last + 1 + props.overscan),
  }
})

const visible = computed(() => {
  const { from, to } = range.value
  return props.items.slice(from, to).map((item, offset) => {
    const index = from + offset
    return { item, index, key: keyOf(item, index) }
  })
})

const topPadding = computed(() => offsets.value.start(range.value.from))
const bottomPadding = computed(() =>
  Math.max(0, offsets.value.total - offsets.value.start(range.value.to)),
)

const tracked = new Map<HTMLElement, { key: string | number; index: number }>()

function updateMeasured(
  entries: readonly { element: HTMLElement; key: string | number; index: number }[],
) {
  const container = elRef.value
  const top = container ? container.scrollTop : 0
  const currentOffsets = offsets.value
  let shift = 0
  let changed = false
  const seen = new Set<string | number>()

  for (const { element, key, index } of entries) {
    if (seen.has(key)) continue
    if (
      !element.isConnected ||
      index < 0 ||
      index >= props.items.length ||
      keyOf(props.items[index]!, index) !== key
    ) {
      continue
    }
    seen.add(key)
    const measured = element.offsetHeight
    if (measured <= 0) continue
    const before = known.get(key) ?? estimateOf(index)
    if (measured === before && known.has(key)) continue
    known.set(key, measured)
    changed = true
    if (top > 0 && currentOffsets.start(index + 1) <= top + 1) {
      shift += measured - before
    }
  }

  if (!changed) return
  measureVersion.value++
  if (container && shift !== 0) {
    container.scrollTop = Math.max(0, container.scrollTop + shift)
    scrollTop.value = container.scrollTop
  }
}

const itemObserver =
  typeof ResizeObserver === 'undefined'
    ? null
    : new ResizeObserver((entries) => {
        const items: { element: HTMLElement; key: string | number; index: number }[] = []
        for (const entry of entries) {
          const element = entry.target as HTMLElement
          const meta = tracked.get(element)
          if (meta) items.push({ element, key: meta.key, index: meta.index })
        }
        if (items.length) updateMeasured(items)
      })

function trackItem(element: HTMLElement | null, key: string | number, index: number) {
  if (!element) return
  const existing = tracked.get(element)
  if (existing) {
    existing.key = key
    existing.index = index
    return
  }
  tracked.set(element, { key, index })
  itemObserver?.observe(element)
}

onUpdated(() => {
  for (const [element] of tracked) {
    if (element.isConnected) continue
    itemObserver?.unobserve(element)
    tracked.delete(element)
  }
})

let containerObserver: ResizeObserver | null = null
onMounted(() => {
  const container = elRef.value
  if (!container) return
  viewportSize.value = container.clientHeight
  if (typeof ResizeObserver === 'undefined') return
  containerObserver = new ResizeObserver(() => {
    viewportSize.value = container.clientHeight
  })
  containerObserver.observe(container)
})

let settleFrame: number | null = null
function cancelSettle() {
  if (settleFrame !== null) {
    cancelAnimationFrame(settleFrame)
    settleFrame = null
  }
}

onBeforeUnmount(() => {
  cancelSettle()
  itemObserver?.disconnect()
  containerObserver?.disconnect()
  tracked.clear()
})

function onScroll(event: Event) {
  const container = event.currentTarget as HTMLElement
  scrollTop.value = container.scrollTop
  emit('scroll', event)
}

function scrollTo(offset: number) {
  const container = elRef.value
  if (!container) return
  container.scrollTop = offset
  scrollTop.value = container.scrollTop
}

function scrollToIndex(
  index: number,
  align: 'start' | 'center' | 'end' | 'nearest' = 'nearest',
  settle = 2,
) {
  if (index < 0 || index >= props.items.length) return
  cancelSettle()
  if (settle > 0 && typeof requestAnimationFrame !== 'undefined') {
    settleFrame = requestAnimationFrame(() => {
      settleFrame = null
      scrollToIndex(index, align, settle - 1)
    })
  }
  const { start } = offsets.value
  const top = start(index)
  const bottom = start(index + 1)
  const height = viewportSize.value
  const current = scrollTop.value
  let target = current
  if (align === 'start') target = top
  else if (align === 'end') target = bottom - height
  else if (align === 'center') target = top - (height - (bottom - top)) / 2
  else if (top < current) target = top
  else if (bottom > current + height) target = bottom - height
  if (target !== current) scrollTo(Math.max(0, target))
}

defineExpose<DiffVirtualListExpose>({
  scrollToIndex: (index, align) => scrollToIndex(index, align),
  scrollTo,
  el: () => elRef.value,
})
</script>

<template>
  <div ref="el" tabindex="0" class="diff-virtual-list" @scroll="onScroll">
    <div
      v-if="items.length"
      class="diff-virtual-list-content"
      :style="{ paddingTop: `${topPadding}px`, paddingBottom: `${bottomPadding}px` }"
    >
      <div
        v-for="entry in visible"
        :key="entry.key"
        :ref="(node) => trackItem(node as HTMLElement | null, entry.key, entry.index)"
        class="diff-virtual-list-item"
        :data-index="entry.index"
      >
        <slot :item="entry.item" :index="entry.index" />
      </div>
    </div>
    <slot v-else name="empty" />
  </div>
</template>

<style scoped>
.diff-virtual-list {
  position: relative;
  overflow-y: auto;
  overflow-anchor: none;
  overscroll-behavior: contain;
  outline: none;
  scroll-padding-top: var(--file-diff-header-height, 40px);
}

.diff-virtual-list:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: -2px;
}

.diff-virtual-list-content {
  box-sizing: border-box;
  width: 100%;
}

.diff-virtual-list-item {
  box-sizing: border-box;
  width: 100%;
}

.diff-virtual-list-item:not([data-index='0']) {
  border-top: 1px solid var(--dou-color-border);
}
</style>
