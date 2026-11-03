<script setup lang="ts">
import { DouButton, DouLink, DouTag } from 'dou-ui'
import type { ThreadView } from '@ariadne/core'
import {
  GitMergeIcon,
  GitPullRequestClosedIcon,
  GitPullRequestIcon,
  RefreshIcon,
} from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { t } from '../i18n'
import { refreshPullRequest } from '../store'

// A thread's pull request with how its checks stand; "refresh" asks the repository now.
const props = defineProps<{ thread: ThreadView; refreshable?: boolean }>()
const pr = computed(() => props.thread.pullRequest)
const refreshing = ref(false)

const checksTone = computed(() => {
  switch (pr.value?.checks.state) {
    case 'success':
      return 'success'
    case 'failure':
      return 'danger'
    case 'pending':
      return 'warning'
    default:
      return 'neutral'
  }
})

async function refresh() {
  refreshing.value = true
  await refreshPullRequest(props.thread.id)
  refreshing.value = false
}
</script>

<template>
  <span v-if="pr" class="pr">
    <DouLink :href="pr.url" external :title="pr.title">
      <GitMergeIcon v-if="pr.state === 'merged'" class="pr-icon" />
      <GitPullRequestClosedIcon v-else-if="pr.state === 'closed'" class="pr-icon" />
      <GitPullRequestIcon v-else class="pr-icon" />
      #{{ pr.number }}
    </DouLink>
    <DouTag v-if="pr.state !== 'open'" size="sm">{{ t(`pr.state.${pr.state}`) }}</DouTag>
    <DouTag
      v-else-if="pr.checks.state !== 'none'"
      size="sm"
      :tone="checksTone"
      :title="pr.checks.failed.join(', ')"
    >
      {{ t(`pr.checks.${pr.checks.state}`) }}
    </DouTag>
    <DouButton
      v-if="refreshable && pr.state === 'open'"
      size="sm"
      variant="ghost"
      :loading="refreshing"
      :aria-label="t('pr.refresh')"
      :title="t('pr.refresh')"
      @click="refresh"
    >
      <RefreshIcon />
    </DouButton>
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
