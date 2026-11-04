<script setup lang="ts">
import { DouChatArtifact } from 'dou-ui'
import { MessagesIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import StageLabel from '../components/StageLabel.vue'
import { openThread, state } from '../store'

/** An entry to a thread an agent put in a message; follows the thread as it moves on. */
const props = defineProps<{ threadId: string }>()

const thread = computed(
  () => state.detail?.threads.find((item) => item.id === props.threadId) ?? null,
)
</script>

<template>
  <DouChatArtifact
    v-if="thread"
    :title="thread.title"
    :description="thread.lastSummary ?? undefined"
    :active="state.threadId === thread.id"
    class="thread-card"
    @open="openThread(thread.id)"
  >
    <template #icon><MessagesIcon /></template>
    <template #actions><StageLabel :stage="thread.stage" /></template>
  </DouChatArtifact>
</template>
