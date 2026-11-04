<script setup lang="ts">
import { DouButton, DouPopover, DouProgressGroup, DouText, type ProgressGroupItem } from 'dou-ui'
import { CoinsIcon } from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { count, locale, t } from '../i18n'
import { openThread, state } from '../store'

const open = ref(false)
const usage = computed(
  () =>
    state.detail?.project.usage ?? { input: 0, output: 0, cached: 0, cacheWrite: 0, reasoning: 0 },
)
const total = computed(() => usage.value.input + usage.value.output)
const percent = (part: number, whole: number) =>
  whole
    ? new Intl.NumberFormat(locale.value, { style: 'percent', maximumFractionDigits: 0 }).format(
        part / whole,
      )
    : '—'

// What the model read, split by how the cache served it, then what it wrote.
const parts = computed<(ProgressGroupItem & { label: string; tone: string })[]>(() => {
  const { input, output, cached, cacheWrite } = usage.value
  return [
    { label: t('usage.input'), value: Math.max(0, input - cached - cacheWrite), tone: 'primary' },
    { label: t('usage.output'), value: output, tone: 'danger' },
    { label: t('usage.cacheWrite'), value: cacheWrite, tone: 'success' },
    { label: t('usage.cacheRead'), value: cached, tone: 'warning' },
  ]
})

/** The conversations that used the most, the coordinator's among them. */
const top = computed(() => {
  const threads = state.detail?.threads ?? []
  const used = threads.map((thread) => ({
    id: thread.id as string | null,
    name: thread.title,
    tokens: thread.usage.input + thread.usage.output,
  }))
  const coordinator = total.value - used.reduce((sum, row) => sum + row.tokens, 0)
  return [{ id: null, name: t('coordinator.title'), tokens: coordinator }, ...used]
    .filter((row) => row.tokens > 0)
    .toSorted((a, b) => b.tokens - a.tokens)
    .slice(0, 5)
})

function go(id: string | null) {
  if (!id) return
  open.value = false
  openThread(id)
}
</script>

<template>
  <DouPopover
    v-if="state.detail"
    v-model:open="open"
    :title="t('usage.title')"
    :width="320"
    placement="bottom-end"
  >
    <DouButton variant="ghost" size="sm" :aria-label="t('usage.title')" :title="t('usage.title')">
      <CoinsIcon />
    </DouButton>
    <template #content>
      <div class="usage">
        <ul class="usage-rows">
          <li>
            <span>{{ t('usage.threads') }}</span>
            <span class="usage-value">{{ state.detail.threads.length }}</span>
          </li>
          <li>
            <span>{{ t('usage.total') }}</span>
            <span class="usage-value">{{ count(total) }}</span>
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
          <DouProgressGroup :items="parts" :label="t('usage.breakdown')" tooltip />
          <ul class="usage-rows">
            <template v-for="(part, index) in parts" :key="part.label">
              <li>
                <span class="usage-label">
                  <span
                    class="usage-dot"
                    :style="{ background: `var(--dou-color-${part.tone})` }"
                  />
                  {{ part.label }}
                </span>
                <span class="usage-value">{{ count(part.value) }}</span>
              </li>
              <!-- Thinking is part of the output, so it sits under it. -->
              <li v-if="index === 1 && usage.reasoning" class="usage-sub">
                <span>{{ t('usage.reasoning') }}</span>
                <span class="usage-value">{{ count(usage.reasoning) }}</span>
              </li>
            </template>
          </ul>

          <DouText variant="tertiary" size="sm" class="usage-heading">
            {{ t('usage.top') }}
          </DouText>
          <ul class="usage-rows">
            <li
              v-for="row in top"
              :key="row.id ?? ''"
              :class="{ 'usage-link': row.id }"
              @click="go(row.id)"
            >
              <DouText truncate class="usage-name">{{ row.name }}</DouText>
              <span class="usage-value">{{ count(row.tokens) }}</span>
              <span class="usage-share">{{ percent(row.tokens, total) }}</span>
            </li>
          </ul>
        </template>
      </div>
    </template>
  </DouPopover>
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

.usage-label {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.usage-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}

.usage-sub {
  padding-left: 16px !important;
  color: var(--dou-color-text-secondary);
  font-size: var(--dou-font-size-sm);
}

.usage-value,
.usage-share {
  font-variant-numeric: tabular-nums;
}

.usage-link {
  cursor: pointer;
}

.usage-link:hover .usage-name {
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
