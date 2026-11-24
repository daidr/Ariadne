<script setup lang="ts">
import type { ShowBlock, StepStatus as BlockStepStatus } from '@ariadne/core'
import { DouSteps, DouText, type StepItem, type StepStatus } from 'dou-ui'
import { computed, defineAsyncComponent } from 'vue'
import ConnectorCard from './ConnectorCard.vue'
import ThreadCard from './ThreadCard.vue'

/** A table, chart, steps or thread entry an agent put in a message; checked on the server. */
const props = defineProps<{ block: ShowBlock }>()

// Charts and tables pull heavy visualization/grid bundles; load them only when a block uses them.
const ShowChart = defineAsyncComponent(() => import('./ShowChart.vue'))
const ShowTable = defineAsyncComponent(() => import('./ShowTable.vue'))

const stepStatus: Record<BlockStepStatus, StepStatus> = {
  done: 'done',
  current: 'running',
  pending: 'pending',
  failed: 'error',
}
const steps = computed<StepItem[]>(() =>
  props.block.type === 'steps'
    ? props.block.items.map((item) => ({
        title: item.title,
        ...(item.description ? { description: item.description } : {}),
        status: stepStatus[item.status],
      }))
    : [],
)
</script>

<template>
  <ThreadCard v-if="block.type === 'thread'" :thread-id="block.threadId" />
  <ConnectorCard v-else-if="block.type === 'connector'" :block="block" />
  <div v-else class="show-block">
    <DouText v-if="block.title" weight="medium" size="sm">{{ block.title }}</DouText>
    <ShowTable v-if="block.type === 'table'" :block="block" />
    <ShowChart v-else-if="block.type === 'chart'" :block="block" />
    <DouSteps v-else :items="steps" direction="vertical" size="sm" />
  </div>
</template>

<style scoped>
.show-block {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}
</style>
