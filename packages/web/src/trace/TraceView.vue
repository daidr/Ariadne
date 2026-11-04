<script setup lang="ts">
import type { TraceTurn } from '@ariadne/core'
import { DouAlert, DouButton, DouSpinner, DouTable, DouText, type TableColumn } from 'dou-ui'
import { ChevronLeftIcon, RotateCwIcon } from '@dou-ui/icons'
import { computed, onUnmounted, ref, watch } from 'vue'
import { api, ApiError } from '../api'
import { duration, locale, t, tm } from '../i18n'
import { onLiveEvent, openMain, state } from '../store'
import TraceInspector from './TraceInspector.vue'
import { findRow, parentKeys, traceRows, type TraceRow } from './rows'

const props = defineProps<{ conversationId: string }>()

const name = computed(() => {
  if (state.detail?.project.coordinatorId === props.conversationId) return t('coordinator.title')
  return state.detail?.threads.find((thread) => thread.id === props.conversationId)?.title ?? ''
})

const turns = ref<TraceTurn[]>([])
const more = ref(false)
const loading = ref(true)
const problem = ref<string | null>(null)
const current = ref<string | number | null>(null)
// Rows the user folded; everything else stays open, also as new rows come in.
const folded = ref(new Set<string>())

// Loads overlap as events arrive; only the latest one's answer is used.
let latest = 0

/** The newest turns again, keeping older ones already paged in. */
async function load() {
  const ticket = ++latest
  try {
    const page = await api.getTrace(props.conversationId)
    if (ticket !== latest) return
    const oldest = page.turns[0]?.seq ?? Number.POSITIVE_INFINITY
    const older = turns.value.filter((turn) => turn.seq < oldest)
    if (!turns.value.length) more.value = page.more
    turns.value = [...older, ...page.turns]
    problem.value = null
  } catch (error) {
    if (ticket === latest) {
      problem.value = error instanceof ApiError ? tm(error.localized) : String(error)
    }
  } finally {
    if (ticket === latest) loading.value = false
  }
}

async function loadEarlier() {
  const first = turns.value[0]
  if (!first) return
  try {
    const page = await api.getTrace(props.conversationId, first.seq)
    turns.value = [...page.turns, ...turns.value]
    more.value = page.more
  } catch (error) {
    problem.value = error instanceof ApiError ? tm(error.localized) : String(error)
  }
}

watch(
  () => props.conversationId,
  () => {
    turns.value = []
    current.value = null
    folded.value = new Set()
    loading.value = true
    void load()
  },
  { immediate: true },
)
// The running turn of this conversation moved on.
onUnmounted(
  onLiveEvent((event) => {
    const data = event.data as { conversationId?: unknown }
    if (data.conversationId === props.conversationId) void load()
  }),
)

const rows = computed(() => traceRows(turns.value))
const selected = computed(() => findRow(rows.value, current.value))
const expanded = computed({
  get: () => parentKeys(rows.value).filter((key) => !folded.value.has(key)),
  set: (keys) => {
    const open = new Set(keys.map(String))
    folded.value = new Set(parentKeys(rows.value).filter((key) => !open.has(key)))
  },
})

const columns = computed<TableColumn<TraceRow>[]>(() => [
  { key: 'event', title: t('trace.col.event'), width: 150 },
  { key: 'content', title: t('trace.col.content'), ellipsis: true, minWidth: 200 },
  { key: 'input', title: t('trace.col.input'), width: 84, align: 'end' },
  { key: 'cached', title: t('trace.col.cached'), width: 84, align: 'end' },
  { key: 'output', title: t('trace.col.output'), width: 72, align: 'end' },
  { key: 'reasoning', title: t('trace.col.reasoning'), width: 72, align: 'end' },
  {
    key: 'duration',
    title: t('trace.col.duration'),
    width: 84,
    align: 'end',
    value: (row) => (row.end === null ? null : row.end - row.start),
  },
  { key: 'timeline', title: t('trace.col.timeline'), width: 160 },
])

const tokens = (value: unknown) =>
  typeof value === 'number' ? value.toLocaleString(locale.value) : ''

// Now, for what still runs; it moves on with each load.
const now = computed(() => (turns.value, Date.now()))

/** Where the row sits in its turn, as a share of the turn's time. */
function bar(row: TraceRow) {
  const from = row.turn.startedAt
  const to = Math.max(row.turn.finishedAt ?? now.value, from + 1)
  const span = to - from
  const left = Math.min(1, Math.max(0, (row.start - from) / span))
  const end = Math.min(1, Math.max(left, ((row.end ?? now.value) - from) / span))
  return { left: `${left * 100}%`, width: `max(2px, ${(end - left) * 100}%)` }
}
</script>

<template>
  <div class="trace">
    <div class="trace-header">
      <DouButton variant="ghost" size="sm" @click="openMain({ kind: 'conversation' })">
        <template #leading><ChevronLeftIcon /></template>
        {{ t('changes.back') }}
      </DouButton>
      <DouText weight="medium" truncate class="trace-title">
        {{ t('trace.title', { name }) }}
      </DouText>
      <DouButton v-if="more" variant="ghost" size="sm" @click="loadEarlier">
        {{ t('trace.earlier') }}
      </DouButton>
      <DouButton
        variant="ghost"
        size="sm"
        :aria-label="t('changes.refresh')"
        :title="t('changes.refresh')"
        @click="load"
      >
        <RotateCwIcon />
      </DouButton>
    </div>
    <DouAlert v-if="problem" tone="danger" class="trace-problem">{{ problem }}</DouAlert>
    <div v-if="loading" class="trace-empty"><DouSpinner /></div>
    <div v-else class="trace-body">
      <div class="trace-table">
        <DouTable
          v-model:current="current"
          v-model:expanded="expanded"
          :data="rows"
          :columns="columns"
          row-key="key"
          children-key="children"
          size="sm"
          :indent="14"
          highlight-current
          hoverable
          max-height="100%"
          :empty-text="t('trace.empty')"
          :row-class="(row: TraceRow) => (row.tone ? `trace-row--${row.tone}` : undefined)"
        >
          <template #cell-event="{ row }">
            <span :class="['trace-event', `trace-event--${row.kind}`]">{{ row.event }}</span>
          </template>
          <template #cell-content="{ row }">
            <span :class="['trace-content', `trace-content--${row.kind}`]">{{ row.content }}</span>
          </template>
          <template #cell-input="{ value }">{{ tokens(value) }}</template>
          <template #cell-cached="{ value }">{{ tokens(value) }}</template>
          <template #cell-output="{ value }">{{ tokens(value) }}</template>
          <template #cell-reasoning="{ value }">{{ tokens(value) }}</template>
          <template #cell-duration="{ value }">
            {{ typeof value === 'number' ? duration(value) : '' }}
          </template>
          <template #cell-timeline="{ row }">
            <div class="trace-track">
              <span :class="['trace-bar', `trace-bar--${row.kind}`]" :style="bar(row)" />
            </div>
          </template>
        </DouTable>
      </div>
      <TraceInspector
        v-if="selected"
        :key="selected.key"
        :row="selected"
        :conversation-id="conversationId"
        class="trace-inspector"
        @close="current = null"
      />
    </div>
  </div>
</template>

<style scoped>
.trace {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.trace-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 16px;
  border-bottom: 1px solid var(--dou-color-border);
}

.trace-title {
  flex: 1;
  min-width: 0;
}

.trace-problem {
  margin: 8px 16px 0;
}

.trace-empty {
  display: grid;
  place-items: center;
  padding: 48px 0;
}

.trace-body {
  display: flex;
  flex: 1;
  min-height: 0;
}

.trace-table {
  flex: 1;
  min-width: 0;
  min-height: 0;
}

.trace-inspector {
  width: 520px;
  flex: none;
  border-left: 1px solid var(--dou-color-border);
}

.trace-event--turn {
  font-weight: var(--dou-font-weight-medium);
}

.trace-event--tool,
.trace-event--retry,
.trace-event--instructions,
.trace-event--compaction-failed {
  color: var(--dou-color-text-secondary);
}

.trace-content--tool {
  font-family: var(--dou-font-family-mono);
  font-size: var(--dou-font-size-sm);
}

:deep(.trace-row--danger) .trace-content {
  color: var(--dou-color-danger-text);
}

:deep(.trace-row--warning) .trace-content {
  color: var(--dou-color-warning-text);
}

.trace-track {
  position: relative;
  height: 8px;
  border-radius: 2px;
  background: var(--dou-color-bg-muted);
}

.trace-bar {
  position: absolute;
  top: 0;
  bottom: 0;
  border-radius: 2px;
  background: var(--dou-color-primary);
}

.trace-bar--turn {
  background: var(--dou-color-border-strong);
}

.trace-bar--tool {
  background: var(--dou-color-success);
}

.trace-bar--retry,
.trace-bar--compaction-failed {
  background: var(--dou-color-warning);
}

.trace-bar--instructions {
  background: var(--dou-color-text-tertiary);
}
</style>
