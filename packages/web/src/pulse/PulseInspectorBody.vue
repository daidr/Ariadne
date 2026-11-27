<script setup lang="ts">
import type {
  AttentionView,
  ConversationView,
  LiveCommand,
  NeedsMeThreadSummary,
  PulseLane,
  PulseMarker,
  PulseSpan,
  ThreadView,
} from '@ariadne/core'
import { PlayIcon, RotateCwIcon } from '@dou-ui/icons'
import {
  DouAlert,
  DouButton,
  DouCard,
  DouCodeBlock,
  DouDescriptions,
  DouDescriptionsItem,
  DouLink,
  DouSpinner,
  DouStatusDot,
  DouText,
  DouTimeline,
  DouTimelineItem,
} from 'dou-ui'
import { computed, ref } from 'vue'
import AskBlock from '../blocks/AskBlock.vue'
import ChangeTotals from '../components/ChangeTotals.vue'
import PullRequestTag from '../components/PullRequestTag.vue'
import { locale, t } from '../i18n'
import { branchChanges } from '../review/merge-offered'
import { threadLine } from '../stage'
import { endAside, endWait, resumeThread, retryTurn } from '../store'
import {
  classifyMarkerTone,
  formatClockShort,
  formatDurationCompact,
  markerToneToDouTone,
  resolveSpanVisualKind,
  type PulseNodeMeta,
} from './model'

const props = defineProps<{
  lane: PulseLane
  spans: readonly PulseSpan[]
  markers: readonly PulseMarker[]
  selectedSpanId: number | null
  viewStart: number
  viewEnd: number
  nowMs: number
  attentions: readonly AttentionView[]
  attentionsLoading: boolean
  thread: ThreadView | null
  threadSummary: ThreadView | NeedsMeThreadSummary | null
  nodeMeta: PulseNodeMeta | null
  reviewTarget: PulseLane | null
  liveCommand: LiveCommand | null
  liveWait: ConversationView['wait']
}>()

const emit = defineEmits<{
  openLane: [lane: PulseLane]
  selectLane: [laneId: string]
  mutated: []
}>()

const acting = ref<'retry' | 'resume' | 'aside' | 'wake' | 'cancelWait' | null>(null)

const activeSpan = computed<PulseSpan | null>(() => {
  if (props.selectedSpanId !== null) {
    const found = props.spans.find((span) => span.id === props.selectedSpanId)
    if (found) return found
  }
  return props.spans.at(-1) ?? null
})

const spanStageText = computed(() => {
  const span = activeSpan.value
  if (!span) return null
  const kind = resolveSpanVisualKind(span, props.lane.role)
  const stageText = t(`pulse.stage.${kind}`)
  if (span.label && span.label !== stageText) return `${stageText} · ${span.label}`
  return stageText
})

const spanTimeRangeText = computed(() => {
  const span = activeSpan.value
  if (!span) return null
  const endAt = span.endedAt ?? props.nowMs
  const startText = formatClockShort(span.startedAt, props.viewStart, props.viewEnd, locale.value)
  const endText =
    span.endedAt === null
      ? t('pulse.tip.running')
      : formatClockShort(span.endedAt, props.viewStart, props.viewEnd, locale.value)
  const durText = formatDurationCompact(Math.max(0, endAt - span.startedAt))
  return `${startText} – ${endText} (${durText})`
})

const progressHeadline = computed(() => {
  if (props.lane.lastHeadline) return props.lane.lastHeadline
  if (props.thread) return threadLine(props.thread)
  return null
})

const changes = computed(() => (props.thread ? branchChanges(props.thread) : null))

const todoProgress = computed(() => {
  const progress = props.thread?.progress
  if (!progress || progress.total <= 0) return null
  return `${progress.done} / ${progress.total}`
})

const openSpan = computed<PulseSpan | null>(
  () => props.spans.findLast((span) => span.endedAt === null) ?? null,
)

const failedChecks = computed(() => props.thread?.pullRequest?.checks.failed ?? [])

const stoppedInfo = computed(() => props.threadSummary?.stopped ?? null)

const isStopped = computed(() => Boolean(stoppedInfo.value) || props.lane.stage === 'stopped')

const stoppedTitle = computed(() => {
  if (stoppedInfo.value) {
    return t(
      stoppedInfo.value.by === 'user' ? 'thread.stoppedByUser' : 'thread.stoppedByCoordinator',
      { reason: stoppedInfo.value.reason },
    )
  }
  return t('thread.stage.stopped')
})

const stoppedFallbackReason = computed(() => {
  if (stoppedInfo.value) return null
  if (openSpan.value?.stage === 'halted' && openSpan.value.label) return openSpan.value.label
  return props.lane.lastHeadline
})

const showRetryCard = computed(
  () =>
    props.attentions.length === 0 &&
    !isStopped.value &&
    (props.lane.stage === 'halted' ||
      props.lane.stage === 'silent' ||
      props.lane.stage === 'blocked' ||
      (failedChecks.value.length > 0 &&
        props.lane.stage !== 'working' &&
        props.lane.stage !== 'queued' &&
        props.lane.stage !== 'aside' &&
        !props.lane.completed &&
        openSpan.value?.stage !== 'aside')),
)

const retryDesc = computed(() => {
  if (openSpan.value?.stage === 'halted' && openSpan.value.label) return openSpan.value.label
  return props.lane.lastHeadline
})

const showWaitCard = computed(
  () =>
    props.attentions.length === 0 &&
    !isStopped.value &&
    !showRetryCard.value &&
    (props.lane.stage === 'waiting' ||
      Boolean(props.liveWait) ||
      (openSpan.value?.stage === 'waiting' && Boolean(openSpan.value.label))),
)

const waitTitle = computed(() => {
  if (props.liveWait?.until) {
    const time = formatClockShort(
      props.liveWait.until,
      props.nowMs,
      props.liveWait.until,
      locale.value,
    )
    return t('wait.until', { time })
  }
  return t('pulse.inspector.waiting')
})

const waitNote = computed(
  () =>
    props.liveWait?.note ||
    (openSpan.value?.stage === 'waiting' && openSpan.value.label ? openSpan.value.label : null),
)

const showAsideCard = computed(
  () =>
    props.attentions.length === 0 &&
    !isStopped.value &&
    props.lane.role !== 'coordinator' &&
    (props.lane.stage === 'aside' ||
      openSpan.value?.stage === 'aside' ||
      Boolean(props.threadSummary?.aside)),
)

const asideTitle = computed(() =>
  props.threadSummary?.aside?.due ? t('aside.due') : t('pulse.inspector.aside'),
)

const asideReason = computed(
  () =>
    props.threadSummary?.aside?.reason ||
    (openSpan.value?.stage === 'aside' && openSpan.value.label ? openSpan.value.label : null) ||
    props.lane.lastHeadline,
)

const liveCmdText = computed(() => {
  const raw = props.liveCommand?.command ?? props.lane.liveCommand
  return raw?.trim() || null
})

const liveTailText = computed(() => {
  const raw = props.liveCommand?.tail ?? props.lane.liveTail
  return raw?.trim() || null
})

const liveCommandBlock = computed(() => {
  if (!liveCmdText.value) return ''
  return liveTailText.value
    ? `$ ${liveCmdText.value}\n${liveTailText.value}`
    : `$ ${liveCmdText.value}`
})

const eventTrail = computed(() =>
  props.markers
    .filter((marker) => marker.kind !== 'user_returned')
    .toSorted((a, b) => b.at - a.at || b.seq - a.seq)
    .map((marker) => {
      const title = t(`pulse.marker.${marker.kind}`)
      const detail = marker.label && marker.label !== title ? marker.label : null
      return {
        key: `${marker.seq}:${marker.kind}`,
        atText: formatClockShort(marker.at, props.viewStart, props.viewEnd, locale.value),
        tone: markerToneToDouTone(classifyMarkerTone(marker)),
        text: detail ? `${title} · ${detail}` : title,
      }
    }),
)

async function runAction(kind: NonNullable<typeof acting.value>, fn: () => Promise<unknown>) {
  if (acting.value) return
  acting.value = kind
  try {
    await fn()
    emit('mutated')
  } finally {
    acting.value = null
  }
}

function handleRetry() {
  void runAction('retry', () => retryTurn(props.lane.conversationId))
}

function handleResume() {
  void runAction('resume', () => resumeThread(props.lane.conversationId))
}

function handleEndAside() {
  void runAction('aside', () => endAside(props.lane.conversationId))
}

function handleWakeWait() {
  void runAction('wake', () => endWait(props.lane.conversationId, 'now'))
}

function handleCancelWait() {
  void runAction('cancelWait', () => endWait(props.lane.conversationId, 'cancel'))
}
</script>

<template>
  <div class="pulse-insp-sections">
    <!-- Pending attentions (in-place approval / choice / form link) -->
    <template v-if="attentions.length">
      <div
        v-for="att in attentions"
        :key="att.id"
        class="pulse-insp-attention"
        :data-pulse-attention-id="att.id"
      >
        <DouAlert v-if="att.block.type === 'form'" tone="warning" :title="att.block.title">
          <template #action>
            <DouButton size="sm" tone="primary" @click="emit('openLane', lane)">
              {{ t('block.fill') }}
            </DouButton>
          </template>
        </DouAlert>
        <div v-else class="pulse-ask-wrap" data-pulse-inline-ask>
          <AskBlock
            :attention-id="att.id"
            :block="att.block"
            :answer="null"
            @answered="emit('mutated')"
          />
        </div>
      </div>
    </template>
    <DouCard v-else-if="lane.attentionId !== null && attentionsLoading" padding="sm">
      <div class="pulse-card-loading" role="status">
        <DouSpinner size="sm" />
      </div>
    </DouCard>
    <DouAlert
      v-else-if="lane.attentionId !== null"
      tone="warning"
      :title="t('pulse.stage.waiting_user')"
    >
      <template v-if="lane.lastHeadline">{{ lane.lastHeadline }}</template>
      <template #action>
        <DouButton size="sm" tone="primary" @click="emit('openLane', lane)">
          {{
            lane.role === 'coordinator'
              ? t('pulse.inspector.openCoordinator')
              : t('pulse.inspector.openThread')
          }}
        </DouButton>
      </template>
    </DouAlert>

    <!-- Stopped thread -->
    <DouAlert v-if="isStopped" tone="warning" :title="stoppedTitle" data-pulse-stopped-card>
      <template v-if="stoppedFallbackReason">{{ stoppedFallbackReason }}</template>
      <template #action>
        <DouButton
          size="sm"
          variant="outline"
          data-pulse-resume
          :loading="acting === 'resume'"
          @click="handleResume"
        >
          <template #leading><PlayIcon aria-hidden="true" /></template>
          {{ t('thread.resume') }}
        </DouButton>
      </template>
    </DouAlert>

    <!-- Blocked / halted / silent / CI failed retry card -->
    <DouAlert
      v-if="showRetryCard"
      tone="danger"
      :title="
        failedChecks.length ? t('pulse.inspector.ciFailed') : t('pulse.inspector.actionNeeded')
      "
      data-pulse-retry-card
    >
      <ul v-if="failedChecks.length" class="pulse-checks-list">
        <li v-for="check in failedChecks" :key="check" class="pulse-mono">
          {{ check }}
        </li>
      </ul>
      <div v-if="retryDesc" class="pulse-alert-desc">{{ retryDesc }}</div>
      <template #action>
        <DouButton
          size="sm"
          variant="outline"
          data-pulse-retry
          :loading="acting === 'retry'"
          @click="handleRetry"
        >
          <template #leading><RotateCwIcon aria-hidden="true" /></template>
          {{ t('conversation.retry') }}
        </DouButton>
      </template>
    </DouAlert>

    <!-- Timed wait card -->
    <DouAlert v-if="showWaitCard" tone="neutral" :title="waitTitle" data-pulse-wait-card>
      <template v-if="waitNote">{{ waitNote }}</template>
      <template #action>
        <div class="pulse-alert-actions">
          <DouButton
            size="sm"
            variant="outline"
            data-pulse-wake
            :loading="acting === 'wake'"
            @click="handleWakeWait"
          >
            {{ t('wait.now') }}
          </DouButton>
          <DouButton
            size="sm"
            variant="ghost"
            data-pulse-cancel-wait
            :loading="acting === 'cancelWait'"
            @click="handleCancelWait"
          >
            {{ t('wait.cancel') }}
          </DouButton>
        </div>
      </template>
    </DouAlert>

    <!-- Set aside card -->
    <DouAlert v-if="showAsideCard" tone="neutral" :title="asideTitle" data-pulse-aside-card>
      <template v-if="asideReason">{{ asideReason }}</template>
      <template #action>
        <DouButton
          size="sm"
          variant="outline"
          data-pulse-end-aside
          :loading="acting === 'aside'"
          @click="handleEndAside"
        >
          {{ t('aside.done') }}
        </DouButton>
      </template>
    </DouAlert>

    <!-- Live foreground command -->
    <DouCard v-if="liveCmdText" padding="sm" data-pulse-live-cmd>
      <template #header>
        <div class="pulse-live-head">
          <DouStatusDot tone="primary" size="sm" ripple aria-hidden="true" />
          <DouText size="sm" weight="medium">{{ t('pulse.inspector.liveCommand') }}</DouText>
        </div>
      </template>
      <DouCodeBlock :code="liveCommandBlock" wrap streaming :max-height="220" />
    </DouCard>

    <!-- Selected span & lane metadata -->
    <DouCard padding="sm" data-pulse-span-detail>
      <template #header>
        <DouText size="sm" weight="medium">{{ t('pulse.inspector.selectedSpan') }}</DouText>
      </template>
      <DouDescriptions
        class="pulse-descriptions"
        :columns="1"
        layout="horizontal"
        variant="plain"
        :bordered="false"
        label-width="68px"
      >
        <DouDescriptionsItem v-if="lane.projectName" :label="t('pulse.inspector.project')">
          {{ lane.projectName }}
        </DouDescriptionsItem>
        <DouDescriptionsItem v-if="nodeMeta" :label="t('pulse.inspector.planNode')">
          {{ nodeMeta.nodeTitle }}
        </DouDescriptionsItem>
        <DouDescriptionsItem v-if="spanStageText" :label="t('pulse.inspector.stage')">
          {{ spanStageText }}
        </DouDescriptionsItem>
        <DouDescriptionsItem v-if="spanTimeRangeText" :label="t('pulse.inspector.timeRange')">
          <span class="pulse-mono">{{ spanTimeRangeText }}</span>
        </DouDescriptionsItem>
        <DouDescriptionsItem v-if="progressHeadline" :label="t('pulse.inspector.status')">
          {{ progressHeadline }}
        </DouDescriptionsItem>
        <DouDescriptionsItem v-if="reviewTarget" :label="t('pulse.inspector.reviewOf')">
          <DouLink underline="hover" @click="emit('selectLane', reviewTarget.conversationId)">
            {{ reviewTarget.title || reviewTarget.conversationId }}
          </DouLink>
        </DouDescriptionsItem>
        <DouDescriptionsItem v-if="thread?.pullRequest" :label="t('pulse.inspector.pullRequest')">
          <PullRequestTag :thread="thread" pill />
        </DouDescriptionsItem>
        <DouDescriptionsItem v-if="changes" :label="t('pulse.inspector.diff')">
          <ChangeTotals :totals="changes" :thread-id="lane.conversationId" />
        </DouDescriptionsItem>
        <DouDescriptionsItem v-if="todoProgress" :label="t('pulse.inspector.todoProgress')">
          <span class="pulse-mono">{{ todoProgress }}</span>
        </DouDescriptionsItem>
      </DouDescriptions>
    </DouCard>

    <!-- Lane milestone events -->
    <div v-if="eventTrail.length" class="pulse-events-section" data-pulse-inspector-events>
      <DouText size="sm" variant="tertiary" weight="medium">
        {{ t('pulse.inspector.events') }}
      </DouText>
      <DouTimeline class="pulse-timeline">
        <DouTimelineItem
          v-for="ev in eventTrail"
          :key="ev.key"
          :time="ev.atText"
          :title="ev.text"
          :tone="ev.tone"
        />
      </DouTimeline>
    </div>
  </div>
</template>

<style scoped>
.pulse-insp-sections {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
}

.pulse-ask-wrap {
  min-width: 0;
}

.pulse-card-loading {
  display: flex;
  justify-content: center;
  padding: 8px 0;
}

.pulse-alert-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.pulse-alert-desc {
  margin-top: 2px;
  word-break: break-word;
}

.pulse-checks-list {
  margin: 0;
  padding-left: 16px;
  font-size: 12px;
  color: var(--dou-color-danger-text);
  line-height: 1.5;
}

.pulse-live-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.pulse-descriptions {
  --dou-descriptions-gap-x: 10px;
  --dou-descriptions-gap-y: 8px;
  font-size: var(--dou-font-size-sm);
}

.pulse-events-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}

.pulse-timeline {
  font-size: var(--dou-font-size-sm);
}

.pulse-mono {
  font-family: var(--dou-font-family-mono);
  font-variant-numeric: tabular-nums;
}
</style>
