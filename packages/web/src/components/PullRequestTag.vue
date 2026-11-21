<script setup lang="ts">
import { computed } from 'vue'
import PullRequestLink from '../markdown/PullRequestLink.vue'
import {
  pullRequestLabel,
  trackedPullRequestForThread,
  type PullRequestThread,
} from '../markdown/tracked-pull-requests'

const props = defineProps<{ thread: PullRequestThread | null | undefined; pill?: boolean }>()
const tracked = computed(() => {
  const thread = props.thread
  if (!thread?.pullRequest || thread.pullRequest.headSha) return thread
  return trackedPullRequestForThread(thread.id) ?? thread
})
const pr = computed(() => tracked.value?.pullRequest)
</script>

<template>
  <PullRequestLink
    v-if="pr"
    :href="pr.url"
    :label="pullRequestLabel(pr)"
    :tracked="tracked ?? undefined"
    :variant="pill ? 'pill' : 'text'"
  />
</template>
