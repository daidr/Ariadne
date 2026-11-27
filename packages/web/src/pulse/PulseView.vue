<script setup lang="ts">
import type {
  ConversationView,
  PulseLane,
  PulseMarker,
  PulseSnapshot,
  PulseSpan,
} from '@ariadne/core'
import { DouAlert, DouButton, DouEmpty, DouSpinner, DouSplitter } from 'dou-ui'
import { computed, onMounted, onUnmounted, ref, shallowRef, watch } from 'vue'
import { api, ApiError } from '../api'
import { coalescedRefresh } from '../coalesced-refresh'
import { t, tm } from '../i18n'
import { currentServerNow, synchronizeServerClock } from '../server-clock'
import {
  backMain,
  onLiveEvent,
  openMain,
  openProjectChanges,
  openProjectConversation,
  openProjectThread,
  openProjectTrace,
  openThread,
  refreshAllNeedsMe,
  state,
  threadsById,
} from '../store'
import type { WorkspaceLayoutMode } from '../workspace-layout'
import PulseCanvas from './PulseCanvas.vue'
import PulseInspector from './PulseInspector.vue'
import PulseLegend from './PulseLegend.vue'
import PulseNarrowView from './PulseNarrowView.vue'
import PulseToolbar from './PulseToolbar.vue'
import {
  buildPulseFeed,
  clamp,
  computeFetchBounds,
  computeLiveViewport,
  DEFAULT_LABEL_COL_WIDTH,
  filterAndPartitionLanes,
  isActiveLane,
  isNeedsLane,
  isPulseRelevantEvent,
  MAX_LABEL_COL_WIDTH,
  MIN_LABEL_COL_WIDTH,
  needsRefetch,
  panViewport,
  resolveLaneAttentions,
  resolveLaneThreadSummary,
  WINDOW_DURATION_MS,
  zoomViewport,
  type PulseDensity,
  type PulseFilter,
  type PulseGroupBy,
  type PulseLaneLines,
  type PulseNarrowTab,
  type PulseNodeMeta,
  type PulseScope,
  type PulseViewport,
  type PulseWindowKey,
} from './model'

const props = withDefaults(
  defineProps<{
    layoutMode?: WorkspaceLayoutMode
  }>(),
  { layoutMode: 'wide' },
)

const LIVE_REFRESH_MS = 450
const PAN_REFETCH_MS = 220
const PREFS_KEY = 'ariadne.pulse.prefs.v2'

interface PulsePrefs {
  density?: PulseDensity
  laneLines?: PulseLaneLines
  showConcurrency?: boolean
  showHandoffs?: boolean
  showReturnLine?: boolean
  showLegend?: boolean
  labelWidth?: number
}

function readPrefs(): PulsePrefs {
  try {
    const raw = localStorage.getItem(PREFS_KEY)
    if (!raw) return {}
    const parsed = JSON.parse(raw) as PulsePrefs
    return parsed && typeof parsed === 'object' ? parsed : {}
  } catch {
    return {}
  }
}

function writePrefs(prefs: PulsePrefs) {
  try {
    localStorage.setItem(PREFS_KEY, JSON.stringify(prefs))
  } catch {
    // Ignore storage errors in restricted environments.
  }
}

const initialPrefs = readPrefs()

const scope = ref<PulseScope>('project')
const narrowTab = ref<PulseNarrowTab>('cards')
const filter = ref<PulseFilter>('all')
const groupBy = ref<PulseGroupBy>('stage')
const windowKey = ref<PulseWindowKey>('1h')
const density = ref<PulseDensity>(initialPrefs.density === 'compact' ? 'compact' : 'comfortable')
const laneLines = ref<PulseLaneLines>(
  density.value === 'compact'
    ? 'single'
    : initialPrefs.laneLines === 'double'
      ? 'double'
      : 'single',
)
const showConcurrency = ref(initialPrefs.showConcurrency ?? false)
const showHandoffs = ref(initialPrefs.showHandoffs ?? false)
const showReturnLine = ref(initialPrefs.showReturnLine ?? true)
const showLegend = ref(initialPrefs.showLegend ?? false)
const labelWidth = ref(
  typeof initialPrefs.labelWidth === 'number' && Number.isFinite(initialPrefs.labelWidth)
    ? Math.round(clamp(initialPrefs.labelWidth, MIN_LABEL_COL_WIDTH, MAX_LABEL_COL_WIDTH))
    : DEFAULT_LABEL_COL_WIDTH,
)
const inspectorSplit = ref(72)
const showCompleted = ref(false)
const following = ref(true)
const hovering = ref(false)

const selectedLaneId = ref<string | null>(null)
const selectedSpanId = ref<number | null>(null)

const nowMs = ref(currentServerNow())
const viewport = ref<PulseViewport>(computeLiveViewport(nowMs.value, WINDOW_DURATION_MS['1h']))
const snapshot = shallowRef<PulseSnapshot | null>(null)
const loading = ref(true)
const problem = ref<string | null>(null)

let fetchedFrom = 0
let fetchedTo = 0
let viewportSynced = false
let controller = new AbortController()
let liveTimer: ReturnType<typeof setTimeout> | undefined
let needsMeTimer: ReturnType<typeof setTimeout> | undefined
let panTimer: ReturnType<typeof setTimeout> | undefined
let tickInterval: ReturnType<typeof setInterval> | undefined

const isNarrow = computed(() => props.layoutMode === 'narrow')
const drawerMode = computed<'docked' | 'right' | 'bottom'>(() => {
  if (props.layoutMode === 'narrow') return 'bottom'
  if (props.layoutMode === 'medium') return 'right'
  return 'docked'
})

watch(
  [density, laneLines, showConcurrency, showHandoffs, showReturnLine, showLegend, labelWidth],
  () => {
    writePrefs({
      density: density.value,
      laneLines: laneLines.value,
      showConcurrency: showConcurrency.value,
      showHandoffs: showHandoffs.value,
      showReturnLine: showReturnLine.value,
      showLegend: showLegend.value,
      labelWidth: labelWidth.value,
    })
  },
)

const currentProjectName = computed(
  () => state.projects.find((project) => project.id === state.currentId)?.name ?? null,
)

const threadNodes = computed(() => {
  const map = new Map<string, PulseNodeMeta>()
  const nodes = state.plan?.nodes ?? []
  nodes.forEach((node, order) => {
    for (const thread of node.threads) {
      if (!map.has(thread.id)) {
        map.set(thread.id, { nodeId: node.id, nodeTitle: node.title, order })
      }
    }
  })
  return map
})

const partitioned = computed(() =>
  filterAndPartitionLanes(
    snapshot.value?.lanes ?? [],
    filter.value,
    groupBy.value,
    threadNodes.value,
    scope.value,
    state.currentId,
  ),
)

const spansByConversation = computed(() => {
  const map = new Map<string, PulseSpan[]>()
  for (const span of snapshot.value?.spans ?? []) {
    const list = map.get(span.conversationId) ?? []
    list.push(span)
    map.set(span.conversationId, list)
  }
  return map
})

const markersByConversation = computed(() => {
  const map = new Map<string, PulseMarker[]>()
  for (const marker of snapshot.value?.markers ?? []) {
    const list = map.get(marker.conversationId) ?? []
    list.push(marker)
    map.set(marker.conversationId, list)
  }
  return map
})

const feedItems = computed(() =>
  buildPulseFeed(
    snapshot.value?.lanes ?? [],
    snapshot.value?.markers ?? [],
    viewport.value.viewStart,
    viewport.value.viewEnd,
  ),
)

const selectedLane = computed<PulseLane | null>(() => {
  if (!selectedLaneId.value) return null
  return snapshot.value?.lanes.find((lane) => lane.conversationId === selectedLaneId.value) ?? null
})

const selectedSpans = computed<PulseSpan[]>(() =>
  selectedLane.value
    ? (spansByConversation.value.get(selectedLane.value.conversationId) ?? [])
    : [],
)

const selectedMarkers = computed<PulseMarker[]>(() =>
  selectedLane.value
    ? (markersByConversation.value.get(selectedLane.value.conversationId) ?? [])
    : [],
)

const selectedAttentions = computed(() =>
  selectedLane.value
    ? resolveLaneAttentions(
        selectedLane.value,
        state.currentId,
        state.detail?.needsMe,
        state.allNeedsMe,
      )
    : [],
)

const selectedAttentionsLoading = computed(() =>
  Boolean(
    selectedLane.value &&
    (selectedLane.value.attentionId !== null || selectedLane.value.stage === 'answer') &&
    selectedAttentions.value.length === 0 &&
    state.allNeedsMeLoading,
  ),
)

const selectedThread = computed(() => {
  const lane = selectedLane.value
  if (!lane || lane.role === 'coordinator' || lane.projectId !== state.currentId) return null
  return threadsById.value.get(lane.conversationId) ?? null
})

const selectedThreadSummary = computed(() =>
  selectedLane.value
    ? resolveLaneThreadSummary(
        selectedLane.value,
        state.currentId,
        threadsById.value,
        state.allNeedsMe,
      )
    : null,
)

const selectedNodeMeta = computed<PulseNodeMeta | null>(() => {
  const lane = selectedLane.value
  if (!lane || lane.projectId !== state.currentId) return null
  return threadNodes.value.get(lane.conversationId) ?? null
})

const selectedReviewTarget = computed<PulseLane | null>(() => {
  const targetId = selectedLane.value?.reviewOf
  if (!targetId) return null
  return snapshot.value?.lanes.find((lane) => lane.conversationId === targetId) ?? null
})

const selectedLiveCommand = computed(() =>
  selectedLane.value ? (state.live[selectedLane.value.conversationId] ?? null) : null,
)

const liveWaits = computed(() => {
  const map = new Map<string, NonNullable<ConversationView['wait']>>()
  const coordConv = state.coordinator.conversation
  if (coordConv?.wait) map.set(coordConv.id, coordConv.wait)
  for (const [id, entry] of Object.entries(state.threads)) {
    if (entry.conversation?.wait) map.set(id, entry.conversation.wait)
  }
  return map
})

const selectedLiveWait = computed<ConversationView['wait']>(() =>
  selectedLane.value ? (liveWaits.value.get(selectedLane.value.conversationId) ?? null) : null,
)

const emptyWindow = computed(() => {
  const data = snapshot.value
  if (!data) return false
  return data.lanes.length === 0 && data.spans.length === 0
})

const coordinatorMatchesFilter = computed(() => {
  const coord = partitioned.value.coordinator
  if (!coord) return false
  if (filter.value === 'all') return true
  if (filter.value === 'needs') return isNeedsLane(coord)
  return isActiveLane(coord)
})

const showFilteredEmpty = computed(
  () =>
    filter.value !== 'all' &&
    partitioned.value.groups.length === 0 &&
    !coordinatorMatchesFilter.value,
)

const showQuietWindowHint = computed(
  () =>
    filter.value === 'all' &&
    partitioned.value.groups.length === 0 &&
    partitioned.value.completed.length === 0 &&
    (snapshot.value?.spans.length ?? 0) === 0,
)

const emptyConcurrency = { slots: 0, current: 0, peak: 0, points: [] }

async function syncAllNeedsMe() {
  try {
    await refreshAllNeedsMe()
  } catch {
    // Store reports or will refresh on the next event.
  }
}

const load = coalescedRefresh<never, void>(async () => {
  const requestScope = scope.value
  const projectId = requestScope === 'all' ? '' : (state.currentId ?? '')
  if (requestScope === 'project' && !projectId) {
    snapshot.value = null
    loading.value = false
    problem.value = null
    return
  }
  const { signal } = controller
  if (signal.aborted) return

  const currentNow = Math.max(nowMs.value, currentServerNow())
  const bounds = computeFetchBounds(viewport.value.viewStart, viewport.value.viewEnd, currentNow)
  try {
    const next = await api.getPulse({
      projectId,
      fromMs: bounds.fromMs,
      toMs: bounds.toMs,
      includeCompleted: true,
      signal,
    })
    if (
      signal.aborted ||
      scope.value !== requestScope ||
      (requestScope === 'project' && state.currentId !== projectId)
    ) {
      return
    }

    if (next.serverNowMs > 0) {
      synchronizeServerClock(String(next.serverNowMs))
      nowMs.value = Math.max(currentServerNow(), next.serverNowMs)
    } else {
      nowMs.value = currentServerNow()
    }

    if (!viewportSynced && following.value) {
      viewport.value = computeLiveViewport(nowMs.value, WINDOW_DURATION_MS[windowKey.value])
      viewportSynced = true
    }

    snapshot.value = next
    fetchedFrom = next.fromMs || bounds.fromMs
    fetchedTo = next.toMs || bounds.toMs
    problem.value = null

    if (
      selectedLaneId.value &&
      !next.lanes.some((lane) => lane.conversationId === selectedLaneId.value)
    ) {
      selectedLaneId.value = null
      selectedSpanId.value = null
    }
  } catch (error) {
    if (!signal.aborted) {
      problem.value = error instanceof ApiError ? tm(error.localized) : t('pulse.error')
    }
  } finally {
    if (!signal.aborted) loading.value = false
  }
})

function resetAndLoad() {
  controller.abort()
  controller = new AbortController()
  clearTimeout(liveTimer)
  clearTimeout(panTimer)
  snapshot.value = null
  fetchedFrom = 0
  fetchedTo = 0
  viewportSynced = false
  selectedLaneId.value = null
  selectedSpanId.value = null
  loading.value = true
  problem.value = null
  nowMs.value = currentServerNow()
  following.value = true
  viewport.value = computeLiveViewport(nowMs.value, WINDOW_DURATION_MS[windowKey.value])
  void load()
}

function scheduleLiveRefresh() {
  clearTimeout(liveTimer)
  liveTimer = setTimeout(() => void load(), LIVE_REFRESH_MS)
}

function scheduleNeedsMeRefresh() {
  clearTimeout(needsMeTimer)
  needsMeTimer = setTimeout(() => void syncAllNeedsMe(), LIVE_REFRESH_MS)
}

function schedulePanRefetchIfNeeded() {
  if (
    !needsRefetch(
      viewport.value.viewStart,
      viewport.value.viewEnd,
      fetchedFrom,
      fetchedTo,
      nowMs.value,
    )
  ) {
    return
  }
  clearTimeout(panTimer)
  panTimer = setTimeout(() => void load(), PAN_REFETCH_MS)
}

watch(
  () => state.currentId,
  (_id, previous) => {
    if (previous === undefined || scope.value === 'project') {
      resetAndLoad()
    }
  },
  { immediate: true },
)

watch(scope, (nextScope) => {
  resetAndLoad()
  if (nextScope === 'all') {
    void syncAllNeedsMe()
  }
})

watch(windowKey, (key) => {
  nowMs.value = Math.max(nowMs.value, currentServerNow())
  following.value = true
  viewport.value = computeLiveViewport(nowMs.value, WINDOW_DURATION_MS[key])
  schedulePanRefetchIfNeeded()
})

function snapToNow() {
  nowMs.value = Math.max(nowMs.value, currentServerNow())
  following.value = true
  viewport.value = computeLiveViewport(nowMs.value, WINDOW_DURATION_MS[windowKey.value])
  schedulePanRefetchIfNeeded()
}

function handleZoom(anchorRatio: number, factor: number) {
  following.value = false
  viewport.value = zoomViewport(
    viewport.value.viewStart,
    viewport.value.viewEnd,
    anchorRatio,
    factor,
    nowMs.value,
  )
  schedulePanRefetchIfNeeded()
}

function handlePan(deltaMs: number) {
  following.value = false
  viewport.value = panViewport(
    viewport.value.viewStart,
    viewport.value.viewEnd,
    deltaMs,
    nowMs.value,
  )
  schedulePanRefetchIfNeeded()
}

function handleSelect(laneId: string, spanId: number | null) {
  selectedLaneId.value = laneId
  selectedSpanId.value = spanId
  const lane = snapshot.value?.lanes.find((item) => item.conversationId === laneId)
  if (!lane || lane.projectId === state.currentId) return
  const missingCrossAttention =
    (lane.attentionId !== null || lane.stage === 'answer') &&
    resolveLaneAttentions(lane, state.currentId, state.detail?.needsMe, state.allNeedsMe).length ===
      0
  if (!state.allNeedsMe || missingCrossAttention) {
    void syncAllNeedsMe()
  }
}

function handleSelectLaneOnly(laneId: string) {
  handleSelect(laneId, null)
}

function clearSelection() {
  selectedLaneId.value = null
  selectedSpanId.value = null
}

function handleOpenLane(lane: PulseLane) {
  if (lane.role === 'coordinator') {
    if (lane.projectId && lane.projectId !== state.currentId) {
      void openProjectConversation(lane.projectId)
      return
    }
    openMain({ kind: 'conversation' })
    return
  }
  if (lane.projectId && lane.projectId !== state.currentId) {
    void openProjectThread(lane.projectId, lane.conversationId)
    return
  }
  void openThread(lane.conversationId)
}

function handleOpenDiff(lane: PulseLane) {
  if (lane.role === 'coordinator') return
  void openProjectChanges(lane.projectId, lane.conversationId)
}

function handleOpenTrace(lane: PulseLane) {
  void openProjectTrace(lane.projectId, lane.conversationId)
}

function handleMutated() {
  void load()
  if (
    scope.value === 'all' ||
    (selectedLane.value !== null && selectedLane.value.projectId !== state.currentId)
  ) {
    void syncAllNeedsMe()
  }
}

function expandTo24h() {
  windowKey.value = '24h'
}

function handleKeyDown(event: KeyboardEvent) {
  if (
    event.key === 'Escape' &&
    !event.defaultPrevented &&
    drawerMode.value === 'docked' &&
    selectedLaneId.value !== null
  ) {
    clearSelection()
  }
}

const stopLive = onLiveEvent((event) => {
  if (scope.value === 'project') {
    if (!state.currentId) return
    if (event.projectId && event.projectId !== state.currentId) return
  }
  if (isPulseRelevantEvent(event.type)) {
    scheduleLiveRefresh()
    if (
      scope.value === 'all' &&
      (event.type.startsWith('attention.') ||
        event.type.startsWith('thread.') ||
        event.type.startsWith('review.'))
    ) {
      scheduleNeedsMeRefresh()
    }
  }
})

watch(
  () => state.stream,
  (stream, previous) => {
    if (stream === 'open' && previous && previous !== 'open') {
      void load()
      if (scope.value === 'all') void syncAllNeedsMe()
    }
  },
)

onMounted(() => {
  window.addEventListener('keydown', handleKeyDown)
  tickInterval = setInterval(() => {
    const nextNow = Math.max(nowMs.value + 1_000, currentServerNow())
    const delta = nextNow - nowMs.value
    nowMs.value = nextNow
    if (following.value && !hovering.value && delta > 0) {
      const len = viewport.value.viewEnd - viewport.value.viewStart
      viewport.value = computeLiveViewport(nextNow, len)
    }
  }, 1_000)
})

onUnmounted(() => {
  window.removeEventListener('keydown', handleKeyDown)
  stopLive()
  clearTimeout(liveTimer)
  clearTimeout(needsMeTimer)
  clearTimeout(panTimer)
  if (tickInterval !== undefined) clearInterval(tickInterval)
  controller.abort()
})
</script>

<template>
  <section class="pulse-view" :class="{ 'is-narrow': isNarrow }" data-pulse-view>
    <PulseToolbar
      v-if="!isNarrow"
      v-model:scope="scope"
      v-model:filter="filter"
      v-model:group-by="groupBy"
      v-model:window-key="windowKey"
      v-model:density="density"
      v-model:lane-lines="laneLines"
      v-model:show-concurrency="showConcurrency"
      v-model:show-handoffs="showHandoffs"
      v-model:show-return-line="showReturnLine"
      v-model:show-legend="showLegend"
      :project-name="currentProjectName"
      :counts="partitioned.counts"
      :following="following"
      @back="backMain"
      @snap-to-now="snapToNow"
    />

    <div v-if="problem && snapshot" class="pulse-error-banner">
      <DouAlert tone="danger">
        {{ problem }}
        <template #action>
          <DouButton size="sm" variant="outline" @click="void load()">
            {{ t('conversation.retry') }}
          </DouButton>
        </template>
      </DouAlert>
    </div>

    <div v-if="loading && !snapshot" class="pulse-state-center">
      <DouSpinner />
    </div>

    <div v-else-if="problem && !snapshot" class="pulse-state-center">
      <DouAlert tone="danger">
        {{ problem }}
        <template #action>
          <div class="pulse-error-actions">
            <DouButton v-if="isNarrow" size="sm" variant="ghost" @click="backMain">
              {{ t('view.back') }}
            </DouButton>
            <DouButton size="sm" variant="outline" @click="void load()">
              {{ t('conversation.retry') }}
            </DouButton>
          </div>
        </template>
      </DouAlert>
    </div>

    <div v-else-if="!isNarrow && emptyWindow" class="pulse-state-center">
      <DouEmpty :description="t('pulse.empty.window')">
        <DouButton v-if="windowKey !== '24h'" size="sm" variant="soft" @click="expandTo24h">
          {{ t('pulse.empty.expandWindow') }}
        </DouButton>
      </DouEmpty>
    </div>

    <div v-else-if="snapshot" class="pulse-body">
      <PulseNarrowView
        v-if="isNarrow"
        v-model:scope="scope"
        v-model:tab="narrowTab"
        v-model:filter="filter"
        v-model:window-key="windowKey"
        v-model:show-completed="showCompleted"
        :partitioned="partitioned"
        :spans-by-conversation="spansByConversation"
        :markers-by-conversation="markersByConversation"
        :feed-items="feedItems"
        :concurrency="snapshot.concurrency ?? emptyConcurrency"
        :all-spans="snapshot.spans"
        :last-returned-at="snapshot.lastReturnedAt"
        :now-ms="nowMs"
        :view-start="viewport.viewStart"
        :view-end="viewport.viewEnd"
        :selected-lane-id="selectedLaneId"
        :threads="threadsById"
        :live-commands="state.live"
        :live-waits="liveWaits"
        :current-project-id="state.currentId"
        :current-needs-me="state.detail?.needsMe"
        :all-needs-me="state.allNeedsMe"
        @back="backMain"
        @select="handleSelect"
        @open-lane="handleOpenLane"
        @open-trace="handleOpenTrace"
        @mutated="handleMutated"
        @expand-window="expandTo24h"
      />

      <DouSplitter
        v-else
        v-model="inspectorSplit"
        :min="45"
        :max="selectedLane && drawerMode === 'docked' ? 82 : 100"
        :single-pane="selectedLane && drawerMode === 'docked' ? null : 'start'"
        class="pulse-splitter"
      >
        <template #start>
          <PulseCanvas
            v-model:show-completed="showCompleted"
            v-model:label-width="labelWidth"
            :partitioned="partitioned"
            :spans-by-conversation="spansByConversation"
            :markers-by-conversation="markersByConversation"
            :handoffs="snapshot.handoffs"
            :concurrency="snapshot.concurrency ?? emptyConcurrency"
            :all-spans="snapshot.spans"
            :last-returned-at="snapshot.lastReturnedAt"
            :now-ms="nowMs"
            :view-start="viewport.viewStart"
            :view-end="viewport.viewEnd"
            :density="density"
            :lane-lines="laneLines"
            :show-concurrency="showConcurrency"
            :show-handoffs="showHandoffs"
            :show-return-line="showReturnLine"
            :selected-lane-id="selectedLaneId"
            :selected-span-id="selectedSpanId"
            :scope="scope"
            :threads="threadsById"
            :live-commands="state.live"
            @toggle-concurrency="showConcurrency = !showConcurrency"
            @select="handleSelect"
            @open-lane="handleOpenLane"
            @zoom="handleZoom"
            @pan="handlePan"
            @reset-window="snapToNow"
            @hovering-change="hovering = $event"
          >
            <template #empty>
              <div v-if="showFilteredEmpty" class="pulse-inline-empty" data-pulse-inline-empty>
                <DouEmpty :description="t('pulse.empty.filtered')">
                  <DouButton size="sm" variant="soft" @click="filter = 'all'">
                    {{ t('pulse.empty.resetFilter') }}
                  </DouButton>
                </DouEmpty>
              </div>
              <div
                v-else-if="showQuietWindowHint"
                class="pulse-inline-empty"
                data-pulse-inline-empty
              >
                <DouEmpty :description="t('pulse.empty.window')">
                  <DouButton
                    v-if="windowKey !== '24h'"
                    size="sm"
                    variant="soft"
                    @click="expandTo24h"
                  >
                    {{ t('pulse.empty.expandWindow') }}
                  </DouButton>
                </DouEmpty>
              </div>
            </template>
          </PulseCanvas>
        </template>

        <template #end>
          <PulseInspector
            v-if="drawerMode === 'docked'"
            :open="Boolean(selectedLane)"
            drawer-mode="docked"
            :lane="selectedLane"
            :spans="selectedSpans"
            :markers="selectedMarkers"
            :selected-span-id="selectedSpanId"
            :view-start="viewport.viewStart"
            :view-end="viewport.viewEnd"
            :now-ms="nowMs"
            :attentions="selectedAttentions"
            :attentions-loading="selectedAttentionsLoading"
            :thread="selectedThread"
            :thread-summary="selectedThreadSummary"
            :node-meta="selectedNodeMeta"
            :review-target="selectedReviewTarget"
            :live-command="selectedLiveCommand"
            :live-wait="selectedLiveWait"
            @close="clearSelection"
            @open-lane="handleOpenLane"
            @open-diff="handleOpenDiff"
            @open-trace="handleOpenTrace"
            @select-lane="handleSelectLaneOnly"
            @mutated="handleMutated"
          />
        </template>
      </DouSplitter>

      <PulseInspector
        v-if="drawerMode !== 'docked'"
        :open="Boolean(selectedLane)"
        :drawer-mode="drawerMode"
        :lane="selectedLane"
        :spans="selectedSpans"
        :markers="selectedMarkers"
        :selected-span-id="selectedSpanId"
        :view-start="viewport.viewStart"
        :view-end="viewport.viewEnd"
        :now-ms="nowMs"
        :attentions="selectedAttentions"
        :attentions-loading="selectedAttentionsLoading"
        :thread="selectedThread"
        :thread-summary="selectedThreadSummary"
        :node-meta="selectedNodeMeta"
        :review-target="selectedReviewTarget"
        :live-command="selectedLiveCommand"
        :live-wait="selectedLiveWait"
        @close="clearSelection"
        @open-lane="handleOpenLane"
        @open-diff="handleOpenDiff"
        @open-trace="handleOpenTrace"
        @select-lane="handleSelectLaneOnly"
        @mutated="handleMutated"
      />
    </div>

    <PulseLegend v-if="!isNarrow && showLegend" />
  </section>
</template>

<style scoped>
.pulse-view {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-width: 0;
  min-height: 0;
  background: var(--dou-color-bg-canvas);
}

.pulse-body {
  display: flex;
  flex: 1;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}

.pulse-splitter {
  flex: 1;
  min-width: 0;
  min-height: 0;
}

.pulse-splitter :deep(.dou-splitter__pane:not([hidden])) {
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
}

.pulse-error-banner {
  flex: none;
  padding: 8px 14px 0;
}

.pulse-error-actions {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.pulse-state-center {
  display: flex;
  flex: 1;
  align-items: center;
  justify-content: center;
  padding: 24px;
}

.pulse-inline-empty {
  position: relative;
  z-index: 3;
  display: flex;
  justify-content: center;
  padding: 36px 24px;
  background: color-mix(in srgb, var(--dou-color-bg-canvas) 82%, transparent);
}
</style>
