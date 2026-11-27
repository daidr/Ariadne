<script setup lang="ts">
import type {
  AllNeedsMeResult,
  AttentionView,
  ChangeTotals as ChangeTotalsData,
  ConversationView,
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
  CornerDownRightIcon,
  FolderIcon,
  ListTreeIcon,
  PlayIcon,
  RotateCwIcon,
} from '@dou-ui/icons'
import {
  DouButton,
  DouCard,
  DouCheckTag,
  DouCheckTagGroup,
  DouDivider,
  DouEmpty,
  DouSegmentedControl,
  DouStatusDot,
  DouTag,
  DouText,
  type CheckTagValue,
} from 'dou-ui'
import { computed, ref } from 'vue'
import AskBlock from '../blocks/AskBlock.vue'
import ChangeTotals from '../components/ChangeTotals.vue'
import { locale, t } from '../i18n'
import { branchChanges } from '../review/merge-offered'
import { endAside, endWait, resumeThread, retryTurn } from '../store'
import {
  countLiveSlotOccupancy,
  formatClockShort,
  formatDurationCompact,
  isActiveLane,
  isNeedsLane,
  liveCommandLabel,
  markerToneToDouTone,
  percentOf,
  PULSE_WINDOW_KEYS,
  resolveLaneAttentions,
  resolveLaneStatusDot,
  resolveLaneThreadSummary,
  resolveSpanToneGroup,
  resolveSpanVisualKind,
  type PulseFeedItem,
  type PulseFilter,
  type PulseLaneGroup,
  type PulseNarrowTab,
  type PulsePartitionedLanes,
  type PulseScope,
  type PulseSpanToneGroup,
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
  liveWaits?: ReadonlyMap<string, NonNullable<ConversationView['wait']>>
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

type CardActionKind = 'resume' | 'retry' | 'aside' | 'wake' | 'cancelWait'
const acting = ref<{ laneId: string; kind: CardActionKind } | null>(null)

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

function onFilterChange(value: CheckTagValue | CheckTagValue[] | null) {
  if (value === 'all' || value === 'needs' || value === 'active') {
    filter.value = value
  }
}

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
  toneGroup: PulseSpanToneGroup
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
    const visualKind = resolveSpanVisualKind(span, lane.role)
    out.push({
      id: span.id,
      visualKind,
      toneGroup: resolveSpanToneGroup(visualKind),
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

function laneDot(lane: PulseLane) {
  return resolveLaneStatusDot(lane)
}

function latestSpanOf(lane: PulseLane): PulseSpan | null {
  const list = props.spansByConversation.get(lane.conversationId) ?? []
  return list.at(-1) ?? null
}

function openSpanOf(lane: PulseLane): PulseSpan | null {
  const list = props.spansByConversation.get(lane.conversationId) ?? []
  return list.findLast((span) => span.endedAt === null) ?? null
}

function laneStageSummary(lane: PulseLane): { label: string; duration: string | null } {
  const liveCmd = liveCommandLabel(lane, props.liveCommands, 64)
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
  if (lane.stage === 'stopped') {
    return { tone: 'warning', text: t('thread.stage.stopped') }
  }
  if (lane.stage === 'blocked' || lane.stage === 'halted' || lane.stage === 'silent') {
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

function laneMeta(lane: PulseLane): {
  pr: string | null
  diff: ChangeTotalsData | null
  progress: string | null
  project: string | null
} {
  const thread = props.threads.get(lane.conversationId)
  const pr = thread?.pullRequest?.number ? `#${thread.pullRequest.number}` : null
  const diff = thread ? branchChanges(thread) : null
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
  return resolveLaneThreadSummary(lane, props.currentProjectId, props.threads, props.allNeedsMe)
}

function isLaneStopped(lane: PulseLane): boolean {
  return Boolean(laneSummary(lane)?.stopped) || lane.stage === 'stopped'
}

function needsLaneRetry(lane: PulseLane): boolean {
  if (isLaneStopped(lane)) return false
  if (lane.stage === 'halted' || lane.stage === 'silent' || lane.stage === 'blocked') return true
  const failed = props.threads.get(lane.conversationId)?.pullRequest?.checks.failed.length ?? 0
  return (
    failed > 0 &&
    lane.stage !== 'working' &&
    lane.stage !== 'queued' &&
    lane.stage !== 'aside' &&
    !lane.completed &&
    openSpanOf(lane)?.stage !== 'aside'
  )
}

function isLaneWaiting(lane: PulseLane): boolean {
  if (isLaneStopped(lane) || needsLaneRetry(lane)) return false
  if (lane.stage === 'waiting' || props.liveWaits?.has(lane.conversationId)) return true
  const open = openSpanOf(lane)
  return open?.stage === 'waiting' && Boolean(open.label)
}

function isLaneAside(lane: PulseLane): boolean {
  if (lane.role === 'coordinator' || isLaneStopped(lane)) return false
  return (
    lane.stage === 'aside' ||
    openSpanOf(lane)?.stage === 'aside' ||
    Boolean(laneSummary(lane)?.aside)
  )
}

function isCardActing(laneId: string, kind: CardActionKind): boolean {
  return acting.value?.laneId === laneId && acting.value?.kind === kind
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

async function runCardAction(laneId: string, kind: CardActionKind, fn: () => Promise<unknown>) {
  if (acting.value) return
  acting.value = { laneId, kind }
  try {
    await fn()
    emit('mutated')
  } finally {
    acting.value = null
  }
}

const showCoordinatorCard = computed(() => {
  const coord = props.partitioned.coordinator
  if (!coord) return false
  if (filter.value === 'all') return true
  if (filter.value === 'needs') return isNeedsLane(coord)
  return isActiveLane(coord)
})

const showFilteredEmpty = computed(
  () =>
    filter.value !== 'all' && props.partitioned.groups.length === 0 && !showCoordinatorCard.value,
)

const showCardsEmpty = computed(
  () =>
    filter.value === 'all' &&
    props.partitioned.groups.length === 0 &&
    props.partitioned.completed.length === 0 &&
    (!showCoordinatorCard.value || props.allSpans.length === 0),
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
        <DouTag
          size="sm"
          round
          :tone="isSlotsFull ? 'warning' : 'primary'"
          class="pulse-mono"
          data-pulse-narrow-slots
        >
          <template v-if="liveActiveSlots > 0" #leading>
            <DouStatusDot
              :tone="isSlotsFull ? 'warning' : 'primary'"
              size="sm"
              ripple
              aria-hidden="true"
            />
          </template>
          {{ slotPillText }}
        </DouTag>
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

      <DouCheckTagGroup
        v-if="tab === 'cards'"
        :model-value="filter"
        size="sm"
        round
        class="pulse-narrow-filters"
        @update:model-value="onFilterChange"
      >
        <DouCheckTag value="all" tone="primary" data-pulse-filter="all">
          <span>{{ t('pulse.filter.all') }}</span>
          <span class="pulse-filter-count">{{ partitioned.counts.all }}</span>
        </DouCheckTag>
        <DouCheckTag value="needs" tone="warning" data-pulse-filter="needs">
          <template v-if="filter !== 'needs'" #leading>
            <DouStatusDot tone="warning" size="sm" aria-hidden="true" />
          </template>
          <span>{{ t('pulse.filter.needs') }}</span>
          <span class="pulse-filter-count">{{ partitioned.counts.needs }}</span>
        </DouCheckTag>
        <DouCheckTag value="active" tone="primary" data-pulse-filter="active">
          <template v-if="filter !== 'active'" #leading>
            <DouStatusDot tone="primary" size="sm" aria-hidden="true" />
          </template>
          <span>{{ t('pulse.filter.active') }}</span>
          <span class="pulse-filter-count">{{ partitioned.counts.active }}</span>
        </DouCheckTag>
      </DouCheckTagGroup>
    </div>

    <!-- Tab 1: Pulse cards -->
    <div v-if="tab === 'cards'" class="pulse-narrow-scroll" data-pulse-narrow-cards>
      <!-- Pinned coordinator card in single-project mode -->
      <DouCard
        v-if="showCoordinatorCard && partitioned.coordinator"
        as="article"
        interactive
        padding="sm"
        class="pulse-card-item is-coordinator"
        :class="{
          'is-needs':
            partitioned.coordinator.stage === 'answer' ||
            partitioned.coordinator.attentionId !== null,
          'is-blocked': partitioned.coordinator.stage === 'halted',
          'is-selected': selectedLaneId === partitioned.coordinator.conversationId,
        }"
        :data-lane-id="partitioned.coordinator.conversationId"
        :data-lane-needs="isNeedsLane(partitioned.coordinator) ? 'true' : undefined"
        @click="emit('select', partitioned.coordinator.conversationId, null)"
      >
        <div class="pulse-card-stack">
          <div class="pulse-card-head">
            <DouStatusDot
              :tone="laneDot(partitioned.coordinator).tone"
              :ripple="laneDot(partitioned.coordinator).ripple"
              size="sm"
              aria-hidden="true"
            />
            <button
              type="button"
              class="pulse-card-title-btn"
              @click.stop="emit('openLane', partitioned.coordinator)"
            >
              {{ laneTitle(partitioned.coordinator) }}
            </button>
            <DouTag
              v-if="laneBadge(partitioned.coordinator)"
              size="sm"
              round
              :tone="laneBadge(partitioned.coordinator)!.tone"
              class="pulse-mono"
            >
              {{ laneBadge(partitioned.coordinator)!.text }}
            </DouTag>
          </div>
          <div class="pulse-mini-strip" aria-hidden="true">
            <div
              v-for="seg in miniSpansForLane(partitioned.coordinator)"
              :key="seg.id"
              class="pulse-mini-seg"
              :class="[
                `is-${seg.visualKind}`,
                `is-tone-${seg.toneGroup}`,
                { 'is-running': seg.running },
              ]"
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
            v-if="
              partitioned.coordinator.lastHeadline &&
              !liveCommandLabel(partitioned.coordinator, liveCommands)
            "
            class="pulse-card-headline"
          >
            {{ partitioned.coordinator.lastHeadline }}
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
          <div
            v-else-if="needsLaneRetry(partitioned.coordinator)"
            class="pulse-card-quick-row"
            @click.stop
          >
            <DouButton
              size="sm"
              variant="outline"
              data-pulse-retry
              :loading="isCardActing(partitioned.coordinator.conversationId, 'retry')"
              @click="
                runCardAction(partitioned.coordinator.conversationId, 'retry', () =>
                  retryTurn(partitioned.coordinator!.conversationId),
                )
              "
            >
              <template #leading><RotateCwIcon aria-hidden="true" /></template>
              {{ t('conversation.retry') }}
            </DouButton>
            <DouButton
              size="sm"
              variant="ghost"
              data-pulse-open-trace
              @click="emit('openTrace', partitioned.coordinator)"
            >
              <template #leading><ListTreeIcon aria-hidden="true" /></template>
              {{ t('pulse.inspector.openTrace') }}
            </DouButton>
          </div>
          <div
            v-else-if="isLaneWaiting(partitioned.coordinator)"
            class="pulse-card-quick-row"
            @click.stop
          >
            <DouButton
              size="sm"
              variant="outline"
              data-pulse-wake
              :loading="isCardActing(partitioned.coordinator.conversationId, 'wake')"
              @click="
                runCardAction(partitioned.coordinator.conversationId, 'wake', () =>
                  endWait(partitioned.coordinator!.conversationId, 'now'),
                )
              "
            >
              {{ t('wait.now') }}
            </DouButton>
            <DouButton
              size="sm"
              variant="ghost"
              data-pulse-cancel-wait
              :loading="isCardActing(partitioned.coordinator.conversationId, 'cancelWait')"
              @click="
                runCardAction(partitioned.coordinator.conversationId, 'cancelWait', () =>
                  endWait(partitioned.coordinator!.conversationId, 'cancel'),
                )
              "
            >
              {{ t('wait.cancel') }}
            </DouButton>
          </div>
        </div>
      </DouCard>

      <!-- Grouped cards -->
      <section
        v-for="group in partitioned.groups"
        :key="group.key"
        class="pulse-narrow-group"
        :data-pulse-group="group.key"
      >
        <div class="pulse-narrow-group-head">
          <FolderIcon v-if="group.kind === 'project'" class="pulse-group-icon" aria-hidden="true" />
          <DouStatusDot
            v-else-if="group.kind === 'needs' || group.kind === 'active'"
            :tone="group.kind === 'needs' ? 'warning' : 'primary'"
            size="sm"
            aria-hidden="true"
          />
          <span class="pulse-group-title">{{ groupHeaderTitle(group) }}</span>
          <DouTag v-if="group.kind === 'project' && group.isCurrent" size="sm" round tone="primary">
            {{ t('pulse.group.projectCurrent') }}
          </DouTag>
          <DouTag
            size="sm"
            round
            class="pulse-mono"
            :tone="
              group.kind === 'needs' ? 'warning' : group.kind === 'active' ? 'primary' : 'neutral'
            "
          >
            {{ group.rows.length }}
          </DouTag>
        </div>

        <DouCard
          v-for="row in group.rows"
          :key="row.lane.conversationId"
          as="article"
          interactive
          padding="sm"
          class="pulse-card-item"
          :class="{
            'is-coordinator': row.lane.role === 'coordinator',
            'is-needs':
              row.lane.stage === 'answer' ||
              row.lane.stage === 'stopped' ||
              row.lane.attentionId !== null,
            'is-blocked':
              row.lane.stage === 'blocked' ||
              row.lane.stage === 'halted' ||
              row.lane.stage === 'silent',
            'is-selected': selectedLaneId === row.lane.conversationId,
          }"
          :data-lane-id="row.lane.conversationId"
          :data-lane-needs="isNeedsLane(row.lane) ? 'true' : undefined"
          @click="emit('select', row.lane.conversationId, null)"
        >
          <div class="pulse-card-stack">
            <div class="pulse-card-head">
              <CornerDownRightIcon
                v-if="row.nested"
                :size="13"
                class="pulse-nested-prefix"
                aria-hidden="true"
              />
              <DouStatusDot
                :tone="laneDot(row.lane).tone"
                :ripple="laneDot(row.lane).ripple"
                size="sm"
                aria-hidden="true"
              />
              <button
                type="button"
                class="pulse-card-title-btn"
                @click.stop="emit('openLane', row.lane)"
              >
                {{ laneTitle(row.lane) }}
              </button>
              <DouTag v-if="row.lane.role === 'reviewer'" size="sm" round tone="primary">
                {{ t('pulse.narrow.reviewerTag') }}
              </DouTag>
              <DouTag
                v-if="laneBadge(row.lane)"
                size="sm"
                round
                :tone="laneBadge(row.lane)!.tone"
                class="pulse-mono"
              >
                {{ laneBadge(row.lane)!.text }}
              </DouTag>
            </div>

            <div
              v-if="laneMeta(row.lane).pr || laneMeta(row.lane).diff || laneMeta(row.lane).progress"
              class="pulse-card-meta pulse-mono"
            >
              <span v-if="laneMeta(row.lane).pr">{{ laneMeta(row.lane).pr }}</span>
              <ChangeTotals
                v-if="laneMeta(row.lane).diff"
                :totals="laneMeta(row.lane).diff!"
                :thread-id="row.lane.conversationId"
              />
              <span v-if="laneMeta(row.lane).progress">{{ laneMeta(row.lane).progress }}</span>
            </div>

            <div class="pulse-mini-strip" aria-hidden="true">
              <div
                v-for="seg in miniSpansForLane(row.lane)"
                :key="seg.id"
                class="pulse-mini-seg"
                :class="[
                  `is-${seg.visualKind}`,
                  `is-tone-${seg.toneGroup}`,
                  { 'is-running': seg.running },
                ]"
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
            <div
              v-if="laneAttentions(row.lane).length"
              class="pulse-card-inline-actions"
              @click.stop
            >
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

            <!-- Quick retry / resume / wait / aside action row -->
            <div v-else-if="isLaneStopped(row.lane)" class="pulse-card-quick-row" @click.stop>
              <DouButton
                size="sm"
                variant="outline"
                data-pulse-resume
                :loading="isCardActing(row.lane.conversationId, 'resume')"
                @click="
                  runCardAction(row.lane.conversationId, 'resume', () =>
                    resumeThread(row.lane.conversationId),
                  )
                "
              >
                <template #leading><PlayIcon aria-hidden="true" /></template>
                {{ t('thread.resume') }}
              </DouButton>
            </div>
            <div v-else-if="needsLaneRetry(row.lane)" class="pulse-card-quick-row" @click.stop>
              <DouButton
                size="sm"
                variant="outline"
                data-pulse-retry
                :loading="isCardActing(row.lane.conversationId, 'retry')"
                @click="
                  runCardAction(row.lane.conversationId, 'retry', () =>
                    retryTurn(row.lane.conversationId),
                  )
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
            <div v-else-if="isLaneWaiting(row.lane)" class="pulse-card-quick-row" @click.stop>
              <DouButton
                size="sm"
                variant="outline"
                data-pulse-wake
                :loading="isCardActing(row.lane.conversationId, 'wake')"
                @click="
                  runCardAction(row.lane.conversationId, 'wake', () =>
                    endWait(row.lane.conversationId, 'now'),
                  )
                "
              >
                {{ t('wait.now') }}
              </DouButton>
              <DouButton
                size="sm"
                variant="ghost"
                data-pulse-cancel-wait
                :loading="isCardActing(row.lane.conversationId, 'cancelWait')"
                @click="
                  runCardAction(row.lane.conversationId, 'cancelWait', () =>
                    endWait(row.lane.conversationId, 'cancel'),
                  )
                "
              >
                {{ t('wait.cancel') }}
              </DouButton>
            </div>
            <div v-else-if="isLaneAside(row.lane)" class="pulse-card-quick-row" @click.stop>
              <DouButton
                size="sm"
                variant="outline"
                data-pulse-end-aside
                :loading="isCardActing(row.lane.conversationId, 'aside')"
                @click="
                  runCardAction(row.lane.conversationId, 'aside', () =>
                    endAside(row.lane.conversationId),
                  )
                "
              >
                {{ t('aside.done') }}
              </DouButton>
            </div>
          </div>
        </DouCard>
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
        <DouButton
          variant="ghost"
          size="sm"
          class="pulse-completed-toggle"
          data-pulse-completed-toggle
          :aria-expanded="showCompleted"
          @click="showCompleted = !showCompleted"
        >
          <template #leading>
            <ChevronDownIcon v-if="showCompleted" aria-hidden="true" />
            <ChevronRightIcon v-else aria-hidden="true" />
          </template>
          {{ t('pulse.completed.summary', { count: partitioned.completed.length }) }}
        </DouButton>

        <template v-if="showCompleted">
          <DouCard
            v-for="row in partitioned.completed"
            :key="row.lane.conversationId"
            as="article"
            interactive
            padding="sm"
            class="pulse-card-item is-completed"
            :class="{ 'is-selected': selectedLaneId === row.lane.conversationId }"
            :data-lane-id="row.lane.conversationId"
            data-lane-completed="true"
            @click="emit('select', row.lane.conversationId, null)"
          >
            <div class="pulse-card-stack">
              <div class="pulse-card-head">
                <CornerDownRightIcon
                  v-if="row.nested"
                  :size="13"
                  class="pulse-nested-prefix"
                  aria-hidden="true"
                />
                <DouStatusDot
                  :tone="laneDot(row.lane).tone"
                  :ripple="laneDot(row.lane).ripple"
                  size="sm"
                  aria-hidden="true"
                />
                <button
                  type="button"
                  class="pulse-card-title-btn"
                  @click.stop="emit('openLane', row.lane)"
                >
                  {{ laneTitle(row.lane) }}
                </button>
                <DouTag v-if="laneMeta(row.lane).project" size="sm" round tone="neutral">
                  {{ laneMeta(row.lane).project }}
                </DouTag>
              </div>
              <div class="pulse-mini-strip" aria-hidden="true">
                <div
                  v-for="seg in miniSpansForLane(row.lane)"
                  :key="seg.id"
                  class="pulse-mini-seg"
                  :class="[`is-${seg.visualKind}`, `is-tone-${seg.toneGroup}`]"
                  :style="{ left: `${seg.leftPct}%`, width: `${seg.widthPct}%` }"
                />
              </div>
            </div>
          </DouCard>
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
          <DouDivider
            v-if="index === returnDividerIndex"
            dashed
            class="pulse-feed-return-divider"
            data-pulse-feed-return
          >
            <span class="pulse-mono">{{ returnDividerText }}</span>
          </DouDivider>
          <DouCard
            as="button"
            type="button"
            interactive
            padding="sm"
            class="pulse-feed-row"
            :data-feed-lane-id="item.conversationId"
            @click="emit('select', item.conversationId, null)"
          >
            <span class="pulse-feed-row-inner">
              <span class="pulse-feed-time pulse-mono">{{ formatFeedTime(item.at) }}</span>
              <DouStatusDot
                :tone="markerToneToDouTone(item.tone)"
                size="sm"
                class="pulse-feed-dot"
                aria-hidden="true"
              />
              <span class="pulse-feed-body">
                <span class="pulse-feed-title">{{ feedText(item) }}</span>
                <span class="pulse-feed-meta">
                  {{ item.laneRole === 'coordinator' ? t('coordinator.title') : item.laneTitle }}
                  <template v-if="scope === 'all' && item.projectName">
                    · {{ item.projectName }}
                  </template>
                </span>
              </span>
            </span>
          </DouCard>
        </template>
        <DouDivider
          v-if="returnDividerIndex === feedItems.length"
          dashed
          class="pulse-feed-return-divider"
          data-pulse-feed-return
        >
          <span class="pulse-mono">{{ returnDividerText }}</span>
        </DouDivider>
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
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  background: var(--dou-color-bg-canvas);
  overflow: hidden;
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
}

.pulse-filter-count {
  margin-left: 4px;
  font-variant-numeric: tabular-nums;
  opacity: 0.85;
}

.pulse-mono {
  font-family: var(--dou-font-family-mono);
  font-variant-numeric: tabular-nums;
}

.pulse-narrow-scroll {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 10px;
  min-height: 0;
  padding: 10px 12px 24px;
  overflow-y: auto;
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

.pulse-card-item {
  cursor: pointer;
}

.pulse-card-item.is-coordinator {
  background: color-mix(in srgb, var(--dou-color-bg-muted) 35%, var(--dou-color-bg-surface));
}

.pulse-card-item.is-needs {
  box-shadow: inset 3px 0 0 var(--dou-color-warning);
}

.pulse-card-item.is-blocked {
  box-shadow: inset 3px 0 0 var(--dou-color-danger);
}

.pulse-card-item.is-selected {
  border-color: var(--dou-color-primary);
}

.pulse-card-item.is-completed {
  opacity: 0.78;
}

.pulse-card-stack {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}

.pulse-card-head {
  display: flex;
  align-items: center;
  gap: 7px;
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
  color: var(--dou-color-primary-text);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.pulse-nested-prefix {
  flex: none;
  color: var(--dou-color-text-tertiary);
}

.pulse-card-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  font-size: 11.5px;
  color: var(--dou-color-text-tertiary);
}

/* Mini timeline strip aligned with desktop 4-group quiet palette */
.pulse-mini-strip {
  position: relative;
  height: 10px;
  border-radius: 5px;
  background: var(--dou-color-bg-muted);
  overflow: hidden;
}

.pulse-mini-seg {
  position: absolute;
  top: 0;
  bottom: 0;
  border-radius: 3px;
  border: 1px solid transparent;
}

.pulse-mini-seg.is-tone-active {
  background: var(--dou-color-primary-soft);
  border-color: color-mix(in srgb, var(--dou-color-primary) 50%, transparent);
}

.pulse-mini-seg.is-tone-attention {
  background: var(--dou-color-warning-soft);
  border-color: color-mix(in srgb, var(--dou-color-warning) 58%, transparent);
}

.pulse-mini-seg.is-tone-danger {
  background: var(--dou-color-danger-soft);
  border-color: color-mix(in srgb, var(--dou-color-danger) 58%, transparent);
}

.pulse-mini-seg.is-tone-passive {
  background: color-mix(in srgb, var(--dou-color-border-strong) 35%, transparent);
  border: 1px dashed var(--dou-color-border-strong);
}

.pulse-mini-seg.is-coord {
  background: color-mix(in srgb, var(--dou-color-neutral) 10%, var(--dou-color-bg-surface));
  border-color: var(--dou-color-border-strong);
}

.pulse-mini-seg.is-running::after {
  content: '';
  position: absolute;
  top: 1px;
  bottom: 1px;
  right: 1px;
  width: 2.5px;
  border-radius: 2px;
  background: var(--dou-color-primary);
  opacity: 0.8;
}

.pulse-mini-return {
  position: absolute;
  top: 0;
  bottom: 0;
  width: 0;
  border-left: 1.5px dashed color-mix(in srgb, var(--dou-color-primary) 65%, transparent);
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
  font-size: 11.5px;
  color: var(--dou-color-text-secondary);
}

.pulse-mini-stage {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pulse-card-headline {
  font-size: 12px;
  color: var(--dou-color-text-tertiary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pulse-card-inline-actions {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.pulse-card-ask {
  min-width: 0;
}

.pulse-card-quick-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}

.pulse-narrow-empty {
  display: flex;
  justify-content: center;
  padding: 28px 12px;
}

.pulse-narrow-completed {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.pulse-completed-toggle {
  width: 100%;
  justify-content: flex-start;
  color: var(--dou-color-text-secondary);
}

/* Feed list */
.pulse-feed-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.pulse-feed-return-divider {
  font-size: 11px;
  color: var(--dou-color-primary-text);
}

.pulse-feed-row {
  width: 100%;
  text-align: left;
  cursor: pointer;
}

.pulse-feed-row-inner {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  width: 100%;
}

.pulse-feed-time {
  flex: none;
  font-size: 11px;
  color: var(--dou-color-text-tertiary);
  line-height: 1.5;
}

.pulse-feed-dot {
  margin-top: 5px;
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
  word-break: break-word;
}

.pulse-feed-meta {
  font-size: 11.5px;
  color: var(--dou-color-text-tertiary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
