<script setup lang="ts">
import { DouChatArtifact } from 'dou-ui'
import { MessagesIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import StageIcon from '../components/StageIcon.vue'
import { showsHeadline } from '../stage'
import { openThread, state } from '../store'

/** An entry to a thread an agent put in a message; follows the thread as it moves on. */
const props = defineProps<{ threadId: string }>()

const thread = computed(
  () => state.detail?.threads.find((item) => item.id === props.threadId) ?? null,
)
// Once it works again, what it last asked of the user no longer holds; a finished thread
// keeps its last word.
const headline = computed(() => {
  const current = thread.value
  if (!current?.lastHeadline) return undefined
  return showsHeadline(current.stage) || current.stage === 'done' ? current.lastHeadline : undefined
})
</script>

<template>
  <DouChatArtifact
    v-if="thread"
    :title="thread.title"
    :description="headline"
    :active="state.threadId === thread.id"
    @open="openThread(thread.id)"
  >
    <template #icon><MessagesIcon /></template>
    <template #actions><StageIcon :stage="thread.stage" /></template>
  </DouChatArtifact>
</template>
