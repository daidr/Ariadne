<script setup lang="ts">
import type { ArtifactSummary, BackgroundView, ChangeTotals, ThreadView } from '@ariadne/core'
import { FileDiffIcon, PackageIcon } from '@dou-ui/icons'
import { DouButton, DouPopover } from 'dou-ui'
import { onBeforeUnmount, onMounted, onUpdated, shallowRef, useTemplateRef } from 'vue'
import { t } from '../i18n'
import { useOpenView } from '../opened-from'
import { kindIcon } from '../artifacts/kinds'
import BackgroundTasks from './BackgroundTasks.vue'
import PullRequestTag from './PullRequestTag.vue'

defineProps<{
  conversationId: string
  changes: { threadId: string; totals: ChangeTotals | null } | null
  artifacts: ArtifactSummary[]
  background: BackgroundView[]
  thread?: Pick<ThreadView, 'id' | 'pullRequest'> | null
}>()
const openView = useOpenView()
const artifactsOpen = shallowRef(false)
const barRef = useTemplateRef<HTMLElement>('bar')
const fadeLeft = shallowRef(false)
const fadeRight = shallowRef(false)
const scrollable = shallowRef(false)

function openArtifact(id: string) {
  artifactsOpen.value = false
  openView({ kind: 'artifact', id })
}

function updateOverflow() {
  const bar = barRef.value
  if (!bar) {
    fadeLeft.value = false
    fadeRight.value = false
    scrollable.value = false
    return
  }
  const max = Math.max(0, bar.scrollWidth - bar.clientWidth)
  if (max <= 1) {
    fadeLeft.value = false
    fadeRight.value = false
    scrollable.value = false
    return
  }
  const rtl = getComputedStyle(bar).direction === 'rtl'
  const clamped = rtl
    ? Math.min(0, Math.max(-max, bar.scrollLeft))
    : Math.min(max, Math.max(0, bar.scrollLeft))
  const offset = Math.abs(clamped)
  const hasStart = offset > 1
  const hasEnd = offset < max - 1
  fadeLeft.value = rtl ? hasEnd : hasStart
  fadeRight.value = rtl ? hasStart : hasEnd
  scrollable.value = true
}

function onWheel(event: WheelEvent) {
  if (event.ctrlKey || event.metaKey) return
  const bar = barRef.value
  if (!bar) return
  const max = bar.scrollWidth - bar.clientWidth
  if (max <= 1) return
  if (event.deltaX !== 0 || event.deltaY === 0) return
  const scale = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? bar.clientWidth : 1
  const delta = event.deltaY * scale
  const next =
    getComputedStyle(bar).direction === 'rtl'
      ? Math.min(0, Math.max(-max, bar.scrollLeft - delta))
      : Math.min(max, Math.max(0, bar.scrollLeft + delta))
  event.preventDefault()
  if (next !== bar.scrollLeft) {
    bar.scrollLeft = next
    updateOverflow()
  }
}

let resizeObserver: ResizeObserver | undefined
let mutationObserver: MutationObserver | undefined

onMounted(() => {
  updateOverflow()
  const bar = barRef.value
  if (!bar) return
  if (typeof ResizeObserver !== 'undefined') {
    resizeObserver = new ResizeObserver(updateOverflow)
    resizeObserver.observe(bar)
  }
  if (typeof MutationObserver !== 'undefined') {
    mutationObserver = new MutationObserver(updateOverflow)
    mutationObserver.observe(bar, { childList: true, subtree: true, characterData: true })
  }
})

onUpdated(updateOverflow)

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  mutationObserver?.disconnect()
})
</script>

<template>
  <div
    ref="bar"
    class="conversation-bar"
    :class="{
      'is-scrollable': scrollable,
      'is-fade-left': fadeLeft,
      'is-fade-right': fadeRight,
    }"
    data-conversation-bar
    @scroll.passive="updateOverflow"
    @wheel="onWheel"
  >
    <DouButton
      v-if="changes && (!changes.totals || changes.totals.files > 0)"
      variant="outline"
      size="sm"
      class="conversation-pill"
      :aria-label="t('changes.open')"
      :title="t('changes.open')"
      @click="openView({ kind: 'changes', threadId: changes.threadId })"
    >
      <template #leading><FileDiffIcon /></template>
      <template v-if="changes.totals">
        <span class="pill-added">+{{ changes.totals.added }}</span>
        <span class="pill-removed">−{{ changes.totals.removed }}</span>
      </template>
      <template v-else>{{ t('changes.open') }}</template>
    </DouButton>
    <PullRequestTag v-if="thread?.pullRequest" :thread="thread" pill />
    <DouPopover
      v-if="artifacts.length"
      v-model:open="artifactsOpen"
      trigger="click"
      placement="top-start"
      :title="t('artifacts.title')"
      :width="320"
      style="box-shadow: 0 2px 8px rgb(0 0 0 / 8%)"
    >
      <DouButton
        variant="outline"
        size="sm"
        class="conversation-pill"
        :aria-expanded="artifactsOpen"
      >
        <template #leading><PackageIcon /></template>
        {{ t('artifacts.count', { count: artifacts.length }) }}
      </DouButton>
      <template #content>
        <div class="capsule-artifacts">
          <DouButton
            v-for="artifact in artifacts"
            :key="artifact.id"
            variant="ghost"
            size="sm"
            class="capsule-artifact"
            :title="artifact.title"
            @click="openArtifact(artifact.id)"
          >
            <template #leading><component :is="kindIcon[artifact.kind]" /></template>
            <span class="capsule-artifact-title">{{ artifact.title }}</span>
          </DouButton>
        </div>
      </template>
    </DouPopover>
    <BackgroundTasks
      v-if="background.length"
      :key="conversationId"
      :conversation-id="conversationId"
      :commands="background"
    />
  </div>
</template>

<style scoped>
.conversation-bar {
  --conversation-bar-fade: 28px;
  display: flex;
  align-items: center;
  gap: 8px;
  height: 36px;
  min-width: 0;
  overflow-x: auto;
  overscroll-behavior-x: contain;
  scrollbar-width: none;
}
.conversation-bar > * {
  flex-shrink: 0;
}
.conversation-bar::-webkit-scrollbar {
  display: none;
}
.conversation-bar.is-scrollable {
  pointer-events: auto;
}
.conversation-bar.is-fade-left {
  mask-image: linear-gradient(to right, transparent, #000 var(--conversation-bar-fade));
}
.conversation-bar.is-fade-right {
  mask-image: linear-gradient(to left, transparent, #000 var(--conversation-bar-fade));
}
.conversation-bar.is-fade-left.is-fade-right {
  mask-image: linear-gradient(
    to right,
    transparent,
    #000 var(--conversation-bar-fade),
    #000 calc(100% - var(--conversation-bar-fade)),
    transparent
  );
}
.conversation-pill {
  flex: none;
  border-radius: 999px;
  font-variant-numeric: tabular-nums;
  box-shadow: none;
  border-color: var(--dou-color-border);
}
.pill-added {
  color: var(--dou-color-success);
}
.pill-removed {
  color: var(--dou-color-danger);
}
.capsule-artifacts {
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 300px;
  overflow: auto;
}
.capsule-artifact {
  justify-content: flex-start;
  width: 100%;
}
.capsule-artifact-title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
