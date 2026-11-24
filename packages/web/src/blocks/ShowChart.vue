<script setup lang="ts">
import type { ChartBlock } from '@ariadne/core'
import {
  DouChart,
  DouChartArea,
  DouChartBar,
  DouChartLine,
  DouChartPie,
  DouChartTooltip,
} from '@dou-ui/chart'
import '@dou-ui/chart/style.css'

const props = defineProps<{ block: ChartBlock }>()

const nameOf = (key: string) => props.block.labels[key] ?? key
</script>

<template>
  <DouChart
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
</template>
