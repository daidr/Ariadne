<script setup lang="ts">
import type {
  AttentionView,
  ConversationView,
  LiveCommand,
  NeedsMeThreadSummary,
  PulseLane,
  PulseMarker,
  PulseSpan,
  ThreadView,
} from '@ariadne/core'
import { CloseIcon, FileDiffIcon, ListTreeIcon, MessagesIcon } from '@dou-ui/icons'
import { DouButton, DouDrawer } from 'dou-ui'
import { computed, ref, watch } from 'vue'
import StageIcon from '../components/StageIcon.vue'
import { t } from '../i18n'
import { branchChanges } from '../review/merge-offered'
import PulseInspectorBody from './PulseInspectorBody.vue'
import { resolveLaneStage, type PulseNodeMeta } from './model'

const props = defineProps<{
  open: boolean
  drawerMode: 'docked' | 'right' | 'bottom'
  lane: PulseLane | null
  spans: readonly PulseSpan[]
  markers: readonly PulseMarker[]
  selectedSpanId: number | null
  viewStart: number
  viewEnd: number
  nowMs: number
  attentions: readonly AttentionView[]
  attentionsLoading: boolean
  thread: ThreadView | null
  threadSummary: ThreadView | NeedsMeThreadSummary | null
  nodeMeta: PulseNodeMeta | null
  reviewTarget: PulseLane | null
  liveCommand: LiveCommand | null
  liveWait: ConversationView['wait']
}>()

const emit = defineEmits<{
  close: []
  openLane: [lane: PulseLane]
  openDiff: [lane: PulseLane]
  openTrace: [lane: PulseLane]
  selectLane: [laneId: string]
  mutated: []
}>()

const displayTitle = computed(() => {
  const lane = props.lane
  if (!lane) return t('pulse.inspector.title')
  if (lane.role === 'coordinator') return t('coordinator.title')
  return lane.title || lane.conversationId
})

const laneStage = computed(() => (props.lane ? resolveLaneStage(props.lane) : 'idle'))

const hasDiff = computed(() => Boolean(props.thread && branchChanges(props.thread)))

const bodyEl = ref<HTMLElement | null>(null)

watch(
  () => props.lane?.conversationId,
  () => {
    if (bodyEl.value) bodyEl.value.scrollTop = 0
  },
)

const openActionLabel = computed(() =>
  props.lane?.role === 'coordinator'
    ? t('pulse.inspector.openCoordinator')
    : t('pulse.inspector.openThread'),
)
</script>

<template>
  <!-- Medium (right slide-over) or Narrow (bottom swipeable sheet) -->
  <DouDrawer
    v-if="drawerMode !== 'docked'"
    :open="open && Boolean(lane)"
    :placement="drawerMode"
    :swipeable="drawerMode === 'bottom'"
    :snap-points="drawerMode === 'bottom' ? ['76%', '92%'] : undefined"
    :size="drawerMode === 'right' ? 360 : undefined"
    :title="displayTitle"
    @update:open="(value) => !value && emit('close')"
  >
    <template v-if="lane" #title>
      <div class="pulse-insp-heading">
        <StageIcon :stage="laneStage" :pull-request-state="thread?.pullRequest?.state" />
        <button
          type="button"
          class="pulse-insp-title-btn"
          data-pulse-inspector-title
          :title="openActionLabel"
          @click="emit('openLane', lane)"
        >
          {{ displayTitle }}
        </button>
      </div>
    </template>
    <div v-if="lane" class="pulse-drawer-content" data-pulse-inspector>
      <PulseInspectorBody
        :key="lane.conversationId"
        :lane="lane"
        :spans="spans"
        :markers="markers"
        :selected-span-id="selectedSpanId"
        :view-start="viewStart"
        :view-end="viewEnd"
        :now-ms="nowMs"
        :attentions="attentions"
        :attentions-loading="attentionsLoading"
        :thread="thread"
        :thread-summary="threadSummary"
        :node-meta="nodeMeta"
        :review-target="reviewTarget"
        :live-command="liveCommand"
        :live-wait="liveWait"
        @open-lane="emit('openLane', $event)"
        @select-lane="emit('selectLane', $event)"
        @mutated="emit('mutated')"
      />
    </div>
    <template #footer>
      <div v-if="lane" class="pulse-sheet-footer">
        <DouButton
          size="sm"
          tone="primary"
          class="pulse-insp-primary-btn"
          data-pulse-open-thread
          @click="emit('openLane', lane)"
        >
          <template #leading><MessagesIcon aria-hidden="true" /></template>
          {{ openActionLabel }}
        </DouButton>
        <DouButton
          v-if="hasDiff"
          size="sm"
          variant="outline"
          data-pulse-open-diff
          @click="emit('openDiff', lane)"
        >
          <template #leading><FileDiffIcon aria-hidden="true" /></template>
          {{ t('pulse.inspector.openDiff') }}
        </DouButton>
        <DouButton
          size="sm"
          variant="outline"
          data-pulse-open-trace
          @click="emit('openTrace', lane)"
        >
          <template #leading><ListTreeIcon aria-hidden="true" /></template>
          {{ t('pulse.inspector.openTrace') }}
        </DouButton>
      </div>
    </template>
  </DouDrawer>

  <!-- Desktop wide: docked inspector pane hosted inside DouSplitter -->
  <aside
    v-else-if="open && lane"
    class="pulse-inspector"
    data-pulse-inspector
    :aria-label="t('pulse.inspector.title')"
  >
    <div class="pulse-insp-header">
      <div class="pulse-insp-heading">
        <StageIcon :stage="laneStage" :pull-request-state="thread?.pullRequest?.state" />
        <button
          type="button"
          class="pulse-insp-title-btn"
          data-pulse-inspector-title
          :title="openActionLabel"
          @click="emit('openLane', lane)"
        >
          {{ displayTitle }}
        </button>
      </div>
      <DouButton
        size="sm"
        variant="ghost"
        data-pulse-inspector-close
        :aria-label="t('pulse.inspector.close')"
        :title="t('pulse.inspector.close')"
        @click="emit('close')"
      >
        <CloseIcon aria-hidden="true" />
      </DouButton>
    </div>

    <div ref="bodyEl" class="pulse-insp-body">
      <PulseInspectorBody
        :key="lane.conversationId"
        :lane="lane"
        :spans="spans"
        :markers="markers"
        :selected-span-id="selectedSpanId"
        :view-start="viewStart"
        :view-end="viewEnd"
        :now-ms="nowMs"
        :attentions="attentions"
        :attentions-loading="attentionsLoading"
        :thread="thread"
        :thread-summary="threadSummary"
        :node-meta="nodeMeta"
        :review-target="reviewTarget"
        :live-command="liveCommand"
        :live-wait="liveWait"
        @open-lane="emit('openLane', $event)"
        @select-lane="emit('selectLane', $event)"
        @mutated="emit('mutated')"
      />
    </div>

    <div class="pulse-insp-footer">
      <DouButton
        size="sm"
        tone="primary"
        class="pulse-insp-primary-btn"
        data-pulse-open-thread
        @click="emit('openLane', lane)"
      >
        <template #leading><MessagesIcon aria-hidden="true" /></template>
        {{ openActionLabel }}
      </DouButton>
      <DouButton
        v-if="hasDiff"
        size="sm"
        variant="outline"
        data-pulse-open-diff
        @click="emit('openDiff', lane)"
      >
        <template #leading><FileDiffIcon aria-hidden="true" /></template>
        {{ t('pulse.inspector.openDiff') }}
      </DouButton>
      <DouButton size="sm" variant="outline" data-pulse-open-trace @click="emit('openTrace', lane)">
        <template #leading><ListTreeIcon aria-hidden="true" /></template>
        {{ t('pulse.inspector.openTrace') }}
      </DouButton>
    </div>
  </aside>
</template>

<style scoped>
.pulse-inspector {
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  min-width: 0;
  min-height: 0;
  background: var(--dou-color-bg-surface);
}

.pulse-insp-header {
  display: flex;
  flex: none;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--dou-color-border);
}

.pulse-insp-heading {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.pulse-insp-title-btn {
  min-width: 0;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--dou-color-text);
  font: inherit;
  font-size: 13.5px;
  font-weight: 600;
  text-align: left;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
}

.pulse-insp-title-btn:hover {
  color: var(--dou-color-primary-text);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.pulse-insp-title-btn:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: 2px;
  border-radius: 3px;
}

.pulse-insp-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 12px 14px;
}

.pulse-drawer-content {
  min-width: 0;
}

.pulse-insp-footer {
  display: flex;
  flex: none;
  flex-wrap: wrap;
  gap: 8px;
  padding: 10px 14px;
  border-top: 1px solid var(--dou-color-border);
}

.pulse-sheet-footer {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  width: 100%;
}

.pulse-insp-primary-btn {
  flex: 1 1 auto;
}
</style>
