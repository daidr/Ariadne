<script setup lang="ts">
import type { ThreadView } from '@ariadne/core'
import PullRequestLink from '../markdown/PullRequestLink.vue'

// Pull request checks are refreshed by the repository watcher.
defineProps<{ thread: Pick<ThreadView, 'id' | 'pullRequest'> | undefined; pill?: boolean }>()
</script>

<template>
  <PullRequestLink
    v-if="thread?.pullRequest"
    :href="thread.pullRequest.url"
    :label="thread.pullRequest.reference ?? `#${thread.pullRequest.number}`"
    :tracked="thread"
    :variant="pill ? 'pill' : 'text'"
  />
</template>
