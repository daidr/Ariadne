<script setup lang="ts">
import type { PlanNodeStatus } from '@ariadne/core'
import { DouStatusDot, DouText, DouTooltip } from 'dou-ui'
import {
  AlertCircleIcon,
  CheckCircleIcon,
  CircleDashedIcon,
  PauseCircleIcon,
  UserCircleIcon,
} from '@dou-ui/icons'
import type { Component } from 'vue'
import { computed } from 'vue'
import { t } from '../i18n'

/** A plan node's status as an icon in its tone, named on hover. */
const props = defineProps<{ status: PlanNodeStatus }>()

const icons: Record<Exclude<PlanNodeStatus, 'running'>, Component> = {
  none: CircleDashedIcon,
  waiting: UserCircleIcon,
  error: AlertCircleIcon,
  paused: PauseCircleIcon,
  done: CheckCircleIcon,
}
const tones = {
  none: 'neutral',
  running: 'primary',
  waiting: 'warning',
  error: 'danger',
  paused: 'neutral',
  done: 'success',
} as const
const tone = computed(() => tones[props.status])
const label = computed(() =>
  props.status === 'waiting' ? t('plan.waiting') : t(`plan.status.${props.status}`),
)
</script>

<template>
  <DouTooltip :content="label">
    <DouText
      :tone="status === 'running' ? 'neutral' : tone"
      :variant="tone === 'neutral' ? 'tertiary' : 'default'"
      class="plan-status"
      role="img"
      :aria-label="label"
    >
      <!-- Under way reads the same as a working thread: a ripple, not a half-filled circle. -->
      <DouStatusDot v-if="status === 'running'" tone="primary" ripple aria-hidden="true" />
      <component :is="icons[status]" v-else aria-hidden="true" />
    </DouText>
  </DouTooltip>
</template>

<style scoped>
.plan-status {
  display: inline-flex;
  flex: none;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  font-size: 16px;
}
</style>
