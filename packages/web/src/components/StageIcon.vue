<script setup lang="ts">
import type { ThreadStage } from '@ariadne/core'
import { DouSpinner, DouTooltip } from 'dou-ui'
import {
  AlertCircleIcon,
  CheckCircleIcon,
  CircleDashedIcon,
  ClockIcon,
  EyeIcon,
  HelpCircleIcon,
  PauseCircleIcon,
} from '@dou-ui/icons'
import type { Component } from 'vue'
import { computed } from 'vue'
import { t } from '../i18n'
import { stageTone } from '../stage'

/** A thread's stage as an icon, named on hover: working spins, the rest take its tone. */
const props = defineProps<{ stage: ThreadStage }>()

const icons: Record<Exclude<ThreadStage, 'working'>, Component> = {
  answer: HelpCircleIcon,
  review: EyeIcon,
  blocked: AlertCircleIcon,
  stopped: PauseCircleIcon,
  queued: ClockIcon,
  idle: CircleDashedIcon,
  done: CheckCircleIcon,
}
const label = computed(() => t(`thread.stage.${props.stage}`))
const tone = computed(() => (props.stage === 'working' ? 'neutral' : stageTone[props.stage]))
</script>

<template>
  <DouTooltip :content="label">
    <span :class="['stage-icon', `stage-icon--${tone}`]" role="img" :aria-label="label">
      <DouSpinner v-if="stage === 'working'" size="sm" aria-hidden="true" />
      <component :is="icons[stage]" v-else aria-hidden="true" />
    </span>
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
  color: var(--dou-color-text-tertiary);
}

.stage-icon--primary {
  color: var(--dou-color-primary-text);
}

.stage-icon--warning {
  color: var(--dou-color-warning-text);
}

.stage-icon--danger {
  color: var(--dou-color-danger-text);
}

.stage-icon--success {
  color: var(--dou-color-success-text);
}
</style>
