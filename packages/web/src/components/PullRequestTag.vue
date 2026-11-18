<script setup lang="ts">
import { DouLink, DouTag } from 'dou-ui'
import type { ThreadView } from '@ariadne/core'
import { GitMergeIcon, GitPullRequestClosedIcon, GitPullRequestIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'
import { checksTone } from '../stage'

// Pull request checks are refreshed by the repository watcher.
const props = defineProps<{ thread: ThreadView }>()
const pr = computed(() => props.thread.pullRequest)

const tone = computed(() => checksTone(pr.value?.checks.state))
</script>

<template>
  <span v-if="pr" class="pr">
    <DouLink :href="pr.url" external :title="pr.title">
      <GitMergeIcon v-if="pr.state === 'merged'" class="pr-icon" />
      <GitPullRequestClosedIcon v-else-if="pr.state === 'closed'" class="pr-icon" />
      <GitPullRequestIcon v-else class="pr-icon" />
      {{ pr.reference ?? `#${pr.number}` }}
    </DouLink>
    <DouTag v-if="pr.state !== 'open'" size="sm">{{ t(`pr.state.${pr.state}`) }}</DouTag>
    <DouTag
      v-else-if="pr.checks.state !== 'none'"
      size="sm"
      :tone="tone"
      :title="pr.checks.failed.join(', ')"
    >
      {{ t(`pr.checks.${pr.checks.state}`) }}
    </DouTag>
  </span>
</template>

<style scoped>
.pr {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.pr-icon {
  vertical-align: -2px;
  margin-inline-end: 2px;
}
</style>
