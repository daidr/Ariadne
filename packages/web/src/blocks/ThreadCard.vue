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
const headline = computed(() => {
  if (!thread.value) return undefined
  const progress = thread.value.stage === 'working' ? thread.value.progress : null
  const text = line.value?.text ?? (running.value ? t('thread.stage.working') : null)
  if (!text) return progress ? t('thread.progressShort', progress) : undefined
  return progress ? `${text} · ${t('thread.progressShort', progress)}` : text
})
</script>

<template>
  <DouChatArtifact
    v-if="thread"
    :title="thread.title"
    :description="headline"
    :status="running ? 'running' : 'done'"
    :active="activeThreadId === thread.id"
    class="thread-card"
    @open="open"
    @pointerdown="(event: PointerEvent) => dragThread(event, thread!.id)"
  >
    <template #icon><MessagesIcon /></template>
    <template #actions>
      <ChangeTotals
        v-if="branchChanges(thread)"
        :totals="branchChanges(thread)!"
        :thread-id="thread.id"
        button
      />
      <ReviewActions v-if="thread.stage === 'review'" :thread-id="thread.id" />
      <ReviewMark :thread="thread" />
      <StageIcon v-if="!running" :stage="thread.stage" />
    </template>
  </DouChatArtifact>
</template>

<style scoped>
/* The card can be dragged onto the side; a swipe still scrolls the chat. */
.thread-card {
  flex-direction: column;
  align-items: stretch;
  width: 100%;
  touch-action: pan-y;
}

/* Opening the thread and reviewing its work have their own rows. */
.thread-card :deep(.dou-chat-artifact__main) {
  align-items: flex-start;
}

.thread-card :deep(.dou-chat-artifact__title) {
  overflow-wrap: anywhere;
  white-space: normal;
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
