<script setup lang="ts">
import type { ThreadPullRequest } from '@ariadne/core'
import { DouLink, DouTag } from 'dou-ui'
import { GitMergeIcon, GitPullRequestClosedIcon, GitPullRequestIcon } from '@dou-ui/icons'
import { t } from '../i18n'
import { checksTone } from '../stage'

// A plan node thread's pull request: its number, and how it or its checks stand.
defineProps<{
  pullRequest: Pick<ThreadPullRequest, 'number' | 'url' | 'state' | 'checks'>
}>()
</script>

<template>
  <span class="plan-pr">
    <DouLink :href="pullRequest.url" external>
      <GitMergeIcon v-if="pullRequest.state === 'merged'" class="plan-pr-icon" />
      <GitPullRequestClosedIcon v-else-if="pullRequest.state === 'closed'" class="plan-pr-icon" />
      <GitPullRequestIcon v-else class="plan-pr-icon" />
      #{{ pullRequest.number }}
    </DouLink>
    <DouTag v-if="pullRequest.state !== 'open'" size="sm">
      {{ t(`pr.state.${pullRequest.state}`) }}
    </DouTag>
    <DouTag
      v-else-if="pullRequest.checks.state !== 'none'"
      size="sm"
      :tone="checksTone(pullRequest.checks.state)"
      :title="pullRequest.checks.failed.join(', ')"
    >
      {{ t(`pr.checks.${pullRequest.checks.state}`) }}
    </DouTag>
  </span>
</template>

<style scoped>
.plan-pr {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.plan-pr-icon {
  vertical-align: -2px;
  margin-inline-end: 2px;
}
</style>
