<script setup lang="ts">
import type { UsageTotals } from '@ariadne/core'
import {
  compactNumber,
  contextUsage,
  DouButton,
  DouChatContextMeter,
  DouPopover,
  DouTooltip,
} from 'dou-ui'
import { computed, ref } from 'vue'
import { count, locale, t } from '../i18n'
import UsageBreakdown from './UsageBreakdown.vue'
import { costText } from '../cost'
import { state } from '../store'

/**
 * How full one conversation's context is, as a ring by its input: its numbers on hover,
 * and on click what the conversation used and how often its history was summarised.
 * `total` is where older history is made room for, so the ring is full there.
 */
const props = defineProps<{
  used: number
  total: number
  compactions: number
  usage: UsageTotals
}>()

const open = ref(false)
const meter = computed(() => contextUsage(props.used, props.total))
// The ring fills up to where older history is summarised, not to the model's window: the
// hint names that line, so the number does not read as a wrong context size.
const line = computed(() =>
  t('context.untilCompact', {
    used: compactNumber(meter.value.used),
    total: compactNumber(meter.value.total),
  }),
)
// In the popover, written like the rest of its numbers.
const amount = computed(() =>
  t('context.amount', {
    used: count(meter.value.used),
    total: count(meter.value.total),
    percent: meter.value.percent,
  }),
)
const hint = computed(() =>
  props.compactions
    ? `${line.value} · ${t('context.compacted', { count: props.compactions })}`
    : line.value,
)
const tokens = computed(() => props.usage.input + props.usage.output)
const cost = computed(() => (state.showCost ? costText(props.usage.cost) : null))
const hitRate = computed(() =>
  props.usage.input
    ? new Intl.NumberFormat(locale.value, { style: 'percent', maximumFractionDigits: 0 }).format(
        props.usage.cached / props.usage.input,
      )
    : '—',
)
</script>

<template>
  <DouTooltip :content="hint" :disabled="open">
    <DouPopover v-model:open="open" :title="t('context.title')" :width="320" placement="top-start">
      <DouButton variant="ghost" size="sm" :aria-label="hint">
        <DouChatContextMeter :used="used" :total="total" />
      </DouButton>
      <template #content>
        <div class="context">
          <ul class="context-rows">
            <li>
              <span>{{ t('context.window') }}</span>
              <span class="context-value">{{ amount }}</span>
            </li>
            <li>
              <span>{{ t('context.compactions') }}</span>
              <span class="context-value">{{ t('context.times', { count: compactions }) }}</span>
            </li>
            <li>
              <span>{{ t('usage.total') }}</span>
              <span class="context-value">{{ count(tokens) }}</span>
            </li>
            <li v-if="cost">
              <span>{{ t('usage.cost') }}</span>
              <span class="context-value">{{ cost }}</span>
            </li>
            <li>
              <span>{{ t('usage.hitRate') }}</span>
              <span class="context-value">{{ hitRate }}</span>
            </li>
          </ul>
          <UsageBreakdown v-if="tokens" :usage="usage" />
        </div>
      </template>
    </DouPopover>
  </DouTooltip>
</template>

<style scoped>
.context {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.context-rows {
  display: flex;
  flex-direction: column;
  margin: 0;
  padding: 0;
  list-style: none;
}

.context-rows li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 4px 0;
}

.context-value {
  font-variant-numeric: tabular-nums;
}
</style>
