<script setup lang="ts">
import type { LiveCommand as Command } from '@ariadne/core'
import { DouShimmer, DouStatusDot, DouText } from 'dou-ui'
import { t } from '../i18n'
import LiveCommand from './LiveCommand.vue'
import WaitStatus from './WaitStatus.vue'

defineProps<{
  conversationId: string | null
  working: boolean
  queued: boolean
  live: Command | null
  activity: string
  elapsed: string
  fallback: string | null
  escArmed: boolean
  wait: { id: string; until: number; note: string } | null
}>()
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
      <DouText
        v-else-if="fallback"
        variant="tertiary"
        size="sm"
        class="activity-extra"
        :title="t('conversation.fallback', { model: fallback })"
      >
        {{ t('conversation.fallback', { model: fallback }) }}
      </DouText>
    </div>
    <DouText v-else-if="queued" variant="secondary" size="sm" role="status">
      {{ t('conversation.queued') }}
    </DouText>
    <WaitStatus v-else-if="wait && conversationId" :conversation-id="conversationId" :wait="wait" />
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
</style>
