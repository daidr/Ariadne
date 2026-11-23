<script setup lang="ts">
import type { LiveCommand as Command, ThreadView } from '@ariadne/core'
import { DouButton, DouShimmer, DouStatusDot, DouText } from 'dou-ui'
import { computed } from 'vue'
import { t } from '../i18n'
import { openThread, state } from '../store'
import LiveCommand from './LiveCommand.vue'
import WaitStatus from './WaitStatus.vue'

const props = defineProps<{
  conversationId: string | null
  working: boolean
  queued: boolean
  live: Command | null
  activity: string
  elapsed: string
  escArmed: boolean
  wait: { id: string; until: number; note: string } | null
  reviewing?: ThreadView['review'] | null
}>()

const reviewingText = computed(() =>
  props.reviewing?.reviewing ? t('review.underway', { round: props.reviewing.rounds }) : '',
)
const reviewerId = computed(() => {
  const id = props.reviewing?.reviewing ? props.reviewing.reviewerId : null
  return id && state.detail?.threads.some((item) => item.id === id) ? id : null
})
</script>

<template>
  <!-- Always occupies one line, including between turns. -->
  <div class="conversation-activity-slot" data-conversation-activity>
    <div v-if="working" class="activity-row" role="status">
      <LiveCommand v-if="live" :command="live" class="activity-main" />
      <DouText v-else variant="secondary" size="sm" class="activity-main">
        <DouStatusDot tone="primary" ripple />
        <DouShimmer class="activity-text" :title="activity">{{ activity }}</DouShimmer>
        <span class="activity-time">{{ elapsed }}</span>
      </DouText>
      <DouText v-if="escArmed" variant="tertiary" size="sm" class="activity-extra">
        {{ t('conversation.escAgain') }}
      </DouText>
    </div>
    <DouText v-else-if="queued" variant="secondary" size="sm" role="status">
      {{ t('conversation.queued') }}
    </DouText>
    <WaitStatus v-else-if="wait && conversationId" :conversation-id="conversationId" :wait="wait" />
    <div v-else-if="reviewing?.reviewing" class="activity-row" role="status">
      <DouText variant="secondary" size="sm" class="activity-main">
        <DouStatusDot tone="primary" ripple />
        <DouShimmer class="activity-text" :title="reviewingText">{{ reviewingText }}</DouShimmer>
      </DouText>
      <DouButton
        v-if="reviewerId"
        variant="ghost"
        size="sm"
        class="activity-action"
        @click="openThread(reviewerId)"
      >
        {{ t('review.openReviewer') }}
      </DouButton>
    </div>
  </div>
</template>

<style scoped>
.conversation-activity-slot {
  height: 32px;
  flex: none;
  min-width: 0;
  display: flex;
  align-items: center;
  margin-top: -16px;
}
.activity-row {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-width: 0;
}
.activity-main {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  flex: 1;
}
.activity-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.activity-time {
  flex: none;
  color: var(--dou-color-text-tertiary);
  font-variant-numeric: tabular-nums;
}
.activity-extra {
  max-width: 40%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.activity-action {
  flex: none;
}
</style>
