<script setup lang="ts">
import { DouChatArtifact } from 'dou-ui'
import { MessagesIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import ChangeTotals from '../components/ChangeTotals.vue'
import ReviewActions from '../components/ReviewActions.vue'
import StageIcon from '../components/StageIcon.vue'
import { t } from '../i18n'
import ReviewMark from '../review/ReviewMark.vue'
import { branchChanges } from '../review/merge-offered'
import { questionOf, statusLine } from '../stage'
import { activeThreadId, openThreadFrom, state } from '../store'
import { dragThread } from '../side/dock'

/** An entry to a thread an agent put in a message; follows the thread as it moves on. */
const props = defineProps<{ threadId: string }>()

const thread = computed(
  () => state.detail?.threads.find((item) => item.id === props.threadId) ?? null,
)
function open(event: MouseEvent) {
  openThreadFrom(event, props.threadId)
}

// What it does now, asks, or delivered, so the card answers "how far is it" by itself.
const line = computed(() =>
  thread.value
    ? statusLine(thread.value, questionOf(thread.value.id, state.detail?.needsMe))
    : null,
)
// While it works the line shimmers, the way a running artifact card shows it is underway.
const running = computed(() => thread.value?.stage === 'working' || !!line.value?.live)
const changes = computed(() => branchChanges(thread.value))
const hasActions = computed(
  () =>
    !!changes.value ||
    thread.value?.stage === 'review' ||
    (!!thread.value?.review && !thread.value.review.reviewing && thread.value.review.rounds > 0),
)
const headline = computed(() => {
  if (!thread.value) return undefined
  const progress = thread.value.stage === 'working' ? thread.value.progress : null
  const status = t(`thread.stage.${thread.value.stage}`)
  const text = line.value?.text
  const description = text !== status ? text : undefined
  if (!progress) return description
  const count = t('thread.progressShort', progress)
  return description ? `${description} · ${count}` : count
})
</script>

<template>
  <div v-if="thread" class="thread-card">
    <DouChatArtifact
      :title="thread.title"
      :description="headline"
      :status="running && headline ? 'running' : 'done'"
      :active="activeThreadId === thread.id"
      class="thread-card__artifact"
      @open="open"
      @pointerdown="(event: PointerEvent) => dragThread(event, thread!.id)"
    >
      <template #icon><MessagesIcon /></template>
      <template v-if="hasActions" #actions>
        <ChangeTotals v-if="changes" :totals="changes" :thread-id="thread.id" button />
        <ReviewActions v-if="thread.stage === 'review'" :thread-id="thread.id" />
        <ReviewMark :thread="thread" />
      </template>
    </DouChatArtifact>
    <span class="thread-card__stage"><StageIcon :stage="thread.stage" /></span>
  </div>
</template>

<style scoped>
/* The card can be dragged onto the side; a swipe still scrolls the chat. */
.thread-card {
  position: relative;
  width: 100%;
  max-width: 420px;
}

.thread-card__artifact {
  flex-direction: column;
  align-items: stretch;
  width: 100%;
  touch-action: pan-y;
}

/* Opening the thread and reviewing its work have their own rows. */
.thread-card :deep(.dou-chat-artifact__main) {
  align-items: flex-start;
  padding-right: 40px;
}

/* Keep the discussion icon; its stage takes the former arrow's place. */
.thread-card :deep(.dou-chat-artifact__arrow) {
  display: none;
}

.thread-card__stage {
  position: absolute;
  top: 12px;
  right: 12px;
  display: flex;
  pointer-events: none;
}

.thread-card :deep(.dou-chat-artifact__title) {
  overflow-wrap: anywhere;
  white-space: normal;
}

.thread-card :deep(.dou-chat-artifact__description:empty) {
  display: none;
}

.thread-card :deep(.dou-chat-artifact__actions) {
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 4px 8px;
  padding: 0 12px 10px;
}

.thread-card :deep(.review-actions) {
  flex-shrink: 1;
  flex-wrap: wrap;
}
</style>
