<script setup lang="ts">
import type {
  AllNeedsMeResult,
  AttentionView,
  LiveCommand,
  NeedsMeItem,
  PulseConcurrency,
  PulseLane,
  PulseMarker,
  PulseSpan,
  ThreadView,
} from '@ariadne/core'
import {
  ChevronDownIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  FolderIcon,
  ListTreeIcon,
  PlayIcon,
  RotateCwIcon,
} from '@dou-ui/icons'
import { DouButton, DouEmpty, DouSegmentedControl, DouStatusDot, DouText } from 'dou-ui'
import { computed, ref } from 'vue'
import AskBlock from '../blocks/AskBlock.vue'
import { locale, t } from '../i18n'
import { branchChanges } from '../review/merge-offered'
import { endAside, resumeThread, retryTurn } from '../store'
import {
  countLiveSlotOccupancy,
  formatClockShort,
  formatDurationCompact,
  isNeedsLane,
  laneStatusDotClass,
  liveCommandLabel,
  percentOf,
  PULSE_WINDOW_KEYS,
  resolveLaneAttentions,
  resolveLaneThreadSummary,
  resolveSpanVisualKind,
  type PulseFeedItem,
  type PulseFilter,
  type PulseLaneGroup,
  type PulseNarrowTab,
  type PulsePartitionedLanes,
  type PulseScope,
  type PulseSpanVisualKind,
  type PulseWindowKey,
} from './model'

const props = defineProps<{
  partitioned: PulsePartitionedLanes
  spansByConversation: ReadonlyMap<string, PulseSpan[]>
  markersByConversation: ReadonlyMap<string, PulseMarker[]>
  feedItems: readonly PulseFeedItem[]
  concurrency: PulseConcurrency
  allSpans: readonly PulseSpan[]
  lastReturnedAt: number | null
  nowMs: number
  viewStart: number
  viewEnd: number
  selectedLaneId: string | null
  threads: ReadonlyMap<string, ThreadView>
  liveCommands: Readonly<Record<string, LiveCommand>>
  currentProjectId: string | null
  currentNeedsMe: readonly NeedsMeItem[] | undefined
  allNeedsMe: AllNeedsMeResult | null | undefined
}>()

const emit = defineEmits<{
  back: []
  select: [laneId: string, spanId: number | null]
  openLane: [lane: PulseLane]
  openTrace: [lane: PulseLane]
  mutated: []
  expandWindow: []
}>()

const scope = defineModel<PulseScope>('scope', { required: true })
const tab = defineModel<PulseNarrowTab>('tab', { required: true })
const filter = defineModel<PulseFilter>('filter', { required: true })
const windowKey = defineModel<PulseWindowKey>('windowKey', { required: true })
const showCompleted = defineModel<boolean>('showCompleted', { required: true })

const actingLaneId = ref<string | null>(null)

const scopeOptions = computed(() => [
  { value: 'project' satisfies PulseScope, label: t('pulse.scope.project') },
  { value: 'all' satisfies PulseScope, label: t('pulse.scope.all') },
])

const tabOptions = computed(() => [
  { value: 'cards' satisfies PulseNarrowTab, label: t('pulse.narrow.tabCards') },
  { value: 'feed' satisfies PulseNarrowTab, label: t('pulse.narrow.tabFeed') },
])

const windowOptions = computed(() =>
  PULSE_WINDOW_KEYS.map((key) => ({
    value: key,
    label: t(`pulse.window.${key}`),
  })),
)

const liveActiveSlots = computed(() => {
  const fromSpans = countLiveSlotOccupancy(props.allSpans)
  return Math.max(fromSpans, props.concurrency.current)
})

const isSlotsFull = computed(
  () => props.concurrency.slots > 0 && liveActiveSlots.value >= props.concurrency.slots,
)

const slotPillText = computed(() => {
  const slots = props.concurrency.slots
  if (slots <= 0) return String(liveActiveSlots.value)
  return t(isSlotsFull.value ? 'pulse.ribbon.full' : 'pulse.ribbon.usage', {
    active: liveActiveSlots.value,
    slots,
  })
})

const returnPct = computed(() => {
  if (props.lastReturnedAt === null) return null
  const pct = percentOf(props.lastReturnedAt, props.viewStart, props.viewEnd)
  return pct >= 0 && pct <= 100 ? pct : null
})

const nowPct = computed(() => {
  const pct = percentOf(props.nowMs, props.viewStart, props.viewEnd)
  return pct >= 0 && pct <= 100 ? pct : null
})

interface MiniSpanSegment {
  id: number
  visualKind: PulseSpanVisualKind
  leftPct: number
  widthPct: number
  running: boolean
}

function miniSpansForLane(lane: PulseLane): MiniSpanSegment[] {
  const list = props.spansByConversation.get(lane.conversationId) ?? []
  const out: MiniSpanSegment[] = []
  for (const span of list) {
    const endAt = span.endedAt ?? props.nowMs
    if (endAt < props.viewStart || span.startedAt > props.viewEnd) continue
    const leftPct = Math.max(0, percentOf(span.startedAt, props.viewStart, props.viewEnd))
    const rightPct = Math.min(100, percentOf(endAt, props.viewStart, props.viewEnd))
    out.push({
      id: span.id,
      visualKind: resolveSpanVisualKind(span, lane.role),
      leftPct,
      widthPct: Math.max(1.5, rightPct - leftPct),
      running: span.endedAt === null,
    })
  }
  return out
}

function laneTitle(lane: PulseLane): string {
  if (lane.role === 'coordinator') return t('coordinator.title')
  return lane.title || lane.conversationId
}

function latestSpanOf(lane: PulseLane): PulseSpan | null {
  const list = props.spansByConversation.get(lane.conversationId) ?? []
  return list.at(-1) ?? null
}

function laneStageSummary(lane: PulseLane): { label: string; duration: string | null } {
  const liveCmd = liveCommandLabel(lane, props.liveCommands, 44)
  const latest = latestSpanOf(lane)
  if (liveCmd) {
    const dur = latest
      ? formatDurationCompact(Math.max(0, (latest.endedAt ?? props.nowMs) - latest.startedAt))
      : null
    return { label: `$ ${liveCmd}`, duration: dur }
  }
  if (latest) {
    const kind = resolveSpanVisualKind(latest, lane.role)
    const stageText = t(`pulse.stage.${kind}`)
    const label = latest.label || stageText
    const dur = formatDurationCompact(
      Math.max(0, (latest.endedAt ?? props.nowMs) - latest.startedAt),
    )
    return { label, duration: dur }
  }
  return {
    label: lane.role === 'coordinator' ? t('pulse.stage.coord') : t('pulse.stage.working'),
    duration: null,
  }
}

function laneBadge(
  lane: PulseLane,
): { tone: 'warning' | 'danger' | 'primary' | 'success' | 'neutral'; text: string } | null {
  if (lane.stage === 'answer' || lane.attentionId !== null) {
    const latest = latestSpanOf(lane)
    const since = latest?.endedAt === null ? latest.startedAt : lane.activeAt
    return { tone: 'warning', text: formatDurationCompact(Math.max(0, props.nowMs - since)) }
  }
  if (lane.stage === 'blocked' || lane.stage === 'halted') {
    const failures = props.threads.get(lane.conversationId)?.pullRequest?.checks.failed.length ?? 0
    return { tone: 'danger', text: failures > 0 ? `×${failures}` : '!' }
  }
  if (lane.completed || lane.stage === 'done') {
    return { tone: 'success', text: t('thread.stage.done') }
  }
  if (lane.stage === 'working') {
    return {
      tone: 'primary',
      text: t(lane.role === 'reviewer' ? 'pulse.stage.reviewing' : 'pulse.stage.working'),
    }
  }
  if (lane.stage === 'queued') {
    return { tone: 'neutral', text: t('pulse.stage.queued') }
  }
  if (lane.stage === 'review') {
    return { tone: 'neutral', text: t('pulse.stage.waiting_review') }
  }
  return null
}

function laneMeta(lane: PulseLane) {
  const thread = props.threads.get(lane.conversationId)
  const pr = thread?.pullRequest?.number ? `#${thread.pullRequest.number}` : null
  const changes = thread ? branchChanges(thread) : null
  const diff = changes ? { added: changes.added, removed: changes.removed } : null
  const progress =
    thread?.progress && thread.progress.total > 0 && thread.progress.done < thread.progress.total
      ? `${thread.progress.done}/${thread.progress.total}`
      : null
  const project =
    scope.value === 'all' && lane.completed && lane.projectName ? lane.projectName : null
  return { pr, diff, progress, project }
}

function laneAttentions(lane: PulseLane): AttentionView[] {
  return resolveLaneAttentions(lane, props.currentProjectId, props.currentNeedsMe, props.allNeedsMe)
}

function laneSummary(lane: PulseLane) {
  return resolveLaneThreadSummary(lane, props.threads, props.allNeedsMe)
}

function groupHeaderTitle(group: PulseLaneGroup): string {
  if (group.kind === 'needs') return t('pulse.group.needsTitle')
  if (group.kind === 'active') return t('pulse.group.activeTitle')
  if (group.kind === 'project') return group.title ?? group.projectId ?? ''
  if (group.kind === 'node') return t('pulse.group.nodePrefix', { title: group.title ?? '' })
  return t('pulse.group.unlinkedNode')
}

const returnDividerIndex = computed(() => {
  const ret = props.lastReturnedAt
  if (ret === null || ret < props.viewStart || ret > props.viewEnd || !props.feedItems.length) {
    return -1
  }
  const idx = props.feedItems.findIndex((item) => item.at <= ret)
  return idx === -1 ? props.feedItems.length : idx
})

const returnDividerText = computed(() => {
  if (props.lastReturnedAt === null) return ''
  return t('pulse.narrow.returnDivider', {
    time: formatClockShort(props.lastReturnedAt, props.viewStart, props.viewEnd, locale.value),
  })
})

function formatFeedTime(at: number): string {
  return formatClockShort(at, props.viewStart, props.viewEnd, locale.value)
}

function feedText(item: PulseFeedItem): string {
  const title = t(`pulse.marker.${item.kind}`)
  if (item.label && item.label !== title) return `${title} · ${item.label}`
  return title
}

async function runCardAction(laneId: string, fn: () => Promise<unknown>) {
  if (actingLaneId.value) return
  actingLaneId.value = laneId
  try {
    await fn()
    emit('mutated')
  } finally {
    actingLaneId.value = null
  }
}

const showCardsEmpty = computed(
  () =>
    props.partitioned.groups.length === 0 &&
    !props.partitioned.coordinator &&
    props.partitioned.completed.length === 0,
)

const showFilteredEmpty = computed(
  () => filter.value !== 'all' && props.partitioned.groups.length === 0,
)
</script>

<template>
  <div class="pulse-narrow" data-pulse-narrow>
    <div class="pulse-narrow-header">
      <div class="pulse-narrow-top">
        <div class="pulse-narrow-brand">
          <DouButton
            variant="ghost"
            size="sm"
            :aria-label="t('view.back')"
            :title="t('view.back')"
            @click="emit('back')"
          >
            <ChevronLeftIcon aria-hidden="true" />
          </DouButton>
          <DouText weight="medium" truncate>{{ t('pulse.title') }}</DouText>
        </div>
        <span
          class="pulse-narrow-slots pulse-mono"
          :class="{ 'is-full': isSlotsFull }"
          data-pulse-narrow-slots
        >
          <DouStatusDot
            v-if="liveActiveSlots > 0"
            :tone="isSlotsFull ? 'warning' : 'primary'"
            ripple
            aria-hidden="true"
          />
          <span>{{ slotPillText }}</span>
        </span>
      </div>

      <div class="pulse-narrow-controls">
        <DouSegmentedControl
          v-model="scope"
          :options="scopeOptions"
          size="sm"
          data-pulse-scope
          :aria-label="t('pulse.scope.aria')"
        />
        <DouSegmentedControl v-model="windowKey" :options="windowOptions" size="sm" />
      </div>

      <div class="pulse-narrow-tabs">
        <DouSegmentedControl
          v-model="tab"
          :options="tabOptions"
          size="sm"
          block
          data-pulse-narrow-tabs
        />
      </div>

      <div v-if="tab === 'cards'" class="pulse-narrow-filters" role="group">
        <button
          type="button"
          class="pulse-filter-pill"
          :class="{ 'is-active': filter === 'all' }"
          data-pulse-filter="all"
          :aria-pressed="filter === 'all'"
          @click="filter = 'all'"
        >
          <span>{{ t('pulse.filter.all') }}</span>
          <span class="pulse-mono">{{ partitioned.counts.all }}</span>
        </button>
        <button
          type="button"
          class="pulse-filter-pill"
          :class="{ 'is-active-warning': filter === 'needs' }"
          data-pulse-filter="needs"
          :aria-pressed="filter === 'needs'"
          @click="filter = 'needs'"
        >
          <span class="pulse-dot is-warning" aria-hidden="true" />
          <span>{{ t('pulse.filter.needs') }}</span>
          <span class="pulse-mono">{{ partitioned.counts.needs }}</span>
        </button>
        <button
          type="button"
          class="pulse-filter-pill"
          :class="{ 'is-active': filter === 'active' }"
          data-pulse-filter="active"
          :aria-pressed="filter === 'active'"
          @click="filter = 'active'"
        >
          <span class="pulse-dot is-primary" aria-hidden="true" />
          <span>{{ t('pulse.filter.active') }}</span>
          <span class="pulse-mono">{{ partitioned.counts.active }}</span>
        </button>
      </div>
    </div>

    <!-- Tab 1: Pulse cards -->
    <div v-if="tab === 'cards'" class="pulse-narrow-scroll" data-pulse-narrow-cards>
      <!-- Pinned coordinator card in single-project mode -->
      <article
        v-if="
          partitioned.coordinator &&
          (filter === 'all' || (filter === 'needs' && isNeedsLane(partitioned.coordinator)))
        "
        class="pulse-card-item is-coordinator"
        :class="{
          'is-needs':
            partitioned.coordinator.stage === 'answer' ||
            partitioned.coordinator.attentionId !== null,
          'is-selected': selectedLaneId === partitioned.coordinator.conversationId,
        }"
        :data-lane-id="partitioned.coordinator.conversationId"
        @click="emit('select', partitioned.coordinator.conversationId, null)"
      >
        <div class="pulse-card-head">
          <span
            class="pulse-dot"
            :class="laneStatusDotClass(partitioned.coordinator)"
            aria-hidden="true"
          />
          <button
            type="button"
            class="pulse-card-title-btn"
            @click.stop="emit('openLane', partitioned.coordinator)"
          >
            {{ laneTitle(partitioned.coordinator) }}
          </button>
        </div>
        <div class="pulse-mini-strip" aria-hidden="true">
          <div
            v-for="seg in miniSpansForLane(partitioned.coordinator)"
            :key="seg.id"
            class="pulse-mini-seg"
            :class="[`is-${seg.visualKind}`, { 'is-running': seg.running }]"
            :style="{ left: `${seg.leftPct}%`, width: `${seg.widthPct}%` }"
          />
          <div
            v-if="returnPct !== null"
            class="pulse-mini-return"
            :style="{ left: `${returnPct}%` }"
          />
          <div v-if="nowPct !== null" class="pulse-mini-now" :style="{ left: `${nowPct}%` }" />
        </div>
        <div class="pulse-mini-meta">
          <span class="pulse-mini-stage">{{
            laneStageSummary(partitioned.coordinator).label
          }}</span>
          <span v-if="laneStageSummary(partitioned.coordinator).duration" class="pulse-mono">
            {{ laneStageSummary(partitioned.coordinator).duration }}
          </span>
        </div>
        <div
          v-if="laneAttentions(partitioned.coordinator).length"
          class="pulse-card-inline-actions"
          @click.stop
        >
          <template v-for="att in laneAttentions(partitioned.coordinator)" :key="att.id">
            <div v-if="att.block.type === 'form'" class="pulse-card-quick-row">
              <DouText size="sm" truncate>{{ att.block.title }}</DouText>
              <DouButton
                size="sm"
                tone="primary"
                @click="emit('openLane', partitioned.coordinator)"
              >
                {{ t('block.fill') }}
              </DouButton>
            </div>
            <div v-else class="pulse-card-ask" data-pulse-inline-ask>
              <AskBlock
                :attention-id="att.id"
                :block="att.block"
                :answer="null"
                @answered="emit('mutated')"
              />
            </div>
          </template>
        </div>
      </article>

      <!-- Grouped cards -->
      <section
        v-for="group in partitioned.groups"
        :key="group.key"
        class="pulse-narrow-group"
        :data-pulse-group="group.key"
      >
        <div class="pulse-narrow-group-head">
          <FolderIcon v-if="group.kind === 'project'" class="pulse-group-icon" aria-hidden="true" />
          <span
            v-else-if="group.kind === 'needs' || group.kind === 'active'"
            class="pulse-dot"
            :class="group.kind === 'needs' ? 'is-warning' : 'is-primary'"
            aria-hidden="true"
          />
          <span class="pulse-group-title">{{ groupHeaderTitle(group) }}</span>
          <span v-if="group.kind === 'project' && group.isCurrent" class="pulse-group-current">
            {{ t('pulse.group.projectCurrent') }}
          </span>
          <span
            class="pulse-badge pulse-mono"
            :class="
              group.kind === 'needs'
                ? 'is-warning'
                : group.kind === 'active'
                  ? 'is-primary'
                  : 'is-neutral'
            "
          >
            {{ group.rows.length }}
          </span>
        </div>

        <article
          v-for="row in group.rows"
          :key="row.lane.conversationId"
          class="pulse-card-item"
          :class="{
            'is-coordinator': row.lane.role === 'coordinator',
            'is-needs': row.lane.stage === 'answer' || row.lane.attentionId !== null,
            'is-blocked': row.lane.stage === 'blocked' || row.lane.stage === 'halted',
            'is-selected': selectedLaneId === row.lane.conversationId,
          }"
          :data-lane-id="row.lane.conversationId"
          :data-lane-needs="isNeedsLane(row.lane) ? 'true' : undefined"
          @click="emit('select', row.lane.conversationId, null)"
        >
          <div class="pulse-card-head">
            <span class="pulse-dot" :class="laneStatusDotClass(row.lane)" aria-hidden="true" />
            <button
              type="button"
              class="pulse-card-title-btn"
              @click.stop="emit('openLane', row.lane)"
            >
              <span v-if="row.nested" class="pulse-nested-prefix" aria-hidden="true">↳</span>
              {{ laneTitle(row.lane) }}
            </button>
            <span v-if="row.lane.role === 'reviewer'" class="pulse-reviewer-tag">
              {{ t('pulse.narrow.reviewerTag') }}
            </span>
            <span
              v-if="laneBadge(row.lane)"
              class="pulse-badge pulse-mono"
              :class="`is-${laneBadge(row.lane)!.tone}`"
            >
              {{ laneBadge(row.lane)!.text }}
            </span>
          </div>

          <div
            v-if="laneMeta(row.lane).pr || laneMeta(row.lane).diff || laneMeta(row.lane).progress"
            class="pulse-card-meta pulse-mono"
          >
            <span v-if="laneMeta(row.lane).pr">{{ laneMeta(row.lane).pr }}</span>
            <span v-if="laneMeta(row.lane).diff" class="pulse-diff">
              <span class="pulse-diff-add">+{{ laneMeta(row.lane).diff!.added }}</span>
              <span class="pulse-diff-del">-{{ laneMeta(row.lane).diff!.removed }}</span>
            </span>
            <span v-if="laneMeta(row.lane).progress">{{ laneMeta(row.lane).progress }}</span>
          </div>

          <div class="pulse-mini-strip" aria-hidden="true">
            <div
              v-for="seg in miniSpansForLane(row.lane)"
              :key="seg.id"
              class="pulse-mini-seg"
              :class="[`is-${seg.visualKind}`, { 'is-running': seg.running }]"
              :style="{ left: `${seg.leftPct}%`, width: `${seg.widthPct}%` }"
            />
            <div
              v-if="returnPct !== null"
              class="pulse-mini-return"
              :style="{ left: `${returnPct}%` }"
            />
            <div v-if="nowPct !== null" class="pulse-mini-now" :style="{ left: `${nowPct}%` }" />
          </div>

          <div class="pulse-mini-meta">
            <span class="pulse-mini-stage">{{ laneStageSummary(row.lane).label }}</span>
            <span v-if="laneStageSummary(row.lane).duration" class="pulse-mono">
              {{ laneStageSummary(row.lane).duration }}
            </span>
          </div>

          <div
            v-if="row.lane.lastHeadline && !liveCommandLabel(row.lane, liveCommands)"
            class="pulse-card-headline"
          >
            {{ row.lane.lastHeadline }}
          </div>

          <!-- Inline in-place approval / choice -->
          <div v-if="laneAttentions(row.lane).length" class="pulse-card-inline-actions" @click.stop>
            <template v-for="att in laneAttentions(row.lane)" :key="att.id">
              <div v-if="att.block.type === 'form'" class="pulse-card-quick-row">
                <DouText size="sm" truncate>{{ att.block.title }}</DouText>
                <DouButton size="sm" tone="primary" @click="emit('openLane', row.lane)">
                  {{ t('block.fill') }}
                </DouButton>
              </div>
              <div v-else class="pulse-card-ask" data-pulse-inline-ask>
                <AskBlock
                  :attention-id="att.id"
                  :block="att.block"
                  :answer="null"
                  @answered="emit('mutated')"
                />
              </div>
            </template>
          </div>

          <!-- Quick retry / resume / aside action row -->
          <div v-else-if="laneSummary(row.lane)?.stopped" class="pulse-card-quick-row" @click.stop>
            <DouButton
              size="sm"
              variant="outline"
              :loading="actingLaneId === row.lane.conversationId"
              @click="
                runCardAction(row.lane.conversationId, () => resumeThread(row.lane.conversationId))
              "
            >
              <template #leading><PlayIcon aria-hidden="true" /></template>
              {{ t('thread.resume') }}
            </DouButton>
          </div>
          <div
            v-else-if="
              row.lane.stage === 'halted' ||
              row.lane.stage === 'silent' ||
              row.lane.stage === 'blocked'
            "
            class="pulse-card-quick-row"
            @click.stop
          >
            <DouButton
              size="sm"
              variant="outline"
              data-pulse-retry
              :loading="actingLaneId === row.lane.conversationId"
              @click="
                runCardAction(row.lane.conversationId, () => retryTurn(row.lane.conversationId))
              "
            >
              <template #leading><RotateCwIcon aria-hidden="true" /></template>
              {{ t('conversation.retry') }}
            </DouButton>
            <DouButton
              size="sm"
              variant="ghost"
              data-pulse-open-trace
              @click="emit('openTrace', row.lane)"
            >
              <template #leading><ListTreeIcon aria-hidden="true" /></template>
              {{ t('pulse.inspector.openTrace') }}
            </DouButton>
          </div>
          <div
            v-else-if="row.lane.stage === 'aside' || laneSummary(row.lane)?.aside?.due"
            class="pulse-card-quick-row"
            @click.stop
          >
            <DouButton
              size="sm"
              variant="outline"
              :loading="actingLaneId === row.lane.conversationId"
              @click="
                runCardAction(row.lane.conversationId, () => endAside(row.lane.conversationId))
              "
            >
              {{ t('aside.done') }}
            </DouButton>
          </div>
        </article>
      </section>

      <!-- Empty states -->
      <div v-if="showFilteredEmpty" class="pulse-narrow-empty" data-pulse-inline-empty>
        <DouEmpty :description="t('pulse.empty.filtered')">
          <DouButton size="sm" variant="soft" @click="filter = 'all'">
            {{ t('pulse.empty.resetFilter') }}
          </DouButton>
        </DouEmpty>
      </div>
      <div v-else-if="showCardsEmpty" class="pulse-narrow-empty" data-pulse-inline-empty>
        <DouEmpty :description="t('pulse.empty.window')">
          <DouButton
            v-if="windowKey !== '24h'"
            size="sm"
            variant="soft"
            @click="emit('expandWindow')"
          >
            {{ t('pulse.empty.expandWindow') }}
          </DouButton>
        </DouEmpty>
      </div>

      <!-- Completed collapsible section -->
      <div v-if="partitioned.completed.length" class="pulse-narrow-completed">
        <button
          type="button"
          class="pulse-completed-toggle"
          data-pulse-completed-toggle
          :aria-expanded="showCompleted"
          @click="showCompleted = !showCompleted"
        >
          <ChevronDownIcon v-if="showCompleted" class="pulse-completed-icon" aria-hidden="true" />
          <ChevronRightIcon v-else class="pulse-completed-icon" aria-hidden="true" />
          <span>{{ t('pulse.completed.summary', { count: partitioned.completed.length }) }}</span>
        </button>

        <template v-if="showCompleted">
          <article
            v-for="row in partitioned.completed"
            :key="row.lane.conversationId"
            class="pulse-card-item is-completed"
            :class="{ 'is-selected': selectedLaneId === row.lane.conversationId }"
            :data-lane-id="row.lane.conversationId"
            data-lane-completed="true"
            @click="emit('select', row.lane.conversationId, null)"
          >
            <div class="pulse-card-head">
              <span class="pulse-dot" :class="laneStatusDotClass(row.lane)" aria-hidden="true" />
              <button
                type="button"
                class="pulse-card-title-btn"
                @click.stop="emit('openLane', row.lane)"
              >
                {{ laneTitle(row.lane) }}
              </button>
              <span v-if="laneMeta(row.lane).project" class="pulse-badge is-neutral">
                {{ laneMeta(row.lane).project }}
              </span>
            </div>
            <div class="pulse-mini-strip" aria-hidden="true">
              <div
                v-for="seg in miniSpansForLane(row.lane)"
                :key="seg.id"
                class="pulse-mini-seg"
                :class="`is-${seg.visualKind}`"
                :style="{ left: `${seg.leftPct}%`, width: `${seg.widthPct}%` }"
              />
            </div>
          </article>
        </template>
      </div>
    </div>

    <!-- Tab 2: Key events feed -->
    <div v-else class="pulse-narrow-scroll" data-pulse-narrow-feed>
      <div v-if="!feedItems.length" class="pulse-narrow-empty">
        <DouEmpty :description="t('pulse.narrow.feedEmpty')">
          <DouButton
            v-if="windowKey !== '24h'"
            size="sm"
            variant="soft"
            @click="emit('expandWindow')"
          >
            {{ t('pulse.empty.expandWindow') }}
          </DouButton>
        </DouEmpty>
      </div>
      <div v-else class="pulse-feed-list">
        <template v-for="(item, index) in feedItems" :key="item.key">
          <div
            v-if="index === returnDividerIndex"
            class="pulse-feed-return-divider"
            data-pulse-feed-return
          >
            <span class="pulse-mono">{{ returnDividerText }}</span>
          </div>
          <button
            type="button"
            class="pulse-feed-row"
            :data-feed-lane-id="item.conversationId"
            @click="emit('select', item.conversationId, null)"
          >
            <span class="pulse-feed-time pulse-mono">{{ formatFeedTime(item.at) }}</span>
            <span class="pulse-feed-dot" :class="`is-${item.tone}`" aria-hidden="true" />
            <span class="pulse-feed-body">
              <span class="pulse-feed-title">{{ feedText(item) }}</span>
              <span class="pulse-feed-meta">
                {{ item.laneRole === 'coordinator' ? t('coordinator.title') : item.laneTitle }}
                <template v-if="scope === 'all' && item.projectName">
                  · {{ item.projectName }}
                </template>
              </span>
            </span>
          </button>
        </template>
        <div
          v-if="returnDividerIndex === feedItems.length"
          class="pulse-feed-return-divider"
          data-pulse-feed-return
        >
          <span class="pulse-mono">{{ returnDividerText }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.pulse-narrow,
.pulse-narrow * {
  box-sizing: border-box;
}

.pulse-narrow {
  --pulse-ci-soft: color-mix(in srgb, var(--dou-color-chart-4) 14%, var(--dou-color-bg-surface));
  --pulse-review-soft: color-mix(
    in srgb,
    var(--dou-color-chart-6) 14%,
    var(--dou-color-bg-surface)
  );
  --pulse-review-fill: var(--dou-color-chart-6);
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  background: var(--dou-color-bg-canvas);
  overflow: hidden;
}

:global(html[data-dou-theme='dark'] .pulse-narrow) {
  --pulse-ci-soft: color-mix(in srgb, var(--dou-color-chart-4) 22%, var(--dou-color-bg-surface));
  --pulse-review-soft: color-mix(
    in srgb,
    var(--dou-color-chart-6) 22%,
    var(--dou-color-bg-surface)
  );
  --pulse-review-fill: color-mix(
    in srgb,
    var(--dou-color-chart-6) 76%,
    var(--dou-color-bg-surface)
  );
}

.pulse-narrow-header {
  display: flex;
  flex: none;
  flex-direction: column;
  gap: 8px;
  padding: 8px 12px 10px;
  background: var(--dou-color-bg-surface);
  border-bottom: 1px solid var(--dou-color-border);
}

.pulse-narrow-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
}

.pulse-narrow-brand {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
}

.pulse-narrow-slots {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex: none;
  padding: 2px 9px;
  border-radius: 9999px;
  background: var(--dou-color-primary-soft);
  color: var(--dou-color-primary-text);
  font-size: 11px;
  font-weight: 600;
}

.pulse-narrow-slots.is-full {
  background: var(--dou-color-warning-soft);
  color: var(--dou-color-warning-text);
}

.pulse-narrow-controls {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 6px 8px;
}

.pulse-narrow-controls > * {
  flex: none;
}

.pulse-narrow-tabs {
  display: flex;
}

.pulse-narrow-filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}

.pulse-filter-pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 26px;
  padding: 0 9px;
  border-radius: 9999px;
  border: 1px solid var(--dou-color-border);
  background: var(--dou-color-bg-surface);
  color: var(--dou-color-text-secondary);
  font: inherit;
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
}

.pulse-filter-pill.is-active {
  border-color: var(--dou-color-primary);
  background: var(--dou-color-primary-soft);
  color: var(--dou-color-primary-text);
}

.pulse-filter-pill.is-active-warning {
  border-color: var(--dou-color-warning);
  background: var(--dou-color-warning-soft);
  color: var(--dou-color-warning-text);
}

.pulse-narrow-scroll {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 12px;
  min-height: 0;
  overflow-y: auto;
  overflow-x: hidden;
  padding: 12px;
}

.pulse-narrow-group {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.pulse-narrow-group-head {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 2px 2px 0;
  font-size: 12px;
  font-weight: 600;
  color: var(--dou-color-text-secondary);
}

.pulse-group-icon {
  flex: none;
  width: 13px;
  height: 13px;
  color: var(--dou-color-text-tertiary);
}

.pulse-group-title {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pulse-group-current {
  flex: none;
  padding: 1px 5px;
  border-radius: 9999px;
  background: var(--dou-color-primary-soft);
  color: var(--dou-color-primary-text);
  font-size: 10px;
  font-weight: 500;
}

.pulse-card-item {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 11px 12px;
  border-radius: var(--dou-radius-container);
  border: 1px solid var(--dou-color-border);
  background: var(--dou-color-bg-surface);
  cursor: pointer;
  transition: border-color 0.15s ease;
}

.pulse-card-item.is-coordinator {
  background: var(--dou-color-bg-muted);
}

.pulse-card-item.is-needs {
  border-color: color-mix(in srgb, var(--dou-color-warning) 38%, var(--dou-color-border));
  background: color-mix(in srgb, var(--dou-color-warning-soft) 40%, var(--dou-color-bg-surface));
}

.pulse-card-item.is-blocked {
  border-color: color-mix(in srgb, var(--dou-color-danger) 38%, var(--dou-color-border));
  background: color-mix(in srgb, var(--dou-color-danger-soft) 36%, var(--dou-color-bg-surface));
}

.pulse-card-item.is-selected {
  border-color: var(--dou-color-primary);
}

.pulse-card-item.is-completed {
  opacity: 0.78;
}

.pulse-card-head {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.pulse-card-title-btn {
  flex: 1;
  min-width: 0;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--dou-color-text);
  font: inherit;
  font-size: 13px;
  font-weight: 500;
  text-align: left;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
}

.pulse-card-title-btn:hover {
  color: var(--dou-color-primary);
}

.pulse-nested-prefix {
  margin-right: 3px;
  color: var(--dou-color-text-tertiary);
}

.pulse-reviewer-tag {
  flex: none;
  padding: 1px 6px;
  border-radius: 9999px;
  background: var(--pulse-review-soft);
  color: var(--dou-color-chart-6);
  font-size: 10px;
  font-weight: 500;
}

.pulse-card-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  font-size: 11px;
  color: var(--dou-color-text-tertiary);
}

.pulse-diff {
  display: inline-flex;
  gap: 4px;
}

.pulse-diff-add {
  color: var(--dou-color-success-text);
}

.pulse-diff-del {
  color: var(--dou-color-danger-text);
}

/* Mini pulse progress strip */
.pulse-mini-strip {
  position: relative;
  height: 8px;
  border-radius: 9999px;
  background: var(--dou-color-bg-muted);
  overflow: hidden;
}

.pulse-mini-seg {
  position: absolute;
  top: 0;
  bottom: 0;
  border-radius: 9999px;
}

.pulse-mini-seg.is-working {
  background: var(--dou-color-primary);
}

.pulse-mini-seg.is-reviewing {
  background: var(--pulse-review-fill);
}

.pulse-mini-seg.is-waiting_review {
  background: var(--pulse-review-soft);
  border: 1px dashed var(--dou-color-chart-6);
}

.pulse-mini-seg.is-waiting_user {
  background: var(--dou-color-warning);
}

.pulse-mini-seg.is-waiting_ci {
  background: var(--dou-color-chart-4);
}

.pulse-mini-seg.is-blocked {
  background: var(--dou-color-danger);
}

.pulse-mini-seg.is-queued {
  background: var(--dou-color-text-tertiary);
  opacity: 0.45;
}

.pulse-mini-seg.is-waiting,
.pulse-mini-seg.is-aside {
  background: var(--dou-color-border-strong);
}

.pulse-mini-seg.is-coord {
  background: var(--dou-color-text-secondary);
  opacity: 0.55;
}

.pulse-mini-return {
  position: absolute;
  top: 0;
  bottom: 0;
  width: 1.5px;
  background: color-mix(in srgb, var(--dou-color-primary) 60%, transparent);
}

.pulse-mini-now {
  position: absolute;
  top: 0;
  bottom: 0;
  width: 1.5px;
  background: var(--dou-color-primary);
}

.pulse-mini-meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  font-size: 11px;
  color: var(--dou-color-text-secondary);
  min-width: 0;
}

.pulse-mini-stage {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pulse-card-headline {
  font-size: 12px;
  line-height: 1.45;
  color: var(--dou-color-text-secondary);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  word-break: break-word;
}

.pulse-card-inline-actions {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding-top: 2px;
}

.pulse-card-ask {
  min-width: 0;
}

.pulse-card-quick-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding-top: 2px;
}

.pulse-narrow-completed {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.pulse-completed-toggle {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 8px 10px;
  border-radius: var(--dou-radius-control);
  border: 1px solid var(--dou-color-border-subtle);
  background: var(--dou-color-bg-surface);
  color: var(--dou-color-text-secondary);
  font: inherit;
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
}

.pulse-completed-icon {
  width: 14px;
  height: 14px;
  flex: none;
}

/* Key events feed */
.pulse-feed-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.pulse-feed-return-divider {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 4px 0;
  font-size: 11px;
  color: var(--dou-color-primary-text);
}

.pulse-feed-return-divider::before,
.pulse-feed-return-divider::after {
  content: '';
  flex: 1;
  height: 1px;
  border-top: 1px dashed color-mix(in srgb, var(--dou-color-primary) 50%, transparent);
}

.pulse-feed-row {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  width: 100%;
  padding: 9px 11px;
  border-radius: var(--dou-radius-container);
  border: 1px solid var(--dou-color-border);
  background: var(--dou-color-bg-surface);
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.pulse-feed-row:hover {
  border-color: var(--dou-color-border-strong);
}

.pulse-feed-time {
  flex: none;
  width: 40px;
  padding-top: 1px;
  font-size: 11px;
  color: var(--dou-color-text-tertiary);
}

.pulse-feed-dot {
  flex: none;
  width: 8px;
  height: 8px;
  margin-top: 5px;
  border-radius: 2px;
  transform: rotate(45deg);
  background: var(--dou-color-primary);
}

.pulse-feed-dot.is-success {
  background: var(--dou-color-success);
}

.pulse-feed-dot.is-warning {
  background: var(--dou-color-warning);
}

.pulse-feed-dot.is-danger {
  background: var(--dou-color-danger);
}

.pulse-feed-dot.is-review {
  background: var(--dou-color-chart-6);
}

.pulse-feed-dot.is-ci {
  background: var(--dou-color-chart-4);
}

.pulse-feed-body {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.pulse-feed-title {
  font-size: 12.5px;
  font-weight: 500;
  color: var(--dou-color-text);
  line-height: 1.4;
  word-break: break-word;
}

.pulse-feed-meta {
  font-size: 11px;
  color: var(--dou-color-text-tertiary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pulse-narrow-empty {
  display: flex;
  flex: 1;
  align-items: center;
  justify-content: center;
  padding: 28px 16px;
}

/* Badges & dots */
.pulse-badge {
  display: inline-flex;
  align-items: center;
  flex: none;
  padding: 1px 6px;
  border-radius: 9999px;
  font-size: 10.5px;
  font-weight: 600;
  line-height: 1.45;
}

.pulse-badge.is-primary {
  background: var(--dou-color-primary-soft);
  color: var(--dou-color-primary-text);
}

.pulse-badge.is-warning {
  background: var(--dou-color-warning-soft);
  color: var(--dou-color-warning-text);
}

.pulse-badge.is-danger {
  background: var(--dou-color-danger-soft);
  color: var(--dou-color-danger-text);
}

.pulse-badge.is-success {
  background: var(--dou-color-success-soft);
  color: var(--dou-color-success-text);
}

.pulse-badge.is-neutral {
  background: var(--dou-color-bg-muted);
  color: var(--dou-color-text-secondary);
}

.pulse-dot {
  width: 7px;
  height: 7px;
  border-radius: 9999px;
  flex: none;
  display: inline-block;
}

.pulse-dot.is-primary {
  background: var(--dou-color-primary);
  color: var(--dou-color-primary);
}

.pulse-dot.is-review {
  background: var(--dou-color-chart-6);
  color: var(--dou-color-chart-6);
}

.pulse-dot.is-ci {
  background: var(--dou-color-chart-4);
}

.pulse-dot.is-warning {
  background: var(--dou-color-warning);
}

.pulse-dot.is-danger {
  background: var(--dou-color-danger);
}

.pulse-dot.is-success {
  background: var(--dou-color-success);
}

.pulse-dot.is-neutral {
  background: var(--dou-color-text-tertiary);
}

.pulse-dot.is-pulse {
  animation: pulse-narrow-dot-ring 1.8s ease-out infinite;
}

@keyframes pulse-narrow-dot-ring {
  0% {
    box-shadow: 0 0 0 0 color-mix(in srgb, currentColor 50%, transparent);
  }
  70% {
    box-shadow: 0 0 0 6px color-mix(in srgb, currentColor 0%, transparent);
  }
  100% {
    box-shadow: 0 0 0 0 color-mix(in srgb, currentColor 0%, transparent);
  }
}

.pulse-mono {
  font-family: var(--dou-font-family-mono);
  font-variant-numeric: tabular-nums;
}
</style>
