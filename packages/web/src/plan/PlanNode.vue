<script setup lang="ts">
import type { PlanNodeKind, PlanNodeStatus } from '@ariadne/core'
import { DouTag, DouText } from 'dou-ui'
import { t } from '../i18n'

defineProps<{
  data: {
    title: string
    kind: PlanNodeKind
    status: PlanNodeStatus
    threads: number
    changed: boolean
  }
}>()

const tone = {
  none: 'neutral',
  process: 'primary',
  wait: 'warning',
  error: 'danger',
  finish: 'success',
} as const
</script>

<template>
  <div class="plan-node" :class="{ 'plan-node-changed': data.changed }">
    <div class="plan-node-head">
      <DouText size="sm" variant="tertiary">{{ t(`plan.kind.${data.kind}`) }}</DouText>
      <DouTag size="sm" :tone="tone[data.status]">{{ t(`plan.status.${data.status}`) }}</DouTag>
    </div>
    <DouText class="plan-node-title" weight="medium">{{ data.title }}</DouText>
  </div>
</template>

<style scoped>
.plan-node {
  display: flex;
  flex-direction: column;
  gap: 4px;
  width: 200px;
  padding: 8px 12px 10px;
  border-radius: var(--dou-radius-container);
}

/* What the coordinator just changed: easy to spot, and the banner offers to revert it. */
.plan-node-changed {
  outline: 2px solid var(--dou-color-warning);
  outline-offset: 2px;
}

.plan-node-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.plan-node-title {
  overflow-wrap: anywhere;
}
</style>
