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
import { DouButton, DouLink, DouSpinner } from 'dou-ui'
import { computed, ref } from 'vue'
import AskBlock from '../blocks/AskBlock.vue'
import PullRequestTag from '../components/PullRequestTag.vue'
import { locale, t } from '../i18n'
import { branchChanges } from '../review/merge-offered'
import { threadLine } from '../stage'
import { endAside, endWait, resumeThread, retryTurn } from '../store'
import {
  classifyMarkerTone,
  formatClockShort,
  formatDurationCompact,
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
        tone: classifyMarkerTone(marker),
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
        <div v-if="att.block.type === 'form'" class="pulse-card is-warning">
          <div class="pulse-card-head">
            <span class="pulse-dot is-warning" aria-hidden="true" />
            <span class="pulse-card-title">{{ att.block.title }}</span>
          </div>
          <div class="pulse-card-actions">
            <DouButton size="sm" tone="primary" @click="emit('openLane', lane)">
              {{ t('block.fill') }}
            </DouButton>
          </div>
        </div>
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
    <div v-else-if="lane.attentionId !== null" class="pulse-card is-warning">
      <div v-if="attentionsLoading" class="pulse-card-loading" role="status">
        <DouSpinner size="sm" />
      </div>
      <template v-else>
        <div class="pulse-card-head">
          <span class="pulse-dot is-warning" aria-hidden="true" />
          <span class="pulse-card-title">{{ t('pulse.stage.waiting_user') }}</span>
        </div>
        <div v-if="lane.lastHeadline" class="pulse-card-desc">{{ lane.lastHeadline }}</div>
        <div class="pulse-card-actions">
          <DouButton size="sm" tone="primary" @click="emit('openLane', lane)">
            {{
              lane.role === 'coordinator'
                ? t('pulse.inspector.openCoordinator')
                : t('pulse.inspector.openThread')
            }}
          </DouButton>
        </div>
      </template>
    </div>

    <!-- Stopped thread -->
    <div v-if="isStopped" class="pulse-card is-warning" data-pulse-stopped-card>
      <div class="pulse-card-head">
        <span class="pulse-dot is-warning" aria-hidden="true" />
        <span class="pulse-card-title">{{ stoppedTitle }}</span>
      </div>
      <div v-if="stoppedFallbackReason" class="pulse-card-desc">{{ stoppedFallbackReason }}</div>
      <div class="pulse-card-actions">
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
      </div>
    </div>

    <!-- Blocked / halted / silent / CI failed retry card -->
    <div v-if="showRetryCard" class="pulse-card is-danger" data-pulse-retry-card>
      <div class="pulse-card-head">
        <span class="pulse-dot is-danger" aria-hidden="true" />
        <span class="pulse-card-title">
          {{
            failedChecks.length ? t('pulse.inspector.ciFailed') : t('pulse.inspector.actionNeeded')
          }}
        </span>
      </div>
      <ul v-if="failedChecks.length" class="pulse-checks-list">
        <li v-for="check in failedChecks" :key="check" class="pulse-mono">
          {{ check }}
        </li>
      </ul>
      <div v-if="retryDesc" class="pulse-card-desc">{{ retryDesc }}</div>
      <div class="pulse-card-actions">
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
      </div>
    </div>

    <!-- Timed wait card -->
    <div v-if="showWaitCard" class="pulse-card" data-pulse-wait-card>
      <div class="pulse-card-head">
        <span class="pulse-dot is-neutral" aria-hidden="true" />
        <span class="pulse-card-title">{{ waitTitle }}</span>
      </div>
      <div v-if="waitNote" class="pulse-card-desc">{{ waitNote }}</div>
      <div class="pulse-card-actions">
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
    </div>

    <!-- Set aside card -->
    <div v-if="showAsideCard" class="pulse-card" data-pulse-aside-card>
      <div class="pulse-card-head">
        <span class="pulse-dot is-neutral" aria-hidden="true" />
        <span class="pulse-card-title">{{ asideTitle }}</span>
      </div>
      <div v-if="asideReason" class="pulse-card-desc">
        {{ asideReason }}
      </div>
      <div class="pulse-card-actions">
        <DouButton
          size="sm"
          variant="outline"
          data-pulse-end-aside
          :loading="acting === 'aside'"
          @click="handleEndAside"
        >
          {{ t('aside.done') }}
        </DouButton>
      </div>
    </div>

    <!-- Live foreground command -->
    <div v-if="liveCmdText" class="pulse-card" data-pulse-live-cmd>
      <div class="pulse-card-head">
        <span class="pulse-dot is-primary is-pulse" aria-hidden="true" />
        <span class="pulse-card-title">{{ t('pulse.inspector.liveCommand') }}</span>
      </div>
      <div class="pulse-cmd-box pulse-mono">$ {{ liveCmdText }}</div>
      <pre v-if="liveTailText" class="pulse-cmd-tail pulse-mono">{{ liveTailText }}</pre>
    </div>

    <!-- Selected span & lane metadata -->
    <div class="pulse-card" data-pulse-span-detail>
      <div class="pulse-card-head">
        <span class="pulse-card-title">{{ t('pulse.inspector.selectedSpan') }}</span>
      </div>
      <dl class="pulse-kv-grid">
        <template v-if="lane.projectName">
          <dt>{{ t('pulse.inspector.project') }}</dt>
          <dd>{{ lane.projectName }}</dd>
        </template>
        <template v-if="nodeMeta">
          <dt>{{ t('pulse.inspector.planNode') }}</dt>
          <dd>{{ nodeMeta.nodeTitle }}</dd>
        </template>
        <template v-if="spanStageText">
          <dt>{{ t('pulse.inspector.stage') }}</dt>
          <dd>{{ spanStageText }}</dd>
        </template>
        <template v-if="spanTimeRangeText">
          <dt>{{ t('pulse.inspector.timeRange') }}</dt>
          <dd class="pulse-mono">{{ spanTimeRangeText }}</dd>
        </template>
        <template v-if="progressHeadline">
          <dt>{{ t('pulse.inspector.status') }}</dt>
          <dd>{{ progressHeadline }}</dd>
        </template>
        <template v-if="reviewTarget">
          <dt>{{ t('pulse.inspector.reviewOf') }}</dt>
          <dd>
            <DouLink underline="hover" @click="emit('selectLane', reviewTarget.conversationId)">
              {{ reviewTarget.title || reviewTarget.conversationId }}
            </DouLink>
          </dd>
        </template>
        <template v-if="thread?.pullRequest">
          <dt>{{ t('pulse.inspector.pullRequest') }}</dt>
          <dd><PullRequestTag :thread="thread" pill /></dd>
        </template>
        <template v-if="changes">
          <dt>{{ t('pulse.inspector.diff') }}</dt>
          <dd class="pulse-mono pulse-diff">
            <span class="pulse-diff-add">+{{ changes.added }}</span>
            <span class="pulse-diff-del">-{{ changes.removed }}</span>
          </dd>
        </template>
        <template v-if="todoProgress">
          <dt>{{ t('pulse.inspector.todoProgress') }}</dt>
          <dd class="pulse-mono">{{ todoProgress }}</dd>
        </template>
      </dl>
    </div>

    <!-- Lane milestone events -->
    <div v-if="eventTrail.length" class="pulse-events-section" data-pulse-inspector-events>
      <div class="pulse-section-label">{{ t('pulse.inspector.events') }}</div>
      <div class="pulse-mini-events">
        <div
          v-for="ev in eventTrail"
          :key="ev.key"
          class="pulse-mini-event"
          :class="`is-${ev.tone}`"
        >
          <span class="pulse-mini-event-time pulse-mono">{{ ev.atText }}</span>
          <span class="pulse-mini-event-text">{{ ev.text }}</span>
        </div>
      </div>
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

.pulse-card {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
  border-radius: var(--dou-radius-container);
  border: 1px solid var(--dou-color-border);
  background: var(--dou-color-bg-canvas);
  min-width: 0;
}

.pulse-card.is-warning {
  border-color: color-mix(in srgb, var(--dou-color-warning) 38%, var(--dou-color-border));
  background: var(--dou-color-warning-soft);
}

.pulse-card.is-danger {
  border-color: color-mix(in srgb, var(--dou-color-danger) 38%, var(--dou-color-border));
  background: var(--dou-color-danger-soft);
}

.pulse-card-loading {
  display: flex;
  justify-content: center;
  padding: 8px 0;
}

.pulse-card-head {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.pulse-card-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--dou-color-text);
  line-height: 1.4;
  min-width: 0;
  word-break: break-word;
}

.pulse-card-desc {
  font-size: 12px;
  line-height: 1.5;
  color: var(--dou-color-text-secondary);
  word-break: break-word;
}

.pulse-card-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 2px;
}

.pulse-checks-list {
  margin: 0;
  padding-left: 16px;
  font-size: 11px;
  line-height: 1.5;
  color: var(--dou-color-danger-text);
}

.pulse-cmd-box {
  padding: 8px 10px;
  border-radius: var(--dou-radius-control);
  border: 1px solid var(--dou-color-border);
  background: var(--dou-color-bg-muted);
  color: var(--dou-color-text);
  font-size: 11px;
  line-height: 1.5;
  word-break: break-all;
}

.pulse-cmd-tail {
  margin: 0;
  padding: 8px 10px;
  max-height: 120px;
  overflow: auto;
  border-radius: var(--dou-radius-control);
  border: 1px solid var(--dou-color-border-subtle);
  background: var(--dou-color-bg-surface);
  color: var(--dou-color-text-secondary);
  font-size: 11px;
  line-height: 1.45;
  white-space: pre-wrap;
  word-break: break-word;
}

.pulse-kv-grid {
  display: grid;
  grid-template-columns: 74px minmax(0, 1fr);
  gap: 6px 10px;
  margin: 0;
  font-size: 12px;
  line-height: 1.45;
}

.pulse-kv-grid dt {
  color: var(--dou-color-text-tertiary);
  font-weight: 400;
}

.pulse-kv-grid dd {
  margin: 0;
  color: var(--dou-color-text);
  min-width: 0;
  word-break: break-word;
}

.pulse-diff {
  display: inline-flex;
  gap: 6px;
}

.pulse-diff-add {
  color: var(--dou-color-success-text);
}

.pulse-diff-del {
  color: var(--dou-color-danger-text);
}

.pulse-events-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.pulse-section-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--dou-color-text-secondary);
}

.pulse-mini-events {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.pulse-mini-event {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 6px 8px;
  border-radius: var(--dou-radius-control);
  border-left: 2px solid var(--dou-color-primary);
  background: var(--dou-color-bg-muted);
  font-size: 12px;
  line-height: 1.4;
}

.pulse-mini-event.is-success {
  border-left-color: var(--dou-color-success);
}

.pulse-mini-event.is-warning {
  border-left-color: var(--dou-color-warning);
}

.pulse-mini-event.is-danger {
  border-left-color: var(--dou-color-danger);
}

.pulse-mini-event.is-review {
  border-left-color: var(--dou-color-chart-6);
}

.pulse-mini-event.is-ci {
  border-left-color: var(--dou-color-chart-4);
}

.pulse-mini-event-time {
  font-size: 11px;
  color: var(--dou-color-text-tertiary);
}

.pulse-mini-event-text {
  color: var(--dou-color-text);
  word-break: break-word;
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

.pulse-dot.is-warning {
  background: var(--dou-color-warning);
}

.pulse-dot.is-danger {
  background: var(--dou-color-danger);
}

.pulse-dot.is-neutral {
  background: var(--dou-color-text-tertiary);
}

.pulse-dot.is-pulse {
  animation: pulse-insp-dot-ring 1.8s ease-out infinite;
}

@keyframes pulse-insp-dot-ring {
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
