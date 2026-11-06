<script setup lang="ts">
import { DouChatArtifact } from 'dou-ui'
import { MessagesIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import ReviewActions from '../components/ReviewActions.vue'
import StageIcon from '../components/StageIcon.vue'
import { t } from '../i18n'
import ReviewMark from '../review/ReviewMark.vue'
import { questionOf, statusLine } from '../stage'
import { activeThreadId, openThreadFrom, state } from '../store'

/** An entry to a thread an agent put in a message; follows the thread as it moves on. */
const props = defineProps<{ threadId: string }>()

const thread = computed(
  () => state.detail?.threads.find((item) => item.id === props.threadId) ?? null,
)
function open(event: MouseEvent) {
  openThreadFrom(event, props.threadId)
}

// What it does now, asks, or delivered, so the card answers "how far is it" by itself.
const headline = computed(() => {
  if (!thread.value) return undefined
  const line = statusLine(thread.value, questionOf(thread.value.id, state.detail?.needsMe))
  const progress = thread.value.stage === 'working' ? thread.value.progress : null
  if (!line) return progress ? t('thread.progressShort', progress) : undefined
  return progress ? `${line.text} · ${t('thread.progressShort', progress)}` : line.text
})
</script>

<template>
  <DouChatArtifact
    v-if="thread"
    :title="thread.title"
    :description="headline"
    :active="activeThreadId === thread.id"
    @open="open"
  >
    <template #icon><MessagesIcon /></template>
    <template #actions>
      <ReviewActions v-if="thread.stage === 'review'" :thread-id="thread.id" />
      <ReviewMark :thread="thread" />
      <StageIcon :stage="thread.stage" />
    </template>
  </DouChatArtifact>
</template>
