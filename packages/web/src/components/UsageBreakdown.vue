<script setup lang="ts">
import type { UsageTotals } from '@ariadne/core'
import { DouProgressGroup, DouText, type ProgressGroupItem } from 'dou-ui'
import { computed } from 'vue'
import { count, t } from '../i18n'

/** Tokens used, as one bar: what the model read, split by how the cache served it, then what it wrote. */
const props = defineProps<{ usage: UsageTotals }>()

const parts = computed<(ProgressGroupItem & { label: string; tone: string })[]>(() => {
  const { input, output, cached, cacheWrite } = props.usage
  return [
    { label: t('usage.input'), value: Math.max(0, input - cached - cacheWrite), tone: 'primary' },
    { label: t('usage.output'), value: output, tone: 'danger' },
    { label: t('usage.cacheWrite'), value: cacheWrite, tone: 'success' },
    { label: t('usage.cacheRead'), value: cached, tone: 'warning' },
  ]
})
</script>

<template>
  <DouProgressGroup
    :items="parts"
    :label="t('usage.breakdown')"
    :format="(value) => count(value)"
    legend
    legend-direction="vertical"
    tooltip
  >
    <!-- Thinking is part of the output, so it is named with it. -->
    <template #legend="{ item, text }">
      <span>{{ item.label }}</span>
      <DouText variant="secondary" size="sm" class="usage-value">
        {{ text }}
        <template v-if="item.label === t('usage.output') && usage.reasoning">
          · {{ t('usage.reasoning') }} {{ count(usage.reasoning) }}
        </template>
      </DouText>
    </template>
  </DouProgressGroup>
</template>

<style scoped>
.usage-value {
  font-variant-numeric: tabular-nums;
}
</style>
