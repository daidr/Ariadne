<script setup lang="ts">
import type { PlanNodeStatus } from '@ariadne/core'
import { DouText, DouTooltip } from 'dou-ui'
import {
  AlertCircleIcon,
  CheckCircleIcon,
  CircleDashedIcon,
  CircleHalfIcon,
  PauseCircleIcon,
  UserCircleIcon,
} from '@dou-ui/icons'
import type { Component } from 'vue'
import { computed } from 'vue'
import { t } from '../i18n'

/** A plan node's status as an icon in its tone, named on hover. */
const props = defineProps<{ status: PlanNodeStatus }>()

const icons: Record<PlanNodeStatus, Component> = {
  none: CircleDashedIcon,
  running: CircleHalfIcon,
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
      :tone="tone"
      :variant="tone === 'neutral' ? 'tertiary' : 'default'"
      class="plan-status"
      role="img"
      :aria-label="label"
    >
      <component :is="icons[status]" aria-hidden="true" />
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
