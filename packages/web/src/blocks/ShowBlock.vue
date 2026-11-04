<script setup lang="ts">
import type { ShowBlock, StepStatus as BlockStepStatus } from '@ariadne/core'
import { DouSteps, DouTable, DouText, type StepItem, type StepStatus } from 'dou-ui'
import {
  DouChart,
  DouChartArea,
  DouChartBar,
  DouChartLine,
  DouChartPie,
  DouChartTooltip,
} from '@dou-ui/chart'
import '@dou-ui/chart/style.css'
import { computed } from 'vue'
import ThreadCard from './ThreadCard.vue'

/** A table, chart, steps or thread entry an agent put in a message; checked on the server. */
const props = defineProps<{ block: ShowBlock }>()

const stepStatus: Record<BlockStepStatus, StepStatus> = {
  done: 'finish',
  current: 'process',
  pending: 'wait',
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

const nameOf = (key: string) =>
  props.block.type === 'chart' ? (props.block.labels[key] ?? key) : key
</script>

<template>
  <ThreadCard v-if="block.type === 'thread'" :thread-id="block.threadId" />
  <div v-else class="show-block">
    <DouText v-if="block.title" weight="medium" size="sm">{{ block.title }}</DouText>
    <DouTable
      v-if="block.type === 'table'"
      :data="block.rows"
      :columns="block.columns"
      size="sm"
      :max-height="360"
    />
    <DouChart
      v-else-if="block.type === 'chart'"
      :data="block.rows"
      :x="block.x"
      :height="220"
      :horizontal="block.horizontal"
      :legend="block.kind === 'pie' || block.y.length > 1"
      :aria-label="block.title ?? undefined"
    >
      <DouChartPie
        v-if="block.kind === 'pie'"
        :value="block.y[0]!"
        :name="nameOf(block.y[0]!)"
        :inner-radius="0.6"
      />
      <template v-for="y in block.y" v-else :key="y">
        <DouChartBar
          v-if="block.kind === 'bar'"
          :y="y"
          :name="nameOf(y)"
          :stack="block.stacked || undefined"
        />
        <DouChartArea
          v-else-if="block.kind === 'area'"
          :y="y"
          :name="nameOf(y)"
          :stack="block.stacked || undefined"
        />
        <DouChartLine v-else :y="y" :name="nameOf(y)" />
      </template>
      <DouChartTooltip />
    </DouChart>
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
