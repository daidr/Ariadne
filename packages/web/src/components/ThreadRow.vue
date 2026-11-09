<script setup lang="ts">
import ChangeTotals from './ChangeTotals.vue'
import type { ThreadView } from '@ariadne/core'
import { DouProgress, DouRelativeTime, DouShimmer, DouText, DouTooltip } from 'dou-ui'
import {
  GitMergeIcon,
  GitPullRequestClosedIcon,
  GitPullRequestIcon,
  ScanSearchIcon,
} from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'
import { checksTone, questionOf, statusLine } from '../stage'
import { state } from '../store'
import ReviewMark from '../review/ReviewMark.vue'
import { branchChanges } from '../review/merge-offered'
import StageIcon from './StageIcon.vue'

/**
 * One thread in the thread list: its status, title, pull request, todos and last word.
 * `nested`: a reviewing thread, under its author's row.
 */
const props = defineProps<{ thread: ThreadView; nested?: boolean }>()

const line = computed(() => {
  if (!props.nested) {
    return statusLine(props.thread, questionOf(props.thread.id, state.detail?.needsMe))
  }
  if (props.thread.review?.reviewing) return statusLine(props.thread, null)
  return props.thread.stage === 'done' && props.thread.lastHeadline
    ? { text: props.thread.lastHeadline, live: false }
    : null
})
// Todos all ticked have nothing left to say in a row.
const unfinished = computed(() => {
  const progress = props.thread.progress
  if (!progress || props.thread.stage === 'done' || progress.done >= progress.total) return null
  return progress
})
const prTone = computed(() =>
  props.thread.pullRequest?.state === 'open'
    ? checksTone(props.thread.pullRequest.checks.state)
    : 'neutral',
)
</script>

<template>
  <span class="row" :class="{ 'row-nested': nested }">
    <span class="row-head">
      <!-- A review that ended says nothing of its verdict: no green check for it. -->
      <DouTooltip v-if="nested && thread.stage === 'done'" :content="t('review.ended')">
        <DouText variant="tertiary" class="row-reviewed" role="img" :aria-label="t('review.ended')">
          <ScanSearchIcon aria-hidden="true" />
        </DouText>
      </DouTooltip>
      <StageIcon v-else :stage="thread.stage" />
      <DouText
        truncate
        class="row-title row-trim"
        :size="nested ? 'sm' : undefined"
        :variant="nested ? 'secondary' : undefined"
      >
        {{ nested ? t('review.row') : thread.title }}
      </DouText>
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
        <span class="row-trim">{{
          thread.pullRequest.reference ?? `#${thread.pullRequest.number}`
        }}</span>
      </DouText>
      <ReviewMark :thread="thread" />
      <ChangeTotals
        v-if="branchChanges(thread) && !nested"
        :totals="branchChanges(thread)!"
        :thread-id="thread.id"
        class="row-mark row-trim"
      />
      <DouText v-if="unfinished && !nested" size="sm" variant="tertiary" class="row-mark">
        <DouProgress
          type="circle"
          :value="unfinished.done"
          :max="unfinished.total"
          :width="12"
          :stroke-width="2"
          color="var(--dou-color-text-tertiary)"
          :label="t('threads.progress')"
        />
        <span class="row-trim">{{ unfinished.done }}/{{ unfinished.total }}</span>
      </DouText>
      <DouText size="sm" variant="tertiary" class="row-time row-trim">
        <DouRelativeTime :value="thread.activeAt" short />
      </DouText>
    </span>
    <span v-if="line" class="row-foot">
      <DouText v-if="line.live" size="sm" variant="tertiary" truncate class="row-line">
        <DouShimmer>{{ line.text }}</DouShimmer>
      </DouText>
      <DouText v-else size="sm" variant="secondary" truncate class="row-line" :title="line.text">
        {{ line.text }}
      </DouText>
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

/* Trimmed text is 2px shorter; the padding makes it up, so rows stay the same height. */
@supports (text-box: trim-both cap alphabetic) {
  .row {
    padding-block: 8px;
  }
}

/* A reviewing thread sits under its author, past the author's status icon. */
.row-nested {
  padding-block: 4px;
  padding-inline-start: 24px;
}

@supports (text-box: trim-both cap alphabetic) {
  .row-nested {
    padding-block: 5px;
  }
}

.row-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

/*
 * The status icon centres on the text's box, and CJK and system fonts sit their glyphs
 * low in it, so the icon looked high. Trimmed to cap height and baseline, the box is
 * the letters themselves, as dou-ui does for its own icon and label pairs.
 */
.row-trim {
  text-box: trim-both cap alphabetic;
}

.row-reviewed {
  display: inline-flex;
  flex: none;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  font-size: 16px;
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
