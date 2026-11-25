<script setup lang="ts">
import { DouShimmer } from 'dou-ui'
import { MessagesIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import ChangeTotals from '../components/ChangeTotals.vue'
import PullRequestTag from '../components/PullRequestTag.vue'
import StageIcon from '../components/StageIcon.vue'
import { t } from '../i18n'
import ReviewMark from '../review/ReviewMark.vue'
import { branchChanges } from '../review/merge-offered'
import { questionOf, statusLine } from '../stage'
import { activeThreadId, openThreadFrom, state, threadsById } from '../store'
import { dragThread } from '../side/dock'

/** An entry to a thread an agent put in a message; follows the thread as it moves on. */
const props = defineProps<{ threadId: string }>()

const thread = computed(() => threadsById.value.get(props.threadId) ?? null)
function open(event: MouseEvent) {
  openThreadFrom(event, props.threadId)
}

// What it does now, asks, or delivered, so the card answers "how far is it" by itself.
const line = computed(() =>
  thread.value
    ? statusLine(thread.value, questionOf(thread.value.id, state.detail?.needsMe))
    : null,
)
const changes = computed(() => branchChanges(thread.value))
const hasActions = computed(
  () =>
    !!changes.value ||
    !!thread.value?.pullRequest ||
    (!!thread.value?.review && !thread.value.review.reviewing && thread.value.review.rounds > 0),
)
const description = computed(() => {
  const stage = thread.value?.stage
  return stage && line.value?.text !== t(`thread.stage.${stage}`) ? line.value?.text : undefined
})
const progress = computed(() =>
  thread.value?.stage === 'working' && thread.value.progress
    ? t('thread.progressShort', thread.value.progress)
    : undefined,
)
</script>

<template>
  <div v-if="thread" class="thread-card" :class="{ 'is-active': activeThreadId === thread.id }">
    <button
      type="button"
      class="thread-card__open"
      @click="open"
      @pointerdown="dragThread($event, thread.id)"
    >
      <span class="thread-card__icon" aria-hidden="true"><MessagesIcon /></span>
      <span class="thread-card__text">
        <span class="thread-card__title">{{ thread.title }}</span>
        <span v-if="description || progress" class="thread-card__summary">
          <component
            :is="line?.live ? DouShimmer : 'span'"
            v-if="description"
            class="thread-card__description"
            :title="description"
          >
            {{ description }}
          </component>
          <span v-if="progress" class="thread-card__progress">
            <span v-if="description" aria-hidden="true">· </span>{{ progress }}
          </span>
        </span>
      </span>
      <StageIcon
        class="thread-card__stage"
        :stage="thread.stage"
        :pull-request-state="thread.pullRequest?.state"
      />
    </button>
    <div v-if="hasActions" class="thread-card__actions">
      <PullRequestTag :thread="thread" />
      <ChangeTotals v-if="changes" :totals="changes" :thread-id="thread.id" button />
      <ReviewMark :thread="thread" />
    </div>
  </div>
</template>

<style scoped>
.thread-card {
  width: 100%;
  max-width: 420px;
  border: 1px solid var(--dou-color-border);
  border-radius: var(--dou-radius-container);
  background-color: var(--dou-color-bg-surface);
  color: var(--dou-color-text);
  font-size: var(--dou-font-size-sm);
  line-height: 20px;
  transition:
    border-color var(--dou-motion-duration-fast) var(--dou-motion-easing),
    background-color var(--dou-motion-duration-fast) var(--dou-motion-easing);
}

.thread-card:hover,
.thread-card.is-active {
  border-color: var(--dou-color-border-strong);
}

.thread-card.is-active {
  background-color: color-mix(in oklab, var(--dou-color-text) 5%, var(--dou-color-bg-surface));
}

.thread-card.is-active:hover {
  background-color: color-mix(in oklab, var(--dou-color-text) 8%, var(--dou-color-bg-surface));
}

.thread-card__open {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  width: 100%;
  padding: 10px 12px 10px 10px;
  border: 0;
  border-radius: inherit;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: start;
  touch-action: pan-y;
  cursor: pointer;
}

.thread-card__open:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: 2px;
}

.thread-card__icon {
  display: inline-flex;
  flex: none;
  align-items: center;
  justify-content: center;
  width: 40px;
  height: 40px;
  border-radius: var(--dou-radius-control);
  background-color: var(--dou-color-bg-muted);
  color: var(--dou-color-text-secondary);
  font-size: 20px;
}

.is-active .thread-card__icon {
  background-color: var(--dou-color-bg-surface);
}

.thread-card__text {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
}

.thread-card__title {
  font-weight: var(--dou-font-weight-medium);
  overflow-wrap: anywhere;
}

.thread-card__stage {
  margin-top: 1px;
}

.thread-card__summary {
  display: flex;
  align-items: baseline;
  gap: 4px;
  color: var(--dou-color-text-tertiary);
  font-size: 12px;
  line-height: 18px;
}

.thread-card__description {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.thread-card__progress {
  flex: none;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}

.thread-card__actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 4px 8px;
  padding: 0 12px 10px;
}
</style>
