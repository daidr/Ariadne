<script setup lang="ts">
import { DouChatArtifact } from 'dou-ui'
import { MessagesIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import ReviewActions from '../components/ReviewActions.vue'
import StageIcon from '../components/StageIcon.vue'
import { t } from '../i18n'
import ReviewMark from '../review/ReviewMark.vue'
import { threadLine } from '../stage'
import { activeThreadId, openThread, state } from '../store'

/** An entry to a thread an agent put in a message; follows the thread as it moves on. */
const props = defineProps<{ threadId: string }>()

const thread = computed(
  () => state.detail?.threads.find((item) => item.id === props.threadId) ?? null,
)
const headline = computed(() => {
  if (!thread.value) return undefined
  const { review } = thread.value
  if (review?.reviewing) return t('review.underway', { round: review.rounds })
  return threadLine(thread.value) ?? undefined
})
</script>

<template>
  <DouChatArtifact
    v-if="thread"
    :title="thread.title"
    :description="headline"
    :active="activeThreadId === thread.id"
    @open="openThread(thread.id)"
  >
    <template #icon><MessagesIcon /></template>
    <template #actions>
      <ReviewActions v-if="thread.stage === 'review'" :thread-id="thread.id" />
      <ReviewMark :thread="thread" />
      <StageIcon :stage="thread.stage" />
    </template>
  </DouChatArtifact>
</template>
