<script setup lang="ts">
import type { ThreadPullRequest, ThreadStage } from '@ariadne/core'
import { DouStatusDot, DouText, DouTooltip } from 'dou-ui'
import {
  AlertCircleIcon,
  CheckCircleIcon,
  CircleDashedIcon,
  ClockIcon,
  EyeIcon,
  HelpCircleIcon,
  MessageOffIcon,
  PauseCircleIcon,
  StopCircleIcon,
} from '@dou-ui/icons'
import type { Component } from 'vue'
import { computed } from 'vue'
import { stageTone, threadStageLabel } from '../stage'

/** A thread's stage as an icon, named on hover: working ripples, the rest take its tone. */
const props = defineProps<{ stage: ThreadStage; pullRequestState?: ThreadPullRequest['state'] }>()

const icons: Record<Exclude<ThreadStage, 'working'>, Component> = {
  answer: HelpCircleIcon,
  review: EyeIcon,
  blocked: AlertCircleIcon,
  stopped: PauseCircleIcon,
  halted: StopCircleIcon,
  silent: MessageOffIcon,
  queued: ClockIcon,
  idle: CircleDashedIcon,
  done: CheckCircleIcon,
}
const label = computed(() => threadStageLabel(props.stage, props.pullRequestState))
// The same tint as the stage's word, which the icon takes through currentColor.
const tone = computed(() => (props.stage === 'working' ? 'neutral' : stageTone[props.stage]))
</script>

<template>
  <DouTooltip :content="label">
    <DouText
      :tone="tone"
      :variant="tone === 'neutral' ? 'tertiary' : 'default'"
      class="stage-icon"
      role="img"
      :aria-label="label"
    >
      <DouStatusDot v-if="stage === 'working'" tone="primary" ripple aria-hidden="true" />
      <component :is="icons[stage]" v-else aria-hidden="true" />
    </DouText>
  </DouTooltip>
</template>

<style scoped>
.stage-icon {
  display: inline-flex;
  flex: none;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  font-size: 16px;
}
</style>
