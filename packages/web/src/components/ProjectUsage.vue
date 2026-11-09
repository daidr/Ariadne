<script setup lang="ts">
import { DouText } from 'dou-ui'
import { computed } from 'vue'
import { count, locale, t } from '../i18n'
import { openThread, state } from '../store'
import UsageBreakdown from './UsageBreakdown.vue'
import { costText } from '../cost'

/** What the open project has used so far, and which conversations used the most. */
const usage = computed(
  () =>
    state.detail?.project.usage ?? {
      input: 0,
      output: 0,
      cached: 0,
      cacheWrite: 0,
      reasoning: 0,
      cost: { USD: 0, CNY: 0 },
    },
)
const cost = computed(() => (state.showCost ? costText(usage.value.cost) : null))
const total = computed(() => usage.value.input + usage.value.output)
const percent = (part: number, whole: number) =>
  whole
    ? new Intl.NumberFormat(locale.value, { style: 'percent', maximumFractionDigits: 0 }).format(
        part / whole,
      )
    : '—'

/** The conversations that used the most, the coordinator's among them. */
const top = computed(() => {
  const threads = state.detail?.threads ?? []
  const used = threads.map((thread) => ({
    id: thread.id as string | null,
    name: thread.title,
    tokens: thread.usage.input + thread.usage.output,
  }))
  const own = state.coordinator.conversation?.usage
  const coordinator = own ? own.input + own.output : 0
  return [{ id: null, name: t('coordinator.title'), tokens: coordinator }, ...used]
    .filter((row) => row.tokens > 0)
    .toSorted((a, b) => b.tokens - a.tokens)
    .slice(0, 5)
})

function go(id: string | null) {
  if (!id) return
  state.projectSettingsOpen = false
  openThread(id)
}
</script>

<template>
  <div v-if="state.detail" class="usage">
    <ul class="usage-rows">
      <li>
        <span>{{ t('usage.threads') }}</span>
        <span class="usage-value">{{ state.detail.threads.length }}</span>
      </li>
      <li>
        <span>{{ t('usage.total') }}</span>
        <span class="usage-value">{{ count(total) }}</span>
      </li>
      <li v-if="cost">
        <span>{{ t('usage.cost') }}</span>
        <span class="usage-value">{{ cost }}</span>
      </li>
      <li>
        <span>{{ t('usage.hitRate') }}</span>
        <span class="usage-value">{{ percent(usage.cached, usage.input) }}</span>
      </li>
    </ul>

    <template v-if="total">
      <DouText variant="tertiary" size="sm" class="usage-heading">
        {{ t('usage.breakdown') }}
      </DouText>
      <UsageBreakdown :usage="usage" />

      <DouText variant="tertiary" size="sm" class="usage-heading">
        {{ t('usage.top') }}
      </DouText>
      <ul class="usage-rows">
        <li
          v-for="row in top"
          :key="row.id ?? ''"
          :class="{ 'usage-link': row.id }"
          :role="row.id ? 'button' : undefined"
          :tabindex="row.id ? 0 : undefined"
          @click="go(row.id)"
          @keydown.enter.prevent="go(row.id)"
          @keydown.space.prevent="go(row.id)"
        >
          <DouText truncate class="usage-name">{{ row.name }}</DouText>
          <span class="usage-value">{{ count(row.tokens) }}</span>
          <span class="usage-share">{{ percent(row.tokens, total) }}</span>
        </li>
      </ul>
    </template>
  </div>
</template>

<style scoped>
.usage {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.usage-heading {
  margin-top: 8px;
}

.usage-rows {
  display: flex;
  flex-direction: column;
  margin: 0;
  padding: 0;
  list-style: none;
}

.usage-rows li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 4px 0;
}

.usage-value,
.usage-share {
  font-variant-numeric: tabular-nums;
}

.usage-link {
  cursor: pointer;
  border-radius: var(--dou-radius-control);
}

.usage-link:focus-visible {
  outline: 2px solid var(--dou-color-primary);
  outline-offset: 2px;
}

.usage-link:hover .usage-name,
.usage-link:focus-visible .usage-name {
  color: var(--dou-color-primary-text);
}

.usage-name {
  flex: 1;
  min-width: 0;
}

.usage-share {
  width: 36px;
  text-align: end;
  color: var(--dou-color-text-tertiary);
}
</style>
