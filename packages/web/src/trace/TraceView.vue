<script setup lang="ts">
import type { TraceTurn } from '@ariadne/core'
import {
  DouAlert,
  DouButton,
  DouSegmentedControl,
  DouSpinner,
  DouSplitter,
  DouTable,
  DouText,
  type TableColumn,
} from 'dou-ui'
import { ChevronLeftIcon, RotateCwIcon } from '@dou-ui/icons'
import { computed, onUnmounted, ref, useTemplateRef, watch } from 'vue'
import { api, ApiError } from '../api'
import { duration, locale, t, tm } from '../i18n'
import { onLiveEvent, backMain, state } from '../store'
import TraceInspector from './TraceInspector.vue'
import TraceTimeline from './TraceTimeline.vue'
import { findRow, parentKeys, traceRows, type TraceRow } from './rows'
import { rowsInRange, timelineModel, type TimeRange, type TimelineMode } from './timeline'

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
// The stretch marked out on the timeline.
const range = ref<TimeRange | null>(null)
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
    range.value = null
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

// How much of the width the table keeps beside the details, remembered in this browser.
// With nothing picked the table takes it all and the divider stays put.
const SPLIT_KEY = 'ariadne.traceSplit'
const split = ref(readSplit())
function readSplit() {
  try {
    const value = Number(localStorage.getItem(SPLIT_KEY))
    return value >= 30 && value <= 80 ? value : 62
  } catch {
    return 62
  }
}
const shownSplit = computed({
  get: () => (selected.value ? split.value : 100),
  set: (value) => {
    if (!selected.value) return
    split.value = value
    try {
      localStorage.setItem(SPLIT_KEY, String(Math.round(value)))
    } catch {
      // Not remembered; the default comes back next time.
    }
  },
})

const columns = computed<TableColumn<TraceRow>[]>(() => [
  // Kept in view when a narrow table scrolls sideways; the columns then keep their widths.
  { key: 'event', title: t('trace.col.event'), width: 150, fixed: 'start' },
  { key: 'content', title: t('trace.col.content'), ellipsis: true, minWidth: 200 },
  { key: 'input', title: t('trace.col.input'), width: 84, align: 'end' },
  { key: 'cached', title: t('trace.col.cached'), width: 84, align: 'end' },
  { key: 'cacheWrite', title: t('trace.col.cacheWrite'), width: 84, align: 'end' },
  { key: 'output', title: t('trace.col.output'), width: 72, align: 'end' },
  { key: 'reasoning', title: t('trace.col.reasoning'), width: 72, align: 'end' },
  {
    key: 'duration',
    title: t('trace.col.duration'),
    width: 96,
    align: 'end',
    value: (row) => (row.end === null ? null : row.end - row.start),
  },
])

const tokens = (value: unknown) =>
  typeof value === 'number' ? value.toLocaleString(locale.value) : ''

// Now, for what still runs; it moves on with each load.
const now = computed(() => (turns.value, Date.now()))

// Steps or real time, remembered in this browser.
const MODE_KEY = 'ariadne.traceTimeline'
const mode = ref<TimelineMode>(readMode())
function readMode(): TimelineMode {
  try {
    return localStorage.getItem(MODE_KEY) === 'duration' ? 'duration' : 'sequence'
  } catch {
    return 'sequence'
  }
}
watch(mode, (value) => {
  try {
    localStorage.setItem(MODE_KEY, value)
  } catch {
    // Not remembered; steps come back next time.
  }
})
const modes = computed(() => [
  { value: 'sequence', label: t('trace.mode.sequence') },
  { value: 'duration', label: t('trace.mode.duration') },
])

const timeline = computed(() => timelineModel(rows.value, mode.value, now.value))
const hovered = ref<string | null>(null)
// The rows in the marked stretch stand out; the rest fade.
const inRange = computed(() => rowsInRange(rows.value, timeline.value, range.value))
const table = useTemplateRef<{ scrollToRow: (key: string) => void }>('table')

function rowClass(row: TraceRow) {
  return [
    row.tone ? `trace-row--${row.tone}` : '',
    inRange.value && !inRange.value.has(row.key) ? 'trace-row--outside' : '',
    hovered.value === row.key ? 'trace-row--hovered' : '',
  ]
    .filter(Boolean)
    .join(' ')
}

/** A span picked on the timeline: its row opens, folded parents unfold, and it scrolls into view. */
function pick(key: string) {
  const open = new Set(folded.value)
  const unfold = (list: TraceRow[], trail: string[]): boolean => {
    for (const row of list) {
      if (row.key === key) {
        for (const parent of trail) open.delete(parent)
        return true
      }
      if (row.children && unfold(row.children, [...trail, row.key])) return true
    }
    return false
  }
  unfold(rows.value, [])
  folded.value = open
  current.value = key
  requestAnimationFrame(() => table.value?.scrollToRow(key))
}
</script>

<template>
  <div class="trace">
    <div class="trace-header">
      <DouButton variant="ghost" size="sm" @click="backMain">
        <template #leading><ChevronLeftIcon /></template>
        {{ t('view.back') }}
      </DouButton>
      <DouText weight="medium" truncate class="trace-title">
        {{ t('trace.title', { name }) }}
      </DouText>
      <DouSegmentedControl v-if="timeline" v-model="mode" size="sm" :options="modes" />
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
    <TraceTimeline
      v-if="!loading && timeline"
      :key="conversationId"
      v-model:range="range"
      :model="timeline"
      :mode="mode"
      :selected-key="current === null ? null : String(current)"
      :hovered-key="hovered"
      @select="pick"
      @hover="hovered = $event"
    />
    <DouSplitter
      v-if="!loading"
      v-model="shownSplit"
      :min="30"
      :max="selected ? 80 : 100"
      :disabled="!selected"
      class="trace-body"
    >
      <template #start>
        <div class="trace-table">
          <DouTable
            ref="table"
            class="trace-rows"
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
            :row-class="rowClass"
          >
            <template #cell-event="{ row }">
              <span :class="['trace-event', `trace-event--${row.kind}`]">{{ row.event }}</span>
            </template>
            <template #cell-content="{ row }">
              <span :class="['trace-content', `trace-content--${row.kind}`]">{{
                row.content
              }}</span>
            </template>
            <template #cell-input="{ value }">{{ tokens(value) }}</template>
            <template #cell-cached="{ value }">{{ tokens(value) }}</template>
            <template #cell-cacheWrite="{ value }">{{ tokens(value) }}</template>
            <template #cell-output="{ value }">{{ tokens(value) }}</template>
            <template #cell-reasoning="{ value }">{{ tokens(value) }}</template>
            <template #cell-duration="{ value }">
              {{ typeof value === 'number' ? duration(value) : '' }}
            </template>
          </DouTable>
        </div>
      </template>
      <template #end>
        <TraceInspector
          v-if="selected"
          :key="selected.key"
          :row="selected"
          :conversation-id="conversationId"
          class="trace-inspector"
          @close="current = null"
        />
      </template>
    </DouSplitter>
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
  flex: 1;
  min-height: 0;
}

.trace-table,
.trace-inspector {
  height: 100%;
  min-height: 0;
}

/* The table's own body is the one that scrolls: its max height is this height, not none. */
.trace-rows {
  height: 100%;
}

.trace-event--turn {
  font-weight: var(--dou-font-weight-medium);
}

.trace-event--tool,
.trace-event--retry,
.trace-event--fallback,
.trace-event--instructions,
.trace-event--results-cleared,
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

:deep(.trace-row--outside) td {
  opacity: 0.35;
}

:deep(.trace-row--hovered) td {
  background: var(--dou-color-bg-muted);
}
</style>
