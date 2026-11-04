<script setup lang="ts">
import type { ThreadView } from '@ariadne/core'
import { DouProgress, DouRelativeTime, DouText } from 'dou-ui'
import { GitMergeIcon, GitPullRequestClosedIcon, GitPullRequestIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'
import { checksTone, threadLine } from '../stage'
import ReviewActions from './ReviewActions.vue'
import StageIcon from './StageIcon.vue'

/** One thread in the thread list: its status, title, pull request, todos and last word. */
const props = defineProps<{ thread: ThreadView }>()

const line = computed(() => threadLine(props.thread))
const prTone = computed(() =>
  props.thread.pullRequest?.state === 'open'
    ? checksTone(props.thread.pullRequest.checks.state)
    : 'neutral',
)
</script>

<template>
  <span class="row">
    <span class="row-head">
      <StageIcon :stage="thread.stage" />
      <DouText truncate class="row-title">{{ thread.title }}</DouText>
      <DouText
        v-if="thread.pullRequest"
        size="sm"
        :tone="prTone"
        :variant="prTone === 'neutral' ? 'tertiary' : 'default'"
        class="row-mark"
        :title="thread.pullRequest.title"
      >
        <GitMergeIcon v-if="thread.pullRequest.state === 'merged'" aria-hidden="true" />
        <GitPullRequestClosedIcon
          v-else-if="thread.pullRequest.state === 'closed'"
          aria-hidden="true"
        />
        <GitPullRequestIcon v-else aria-hidden="true" />
        {{ thread.pullRequest.reference ?? `#${thread.pullRequest.number}` }}
      </DouText>
      <DouText
        v-if="thread.progress && thread.stage !== 'done'"
        size="sm"
        variant="tertiary"
        class="row-mark"
      >
        <DouProgress
          type="circle"
          :value="thread.progress.done"
          :max="thread.progress.total"
          :width="12"
          :stroke-width="2"
          color="var(--dou-color-text-tertiary)"
          :label="t('threads.progress')"
        />
        {{ thread.progress.done }}/{{ thread.progress.total }}
      </DouText>
      <DouText size="sm" variant="tertiary" class="row-time">
        <DouRelativeTime :value="thread.activeAt" short />
      </DouText>
    </span>
    <span v-if="line || thread.stage === 'review'" class="row-foot">
      <DouText size="sm" variant="secondary" truncate class="row-line" :title="line ?? undefined">
        {{ line }}
      </DouText>
      <ReviewActions v-if="thread.stage === 'review'" :thread-id="thread.id" />
    </span>
  </span>
</template>

<style scoped>
.row {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  padding: 7px 0;
  line-height: normal;
  white-space: normal;
}

.row-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.row-title {
  flex: 1;
  min-width: 0;
}

.row-mark {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 3px;
  white-space: nowrap;
}

.row-time {
  flex: none;
  white-space: nowrap;
}

/* The activity line starts under the title, past the status icon. */
.row-foot {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  padding-inline-start: 24px;
}

.row-line {
  flex: 1;
  min-width: 0;
}
</style>
