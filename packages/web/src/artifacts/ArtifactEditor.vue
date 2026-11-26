<script setup lang="ts">
import type {
  ArtifactCommentAction,
  ArtifactCommentAnchor,
  ArtifactCommentThreadView,
  ArtifactVersionView,
  ArtifactView,
  DocumentAskAnswerBaseline,
} from '@ariadne/core'
import type * as Y from 'yjs'
import { DouButton, DouInput, DouPopover, DouText, DouTooltip, type CommentAction } from 'dou-ui'
import {
  ArrowLeftIcon,
  ArrowRightIcon,
  CheckIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  HistoryIcon,
  MoreHorizontalIcon,
  PauseIcon,
  PencilIcon,
  PlayIcon,
  RotateCcwIcon,
  SendIcon,
  UndoIcon,
} from '@dou-ui/icons'
import {
  computed,
  effectScope,
  nextTick,
  onBeforeUnmount,
  provide,
  ref,
  shallowRef,
  watch,
  type EffectScope,
} from 'vue'
import { defineAsyncView } from '../components/async-view'
import { api, ApiError } from '../api'
import { t } from '../i18n'
import {
  artifactNavigation,
  navigateArtifactInPlace,
  report,
  state,
  stepArtifactNavigation,
} from '../store'
import ViewActions from '../full/ViewActions.vue'
import type { ArtifactNavigationTarget } from './artifact-history'
import ArtifactHistory from './ArtifactHistory.vue'
import CommentList from './CommentList.vue'
import type { DecisionGroupSummary } from './decision-groups'
import {
  isAskAnswerOnlyTransaction,
  snapshotDocumentAsks,
  summarizeDocumentAsks,
  type DocumentAskSummary,
} from './document-asks'
import type { AskBlockProps } from './document-block-data'
import {
  clearPendingArtifactFeedback,
  clearPendingPrototypeTweakRollback,
  getPendingArtifactFeedback,
  getPendingPrototypeTweakRollbacks,
  pendingPrototypeRollbackRevision,
  setPendingArtifactFeedback,
  setPendingPrototypeTweakRollback,
} from './feedback-pending'
import { kindIcon } from './kinds'
import {
  PROTOTYPE_TWEAKS_BRIDGE_KEY,
  readDocPrototypeTweakValues,
  rollbackDocPrototypeTweakValues,
  type PrototypeTweaksPreviewEntry,
  type PrototypeTweaksSubmitOptions,
} from './prototype-bridge'
import { areTweakValuesEqual, type PrototypeTweakValues } from './prototype-tweaks'
import { useSharedArtifact, type SharedArtifact } from './shared'

/** `place` is where it is shown: in a tab on the right side, or full screen. */
const props = defineProps<{ id: string; place: 'side' | 'full' }>()

// Each kind has its own dou-ui package; load only the one in use.
const bodies = {
  document: defineAsyncView(() => import('./DocumentBody.vue')),
  whiteboard: defineAsyncView(() => import('./WhiteboardBody.vue')),
  dashboard: defineAsyncView(() => import('./DashboardBody.vue')),
}

const artifact = ref<ArtifactView | null>(null)
const versions = shallowRef<ArtifactVersionView[]>([])
const menu = ref<'actions' | 'history' | null>(null)
const menuOpen = computed({
  get: () => menu.value !== null,
  set: (open) => (menu.value = open ? 'actions' : null),
})
const historyOpen = computed(() => menu.value === 'history')
const historyStatus = ref<'loading' | 'ready' | 'error'>('loading')
const comments = ref<ArtifactCommentThreadView[]>([])
/** The comment thread open in the text. */
const activeComment = ref<string | null>(null)
const body = ref<{
  quotes?: () => Record<string, string>
  anchored?: () => Set<string>
  revealComment?: (id: string) => void
  markedPictures?: () => Promise<Blob[]>
  markHeard?: () => void
  getHeard?: () => unknown
  setHeard?: (snapshot: unknown) => void
  currentSnapshot?: () => unknown
  applyRecommendedPicks?: () => number
  setPrototypeTweakValues?: (
    nodeId: string,
    tweakValues: PrototypeTweakValues | undefined,
  ) => boolean
  currentAskSnapshot?: () => DocumentAskAnswerBaseline[]
} | null>(null)
/** The newest version; an older one is shown read-only. */
const latest = ref(0)
const saving = ref(false)
const switchingVersion = ref(false)
const renaming = ref(false)
const title = ref('')
const titleInput = ref<InstanceType<typeof DouInput> | null>(null)

const artifactSummary = computed(() => state.artifacts.find((a) => a.id === props.id) ?? null)
const displayKind = computed(() => artifact.value?.kind ?? artifactSummary.value?.kind ?? null)
const displayTitle = computed(() => artifact.value?.title ?? artifactSummary.value?.title ?? '')

const nav = computed(() => artifactNavigation(props.place, props.id))
const canBack = computed(() => Boolean(nav.value.back))
const canForward = computed(() => Boolean(nav.value.forward))
const showNav = computed(() => canBack.value || canForward.value)
const titleById = (id: string | undefined) =>
  id ? (state.artifacts.find((a) => a.id === id)?.title ?? '') : ''
const backLabel = computed(() => {
  const targetTitle = titleById(nav.value.back?.id)
  return targetTitle ? t('artifact.nav.backTo', { title: targetTitle }) : t('artifact.nav.back')
})
const forwardLabel = computed(() => {
  const targetTitle = titleById(nav.value.forward?.id)
  return targetTitle
    ? t('artifact.nav.forwardTo', { title: targetTitle })
    : t('artifact.nav.forward')
})

function goBack() {
  stepArtifactNavigation(props.place, props.id, -1)
}

function goForward() {
  stepArtifactNavigation(props.place, props.id, 1)
}

function onNavigateArtifact(target: ArtifactNavigationTarget) {
  navigateArtifactInPlace({
    place: props.place,
    fromId: props.id,
    toId: target.artifactId,
    focusNodeId: target.focusNodeId,
  })
}

/** The newest version is edited together: changes go out as they are made. */
const viewingOld = computed(() => !!artifact.value && artifact.value.version !== latest.value)
const historyNeighbors = computed(() => {
  const index = versions.value.findIndex(({ version }) => version === artifact.value?.version)
  return {
    older: index >= 0 ? versions.value[index + 1]?.version : undefined,
    // The newest version opens the live editor through the separate return action.
    newer: index > 1 ? versions.value[index - 1]?.version : undefined,
  }
})

/** What the editor starts from: the version shown. */
const bodyContent = ref('')

function show(next: ArtifactView) {
  artifact.value = next
  bodyContent.value = next.content
}

let viewRequest = 0

async function load(version?: number) {
  const request = ++viewRequest
  const baselineEpoch = askBaselineEpoch
  const id = props.id
  try {
    const detail = await api.getArtifact(id, { version, versions: version !== undefined })
    if (request !== viewRequest || id !== props.id) return
    if (version !== undefined) versions.value = detail.versions
    else if (baselineEpoch === askBaselineEpoch) {
      askBaseline.value =
        detail.artifact.kind === 'document' ? (detail.askBaseline?.asks ?? null) : null
    }
    comments.value = detail.comments
    latest.value = detail.versions[0]?.version ?? detail.artifact.version
    show(detail.artifact)
  } catch (error) {
    if (request === viewRequest && id === props.id) report(error)
  }
}

async function refreshAskBaseline(id: string) {
  const baselineEpoch = askBaselineEpoch
  try {
    const detail = await api.getArtifact(id, { versions: false })
    if (props.id !== id || viewingOld.value || baselineEpoch !== askBaselineEpoch) return
    if (detail.artifact.kind === 'document') {
      askBaseline.value = detail.askBaseline?.asks ?? null
    }
  } catch {
    // Keep existing local ask baseline if sync fails.
  }
}

async function switchVersion(version?: number) {
  if (switchingVersion.value) return
  menu.value = null
  switchingVersion.value = true
  try {
    await load(version)
  } finally {
    switchingVersion.value = false
  }
}

const historyRetryTick = ref(0)
function retryHistory() {
  historyRetryTick.value++
}

watch([historyOpen, historyRetryTick], async ([open], _, onCleanup) => {
  if (!open) return
  const controller = new AbortController()
  onCleanup(() => controller.abort())
  historyStatus.value = 'loading'
  try {
    const page = await api.listArtifactVersions(props.id, { signal: controller.signal })
    if (controller.signal.aborted) return
    versions.value = page.versions
    historyStatus.value = 'ready'
  } catch {
    if (!controller.signal.aborted) historyStatus.value = 'error'
  }
})

/**
 * What the user does here reaches the agent either when they click send/submit or after a
 * 15-second idle countdown (paused while active decision groups await explicit submission or
 * when the user pauses / switches away).
 */
const QUIET_MS = 15_000
const WITHDRAW_WINDOW_MS = 60_000
let quietTimer: ReturnType<typeof setInterval> | null = null
let quietDeadline = 0
let sentRoundTimer: ReturnType<typeof setInterval> | null = null
let sentRoundDeadline = 0
const waiting = ref(false)
const quietPaused = ref(false)
const quietSeconds = ref(0)
const withdrawSeconds = ref(0)
const pendingChanges = ref(0)
const pendingNotes = ref(0)
/** A round is being sent with pictures, which takes a moment (tracked per artifact ID). */
const sendingArtifacts = ref<ReadonlySet<string>>(new Set())
const withdrawingArtifacts = ref<ReadonlySet<string>>(new Set())
const sending = computed(() => sendingArtifacts.value.has(props.id))
const withdrawing = computed(() => withdrawingArtifacts.value.has(props.id))
const sendingTweakNodes = ref<ReadonlyMap<string, string>>(new Map())
const sendingTweakNodeId = computed(() => sendingTweakNodes.value.get(props.id) ?? null)
const previewRevision = ref(0)
/** Room-level cache of local prototype tweak previews, surviving node viewport culling and group folding. */
const prototypeTweaksPreview = new Map<string, PrototypeTweaksPreviewEntry>()
function setArtifactFlag(target: typeof sendingArtifacts, id: string, active: boolean) {
  const next = new Set(target.value)
  if (active) next.add(id)
  else next.delete(id)
  target.value = next
}
function setSendingTweakNode(id: string, nodeId: string) {
  const next = new Map(sendingTweakNodes.value)
  next.set(id, nodeId)
  sendingTweakNodes.value = next
}
function clearSendingTweakNode(id: string, nodeId: string) {
  if (sendingTweakNodes.value.get(id) !== nodeId) return
  const next = new Map(sendingTweakNodes.value)
  next.delete(id)
  sendingTweakNodes.value = next
}
/** Synchronous guard around internal silent Yjs writes (recommended picks / explicit prototype tweaks). */
let silentLocalUpdates = 0
function runSilently<T>(fn: () => T): T {
  silentLocalUpdates++
  try {
    return fn()
  } finally {
    silentLocalUpdates--
  }
}
const sentRound = ref<{
  artifactId: string
  round: number
  hadPending: number
  hadNotes: number
  previousHeard?: unknown
  previousAskBaseline?: DocumentAskAnswerBaseline[] | null
  rollbackTweaks?: PrototypeTweaksSubmitOptions
  rollbackPreview?: PrototypeTweaksPreviewEntry
} | null>(null)
const decisionSummary = ref<DecisionGroupSummary | null>(null)
const documentAsks = ref<AskBlockProps[]>([])
const askBaseline = ref<DocumentAskAnswerBaseline[] | null>(null)
let askBaselineEpoch = 0
const askSummary = computed<DocumentAskSummary>(() =>
  summarizeDocumentAsks(documentAsks.value, askBaseline.value),
)

const hasActiveDecisionGroups = computed(
  () =>
    artifact.value?.kind === 'whiteboard' &&
    !viewingOld.value &&
    !!decisionSummary.value?.hasActiveDecisionGroups,
)

const hasPendingDocumentAsks = computed(
  () => artifact.value?.kind === 'document' && !viewingOld.value && askSummary.value.needsSubmit,
)

const hasStructuredRound = computed(
  () => hasActiveDecisionGroups.value || hasPendingDocumentAsks.value,
)

watch(hasStructuredRound, (active) => {
  if (active) stopQuietTimer()
  else if (waiting.value && !quietPaused.value) scheduleQuietSend()
})

function stopQuietTimer() {
  if (quietTimer) clearInterval(quietTimer)
  quietTimer = null
  quietDeadline = 0
  quietSeconds.value = 0
}

function clearSentRound() {
  if (sentRoundTimer) clearInterval(sentRoundTimer)
  sentRoundTimer = null
  sentRoundDeadline = 0
  withdrawSeconds.value = 0
  sentRound.value = null
}

function startSentRoundCountdown() {
  if (sentRoundTimer) clearInterval(sentRoundTimer)
  sentRoundDeadline = Date.now() + WITHDRAW_WINDOW_MS
  withdrawSeconds.value = Math.ceil(WITHDRAW_WINDOW_MS / 1000)
  sentRoundTimer = setInterval(() => {
    const leftMs = sentRoundDeadline - Date.now()
    if (leftMs <= 0) {
      clearSentRound()
      return
    }
    withdrawSeconds.value = Math.ceil(leftMs / 1000)
  }, 250)
}

watch(
  () => props.id,
  (_id, prevId) => {
    if (prevId !== undefined) leave()
    renaming.value = false
    menu.value = null
    activeComment.value = null
    comments.value = []
    versions.value = []
    latest.value = 0
    artifact.value = null
    decisionSummary.value = null
    documentAsks.value = []
    askBaseline.value = null
    askBaselineEpoch++
    clearSentRound()
    void load()
  },
  { immediate: true },
)
// Keep the live view in sync while the user is editing.
watch(
  () => {
    const item = state.artifacts.find((a) => a.id === props.id)
    return item ? { id: props.id, version: item.version, updatedBy: item.updatedBy } : null
  },
  (next) => {
    if (
      next &&
      artifact.value?.id === next.id &&
      next.version !== latest.value &&
      !viewingOld.value &&
      !switchingVersion.value
    ) {
      if (next.updatedBy !== 'user') clearSentRound()
      void load()
    }
  },
)
// Someone commented (an agent answering): take the comments in, leaving edits open.
watch(
  () => ({
    id: props.id,
    at: state.artifacts.find((a) => a.id === props.id)?.commentedAt,
  }),
  async (next, prev) => {
    if (!next.at || next.id !== prev?.id || next.at === prev?.at) return
    try {
      const detail = await api.getArtifact(next.id, { versions: false })
      if (props.id === next.id) comments.value = detail.comments
    } catch (error) {
      if (props.id === next.id) report(error)
    }
  },
)

// ---- editing together ----

interface ArtifactRoom {
  id: string
  scope: EffectScope
  shared: SharedArtifact | null
  mutations: number
  /** In-flight prototype tweak sends/withdraws that need this room's Y.Doc alive until settled. */
  holds: number
  detached: boolean
  disposed: boolean
  onDocUpdate?: (_update: Uint8Array, origin: unknown, doc: Y.Doc, tr: Y.Transaction) => void
}

/** The room of the artifact open now, joined while its newest version is shown. */
const shared = shallowRef<SharedArtifact | null>(null)
let room: ArtifactRoom | null = null
const roomGeneration = shallowRef(0)

function holdRoom(target: ArtifactRoom | null): () => void {
  if (!target || target.disposed) return () => {}
  target.holds++
  let released = false
  return () => {
    if (released) return
    released = true
    target.holds = Math.max(0, target.holds - 1)
    if (target.detached && target.holds === 0 && !target.disposed) {
      target.disposed = true
      target.scope.stop()
    }
  }
}

function disposeRoomIfIdle(target: ArtifactRoom) {
  target.detached = true
  if (target.onDocUpdate) {
    target.shared?.doc.off('update', target.onDocUpdate)
    target.onDocUpdate = undefined
  }
  if (target.holds === 0 && !target.disposed) {
    target.disposed = true
    target.scope.stop()
  }
}

function clonePreviewEntry(
  entry: PrototypeTweaksPreviewEntry | undefined,
): PrototypeTweaksPreviewEntry | undefined {
  if (!entry) return undefined
  return {
    specsKey: entry.specsKey,
    savedKey: entry.savedKey,
    liveValues: { ...entry.liveValues },
  }
}

function syncPendingStore(id = room?.id) {
  if (!id) return
  if (waiting.value && pendingChanges.value > 0) {
    setPendingArtifactFeedback(id, {
      pendingChanges: pendingChanges.value,
      pendingNotes: pendingNotes.value,
      paused: quietPaused.value,
    })
  } else {
    clearPendingArtifactFeedback(id)
  }
}

function mergePendingStore(id: string, changes: number, notes: number, paused: boolean) {
  if (changes <= 0) return
  const existing = getPendingArtifactFeedback(id)
  setPendingArtifactFeedback(id, {
    pendingChanges: (existing?.pendingChanges ?? 0) + changes,
    pendingNotes: (existing?.pendingNotes ?? 0) + notes,
    paused: paused || (existing?.paused ?? false),
  })
}

function join(id: string) {
  const scope = effectScope()
  const activeRoom: ArtifactRoom = {
    id,
    scope,
    shared: null,
    mutations: 0,
    holds: 0,
    detached: false,
    disposed: false,
  }
  room = activeRoom
  documentAsks.value = []
  const saved = getPendingArtifactFeedback(id)
  if (saved && saved.pendingChanges > 0) {
    waiting.value = true
    pendingChanges.value = saved.pendingChanges
    pendingNotes.value = saved.pendingNotes
    quietPaused.value = saved.paused
  } else {
    waiting.value = false
    pendingChanges.value = 0
    pendingNotes.value = 0
    quietPaused.value = false
  }
  const onLockedEdit =
    artifact.value?.kind === 'document'
      ? () => {
          if (room?.scope !== scope) return
          // Rejoin with a fresh Y.Doc so the rejected update is never replayed.
          leave()
          join(id)
          report(t('artifact.aiEditConflict'))
        }
      : undefined
  const activeShared = scope.run(() => useSharedArtifact(id, onLockedEdit)) ?? null
  activeRoom.shared = activeShared
  shared.value = activeShared
  roomGeneration.value++
  if (activeShared) {
    const onDocUpdate = (_update: Uint8Array, origin: unknown, doc: Y.Doc, tr: Y.Transaction) => {
      if (room !== activeRoom || activeRoom.detached) return
      if (origin === activeShared.provider.value || silentLocalUpdates > 0) return
      if (artifact.value?.kind === 'document' && isAskAnswerOnlyTransaction(doc, tr)) {
        touchAskAnswer()
        return
      }
      touched()
    }
    activeRoom.onDocUpdate = onDocUpdate
    activeShared.doc.on('update', onDocUpdate)

    const reconcilePendingPrototypeRollbacks = () => {
      if (room !== activeRoom || activeRoom.detached || !activeShared.ready.value) return
      const entries = getPendingPrototypeTweakRollbacks(id)
      if (entries.length === 0) return
      let restoredAnyPreview = false
      for (const entry of entries) {
        const current = readDocPrototypeTweakValues(activeShared.doc, entry.nodeId)
        const matchesNext =
          current.exists && areTweakValuesEqual(current.tweakValues, entry.nextDiff)
        const matchesPrev =
          current.exists && areTweakValuesEqual(current.tweakValues, entry.prevDiff)
        if (matchesNext || matchesPrev) {
          if (
            restorePreviewForRollback(
              entry.nodeId,
              entry.prevSavedKey,
              entry.nextSavedKey,
              entry.preview,
            )
          ) {
            restoredAnyPreview = true
          }
          if (matchesNext) {
            runSilently(() => {
              const applied =
                body.value?.setPrototypeTweakValues?.(entry.nodeId, entry.prevDiff) ?? false
              if (!applied) {
                rollbackDocPrototypeTweakValues(
                  activeShared.doc,
                  entry.nodeId,
                  entry.nextDiff,
                  entry.prevDiff,
                )
              }
            })
          }
        }
        clearPendingPrototypeTweakRollback(id, entry.nodeId)
      }
      if (restoredAnyPreview) {
        previewRevision.value++
      }
    }

    scope.run(() => {
      watch(
        [() => activeShared.ready.value, pendingPrototypeRollbackRevision],
        ([isReady]) => {
          if (isReady) reconcilePendingPrototypeRollbacks()
        },
        { immediate: true, flush: 'sync' },
      )
    })
  }
}

watch(
  () => (artifact.value && !viewingOld.value ? artifact.value.id : null),
  (id) => {
    if (room?.id === id) return
    leave()
    if (id) join(id)
  },
)

function scheduleQuietSend() {
  stopQuietTimer()
  if (hasStructuredRound.value || quietPaused.value || !waiting.value) return
  if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return
  quietDeadline = Date.now() + QUIET_MS
  quietSeconds.value = Math.ceil(QUIET_MS / 1000)
  quietTimer = setInterval(() => {
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') {
      stopQuietTimer()
      return
    }
    const leftMs = quietDeadline - Date.now()
    if (leftMs <= 0) {
      stopQuietTimer()
      void sendFeedback()
      return
    }
    quietSeconds.value = Math.ceil(leftMs / 1000)
  }, 250)
}

function toggleQuietPause() {
  if (quietPaused.value) {
    quietPaused.value = false
    syncPendingStore()
    scheduleQuietSend()
  } else {
    quietPaused.value = true
    stopQuietTimer()
    syncPendingStore()
  }
}

function touchAskAnswer() {
  if (room) room.mutations++
  clearSentRound()
}

function touched(kind: 'edit' | 'comment' = 'edit') {
  if (room) room.mutations++
  clearSentRound()
  waiting.value = true
  pendingChanges.value++
  if (kind === 'comment') pendingNotes.value++
  syncPendingStore()
  scheduleQuietSend()
}

function onDecisionSummary(summary: DecisionGroupSummary) {
  decisionSummary.value = summary
  if (summary.hasActiveDecisionGroups) stopQuietTimer()
}

function onDocumentAsks(asks: AskBlockProps[]) {
  if (viewingOld.value || !shared.value?.ready.value) return
  documentAsks.value = asks
}

function restorePreviewForRollback(
  nodeId: string,
  prevSavedKey: string,
  nextSavedKey: string,
  fallbackPreview?: PrototypeTweaksPreviewEntry,
): boolean {
  const currentEntry = prototypeTweaksPreview.get(nodeId)
  if (currentEntry) {
    if (currentEntry.savedKey === nextSavedKey || currentEntry.savedKey === prevSavedKey) {
      prototypeTweaksPreview.set(nodeId, {
        ...currentEntry,
        savedKey: prevSavedKey,
      })
      return true
    }
    return false
  }
  if (fallbackPreview) {
    prototypeTweaksPreview.set(nodeId, {
      specsKey: fallbackPreview.specsKey,
      savedKey: prevSavedKey,
      liveValues: { ...fallbackPreview.liveValues },
    })
    return true
  }
  return false
}

function rollbackPrototypeTweaksForTarget(
  artifactId: string,
  commit: PrototypeTweaksSubmitOptions,
  targetRoom: ArtifactRoom | null,
  fallbackPreview?: PrototypeTweaksPreviewEntry,
) {
  const isLiveCurrent =
    props.id === artifactId &&
    room?.id === artifactId &&
    !viewingOld.value &&
    !!shared.value?.ready.value
  if (isLiveCurrent && shared.value) {
    const current = readDocPrototypeTweakValues(shared.value.doc, commit.nodeId)
    if (!current.exists) return
    const matchesNext = areTweakValuesEqual(current.tweakValues, commit.nextDiff)
    const matchesPrev = areTweakValuesEqual(current.tweakValues, commit.prevDiff)
    if (!matchesNext && !matchesPrev) return
    const restored = restorePreviewForRollback(
      commit.nodeId,
      commit.prevSavedKey,
      commit.nextSavedKey,
      fallbackPreview,
    )
    if (matchesNext) {
      runSilently(() => {
        const applied =
          body.value?.setPrototypeTweakValues?.(commit.nodeId, commit.prevDiff) ?? false
        if (!applied && shared.value) {
          rollbackDocPrototypeTweakValues(
            shared.value.doc,
            commit.nodeId,
            commit.nextDiff,
            commit.prevDiff,
          )
        }
      })
    }
    if (restored) {
      previewRevision.value++
    }
    clearPendingPrototypeTweakRollback(artifactId, commit.nodeId)
    return
  }

  // The user already switched away (or the live room is still syncing). Avoid overwriting
  // concurrent remote/AI edits if we can inspect the retained room's Y.Doc.
  if (targetRoom && !targetRoom.disposed && targetRoom.shared) {
    const current = readDocPrototypeTweakValues(targetRoom.shared.doc, commit.nodeId)
    if (
      !current.exists ||
      (!areTweakValuesEqual(current.tweakValues, commit.nextDiff) &&
        !areTweakValuesEqual(current.tweakValues, commit.prevDiff))
    ) {
      return
    }
  }

  // Record a pending recovery entry first so any other live editor instance reacts immediately
  // and returning to this whiteboard later reconciles offline rollback + restores local preview.
  setPendingPrototypeTweakRollback(artifactId, {
    nodeId: commit.nodeId,
    nextDiff: commit.nextDiff,
    prevDiff: commit.prevDiff,
    nextSavedKey: commit.nextSavedKey,
    prevSavedKey: commit.prevSavedKey,
    preview: fallbackPreview
      ? {
          specsKey: fallbackPreview.specsKey,
          savedKey: commit.prevSavedKey,
          liveValues: { ...fallbackPreview.liveValues },
        }
      : undefined,
  })

  // Roll back on the retained source room immediately so the server room does not keep dirty
  // tweakValues even if the user never switches back to this whiteboard.
  if (targetRoom && !targetRoom.disposed && targetRoom.shared) {
    runSilently(() => {
      rollbackDocPrototypeTweakValues(
        targetRoom.shared!.doc,
        commit.nodeId,
        commit.nextDiff,
        commit.prevDiff,
      )
    })
  }
}

interface SendFeedbackOptions {
  explicit?: boolean
  prototypeTweaks?: PrototypeTweaksSubmitOptions
}

async function sendFeedback(options: SendFeedbackOptions = {}): Promise<boolean> {
  const prototypeTweaks = options.prototypeTweaks
  const explicit = Boolean(options.explicit || prototypeTweaks)
  stopQuietTimer()
  if (!room) return false
  const sendRoom = room
  const id = sendRoom.id
  if (sendingArtifacts.value.has(id)) return false
  if (!explicit && (!waiting.value || hasStructuredRound.value)) return false

  const isDocument = artifact.value?.kind === 'document'
  const projectId = artifact.value?.projectId ?? ''
  const initialPending = pendingChanges.value
  const hadNotes = pendingNotes.value
  const hadUnsentAsks = isDocument && askSummary.value.hasUnsentChanges
  const previousAskBaseline = isDocument
    ? askBaseline.value
      ? askBaseline.value.map((entry) => ({ ...entry, selected: [...entry.selected] }))
      : null
    : undefined
  const isSameRoom = () => props.id === id && room === sendRoom && !viewingOld.value
  const isCurrentLiveArtifact = () => props.id === id && room?.id === id && !viewingOld.value
  const submittedPreview = prototypeTweaks
    ? clonePreviewEntry(prototypeTweaksPreview.get(prototypeTweaks.nodeId))
    : undefined
  const releaseSendRoom = prototypeTweaks ? holdRoom(sendRoom) : () => {}
  let applied = 0
  let tweaksApplied = false
  let clearedPending = false
  let effectiveInitialPending = initialPending
  let effectiveHadNotes = hadNotes
  const rollbackAppliedTweaks = () => {
    if (!tweaksApplied || !prototypeTweaks) return
    tweaksApplied = false
    rollbackPrototypeTweaksForTarget(id, prototypeTweaks, sendRoom, submittedPreview)
  }
  setArtifactFlag(sendingArtifacts, id, true)
  if (prototypeTweaks) {
    clearPendingPrototypeTweakRollback(id, prototypeTweaks.nodeId)
    setSendingTweakNode(id, prototypeTweaks.nodeId)
  }
  try {
    if (prototypeTweaks) {
      const updated = runSilently(() => {
        const prevEntry = prototypeTweaksPreview.get(prototypeTweaks.nodeId)
        if (prevEntry) {
          prototypeTweaksPreview.set(prototypeTweaks.nodeId, {
            ...prevEntry,
            savedKey: prototypeTweaks.nextSavedKey,
          })
        }
        const appliedWrite =
          body.value?.setPrototypeTweakValues?.(prototypeTweaks.nodeId, prototypeTweaks.nextDiff) ??
          false
        if (!appliedWrite && prevEntry) {
          prototypeTweaksPreview.set(prototypeTweaks.nodeId, prevEntry)
        }
        return appliedWrite
      })
      if (!updated) return false
      tweaksApplied = true
      await nextTick()
      await new Promise((resolve) => setTimeout(resolve, 120))
    } else if (explicit && hasStructuredRound.value) {
      applied = runSilently(() => body.value?.applyRecommendedPicks?.() ?? 0)
      if (applied > 0) {
        await nextTick()
        await new Promise((resolve) => setTimeout(resolve, 120))
      }
    }
    const activeBeforeSend = isSameRoom()
    const previousHeard = activeBeforeSend ? body.value?.getHeard?.() : undefined
    const boardAtSend = activeBeforeSend ? body.value?.currentSnapshot?.() : undefined
    const asksAtSend =
      isDocument && activeBeforeSend
        ? (body.value?.currentAskSnapshot?.() ?? snapshotDocumentAsks(documentAsks.value))
        : undefined
    const sentMutationSeq = sendRoom.mutations
    if (activeBeforeSend) {
      effectiveInitialPending = pendingChanges.value
      effectiveHadNotes = pendingNotes.value
      waiting.value = false
      quietPaused.value = false
      pendingChanges.value = 0
      pendingNotes.value = 0
    } else {
      const savedBeforeClear = getPendingArtifactFeedback(id)
      if (savedBeforeClear) {
        effectiveInitialPending = Math.max(effectiveInitialPending, savedBeforeClear.pendingChanges)
        effectiveHadNotes = Math.max(effectiveHadNotes, savedBeforeClear.pendingNotes)
      }
    }
    clearPendingArtifactFeedback(id)
    clearedPending = true
    const pictures = activeBeforeSend ? ((await body.value?.markedPictures?.()) ?? []) : []
    const images = await Promise.all(
      pictures.map(async (picture) => (await api.uploadFile(projectId, picture)).file.id),
    )
    let result = await api.sendFeedback(id, images)
    // If ask answers, recommended picks, or prototype tweaks were just written to Yjs and the
    // first flush raced WebSocket delivery, wait briefly and try once more before restoring state.
    if (explicit && (applied > 0 || hadUnsentAsks || tweaksApplied) && !result?.feedback) {
      await new Promise((resolve) => setTimeout(resolve, 250))
      result = await api.sendFeedback(id, images)
    }
    const editedDuringSend = sendRoom.mutations !== sentMutationSeq
    if (result?.feedback && result.feedback.round > 0) {
      if (prototypeTweaks) {
        clearPendingPrototypeTweakRollback(id, prototypeTweaks.nodeId)
      }
      if (isSameRoom()) {
        if (boardAtSend !== undefined) body.value?.setHeard?.(boardAtSend)
        else body.value?.markHeard?.()
        if (asksAtSend !== undefined) {
          askBaselineEpoch++
          askBaseline.value = asksAtSend
          if (editedDuringSend) void refreshAskBaseline(id)
        }
        if (!editedDuringSend) {
          clearSentRound()
          sentRound.value = {
            artifactId: id,
            round: result.feedback.round,
            hadPending:
              isDocument || prototypeTweaks
                ? effectiveInitialPending
                : Math.max(1, effectiveInitialPending),
            hadNotes: effectiveHadNotes,
            previousHeard,
            previousAskBaseline,
            rollbackTweaks: prototypeTweaks,
            rollbackPreview: submittedPreview,
          }
          startSentRoundCountdown()
        }
      } else if (isDocument && props.id === id && !viewingOld.value) {
        askBaselineEpoch++
        void refreshAskBaseline(id)
      }
      return true
    }
    rollbackAppliedTweaks()
    if (explicit && !editedDuringSend) {
      const restorePending =
        isDocument || prototypeTweaks
          ? effectiveInitialPending
          : Math.max(effectiveInitialPending, applied)
      if (restorePending > 0) {
        if (isCurrentLiveArtifact()) {
          waiting.value = true
          quietPaused.value = true
          pendingChanges.value += restorePending
          pendingNotes.value += effectiveHadNotes
          syncPendingStore(id)
        } else {
          mergePendingStore(id, restorePending, effectiveHadNotes, true)
        }
      }
      if (isDocument && props.id === id && !viewingOld.value) {
        void refreshAskBaseline(id)
      }
    }
    return false
  } catch (error) {
    rollbackAppliedTweaks()
    if (clearedPending) {
      const restorePending =
        isDocument || prototypeTweaks
          ? effectiveInitialPending
          : Math.max(effectiveInitialPending, applied)
      if (restorePending > 0) {
        if (isCurrentLiveArtifact()) {
          waiting.value = true
          quietPaused.value = true
          pendingChanges.value += restorePending
          pendingNotes.value += effectiveHadNotes
          syncPendingStore(id)
        } else {
          mergePendingStore(id, restorePending, effectiveHadNotes, true)
        }
      }
    }
    report(error)
    return false
  } finally {
    if (prototypeTweaks) {
      clearSendingTweakNode(id, prototypeTweaks.nodeId)
    }
    setArtifactFlag(sendingArtifacts, id, false)
    releaseSendRoom()
  }
}

provide(PROTOTYPE_TWEAKS_BRIDGE_KEY, {
  submitTweaks: (options) => sendFeedback({ explicit: true, prototypeTweaks: options }),
  sending,
  sendingNodeId: sendingTweakNodeId,
  previewRevision,
  getPreview: (nodeId) => prototypeTweaksPreview.get(nodeId),
  setPreview: (nodeId, entry) => {
    prototypeTweaksPreview.set(nodeId, {
      specsKey: entry.specsKey,
      savedKey: entry.savedKey,
      liveValues: { ...entry.liveValues },
    })
  },
  clearPreview: (nodeId) => {
    prototypeTweaksPreview.delete(nodeId)
  },
})

async function withdrawSentFeedback() {
  const target = sentRound.value
  if (!target || withdrawingArtifacts.value.has(target.artifactId)) return
  const withdrawRoom = room
  const currentWithdrawPreview = target.rollbackTweaks
    ? prototypeTweaksPreview.get(target.rollbackTweaks.nodeId)
    : undefined
  const withdrawPreview =
    target.rollbackTweaks &&
    currentWithdrawPreview &&
    (currentWithdrawPreview.savedKey === target.rollbackTweaks.nextSavedKey ||
      currentWithdrawPreview.savedKey === target.rollbackTweaks.prevSavedKey)
      ? clonePreviewEntry(currentWithdrawPreview)
      : target.rollbackPreview
  const releaseWithdrawRoom = target.rollbackTweaks ? holdRoom(withdrawRoom) : () => {}
  const isSameRoom = () =>
    props.id === target.artifactId && room === withdrawRoom && !viewingOld.value
  const isCurrentLiveArtifact = () =>
    props.id === target.artifactId && room?.id === target.artifactId && !viewingOld.value

  setArtifactFlag(withdrawingArtifacts, target.artifactId, true)
  try {
    const { withdrawn } = await api.withdrawFeedback(target.artifactId, target.round)
    if (sentRound.value === target) clearSentRound()
    if (withdrawn) {
      if (target.rollbackTweaks) {
        rollbackPrototypeTweaksForTarget(
          target.artifactId,
          target.rollbackTweaks,
          withdrawRoom,
          withdrawPreview,
        )
      }
      if (isSameRoom()) {
        if (target.previousHeard !== undefined) body.value?.setHeard?.(target.previousHeard)
        if (target.previousAskBaseline !== undefined) {
          askBaselineEpoch++
          askBaseline.value = target.previousAskBaseline
        }
        if (target.hadPending > 0) {
          waiting.value = true
          pendingChanges.value += target.hadPending
          pendingNotes.value += target.hadNotes
          syncPendingStore(target.artifactId)
          scheduleQuietSend()
        }
      } else {
        if (
          target.previousAskBaseline !== undefined &&
          props.id === target.artifactId &&
          !viewingOld.value
        ) {
          askBaselineEpoch++
          void refreshAskBaseline(target.artifactId)
        }
        if (target.hadPending > 0) {
          if (isCurrentLiveArtifact()) {
            waiting.value = true
            pendingChanges.value += target.hadPending
            pendingNotes.value += target.hadNotes
            syncPendingStore(target.artifactId)
            scheduleQuietSend()
          } else {
            mergePendingStore(target.artifactId, target.hadPending, target.hadNotes, true)
          }
        }
      }
    } else {
      report(t('artifact.feedback.withdrawTooLate'))
    }
  } catch (error) {
    report(error)
  } finally {
    setArtifactFlag(withdrawingArtifacts, target.artifactId, false)
    releaseWithdrawRoom()
  }
}

const feedbackBarState = computed<'sent' | 'decision' | 'ask' | 'pending' | null>(() => {
  if (!artifact.value || viewingOld.value || artifact.value.kind === 'dashboard') return null
  if (sentRound.value && sentRound.value.artifactId === artifact.value.id) return 'sent'
  if (hasActiveDecisionGroups.value) return 'decision'
  if (hasPendingDocumentAsks.value) return 'ask'
  if (waiting.value && pendingChanges.value > 0) return 'pending'
  return null
})

const decisionBarText = computed(() => {
  const s = decisionSummary.value
  if (!s) return ''
  let base: string
  if (s.remainingGroups === 0 && s.pickedGroups === s.totalGroups) {
    base = t('artifact.feedback.decisionAllPicked', {
      picked: s.pickedGroups,
      total: s.totalGroups,
    })
  } else if (s.remainingGroups === 0 && s.pickedGroups > 0) {
    base = t('artifact.feedback.decisionWithRecommended', {
      picked: s.pickedGroups,
      total: s.totalGroups,
    })
  } else if (s.remainingGroups === 0) {
    base = t('artifact.feedback.decisionAllRecommended', { total: s.totalGroups })
  } else {
    base = t('artifact.feedback.decisionRemaining', {
      picked: s.pickedGroups,
      total: s.totalGroups,
      remaining: s.remainingGroups,
    })
  }
  if (pendingNotes.value > 0) {
    base += t('artifact.feedback.withNotes', { count: pendingNotes.value })
  }
  return base
})

const canSubmitDecision = computed(() => {
  const s = decisionSummary.value
  if (!s || sending.value) return false
  return s.pickedGroups > 0 || s.recommendedFallbackGroups > 0 || waiting.value
})

const askBarText = computed(() => {
  const s = askSummary.value
  if (!s) return ''
  let base: string
  if (s.remainingAsks === 0 && s.answeredAsks === s.totalAsks) {
    base = t('artifact.feedback.askProgress', {
      answered: s.answeredAsks,
      total: s.totalAsks,
    })
  } else if (s.remainingAsks === 0 && s.answeredAsks > 0) {
    base = t('artifact.feedback.askWithRecommended', {
      answered: s.answeredAsks,
      total: s.totalAsks,
    })
  } else if (s.remainingAsks === 0 && s.recommendedFallbackAsks > 0) {
    base = t('artifact.feedback.askAllRecommended', { total: s.totalAsks })
  } else {
    base = t('artifact.feedback.askProgress', {
      answered: s.answeredAsks,
      total: s.totalAsks,
    })
  }
  if (pendingNotes.value > 0) {
    base += t('artifact.feedback.withNotes', { count: pendingNotes.value })
  }
  return base
})

const canSubmitAsk = computed(() => {
  const s = askSummary.value
  if (!s.needsSubmit || sending.value) return false
  return s.hasUnsentChanges || s.recommendedFallbackAsks > 0 || waiting.value
})

function leave() {
  stopQuietTimer()
  clearSentRound()
  documentAsks.value = []
  prototypeTweaksPreview.clear()
  if (!room) return
  const leavingRoom = room
  if (waiting.value && pendingChanges.value > 0) {
    setPendingArtifactFeedback(leavingRoom.id, {
      pendingChanges: pendingChanges.value,
      pendingNotes: pendingNotes.value,
      paused: true,
    })
  } else {
    clearPendingArtifactFeedback(leavingRoom.id)
  }
  waiting.value = false
  quietPaused.value = false
  pendingChanges.value = 0
  pendingNotes.value = 0
  room = null
  shared.value = null
  disposeRoomIfIdle(leavingRoom)
}
onBeforeUnmount(leave)

// Switching away pauses the countdown instead of silently submitting unfinished feedback.
function onVisibilityChange() {
  if (document.visibilityState === 'hidden') {
    stopQuietTimer()
  } else if (waiting.value && !quietPaused.value && !hasStructuredRound.value) {
    scheduleQuietSend()
  }
}
document.addEventListener('visibilitychange', onVisibilityChange)
onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', onVisibilityChange)
})

/** Comments go to the newest version of a document or whiteboard; a dashboard has none. */
const commentProps = computed(() =>
  artifact.value && artifact.value.kind !== 'dashboard' && !viewingOld.value
    ? {
        comments: comments.value,
        activeComment: activeComment.value,
        'onUpdate:activeComment': (id: string | null) => (activeComment.value = id),
        onComment: comment,
      }
    : {},
)

function openComment(id: string) {
  body.value?.revealComment?.(id)
  activeComment.value = id
}

async function refreshComments(id: string) {
  try {
    const detail = await api.getArtifact(id, { versions: false })
    if (props.id === id) comments.value = detail.comments
  } catch {
    // Keep existing local comment list if sync fails.
  }
}

/** The longest quote the server keeps (MAX_COMMENT_QUOTE). */
const MAX_QUOTE = 1000

/** The editor's comment change, sent as the server takes it. */
function toAction(
  action: CommentAction,
  quote: string | ArtifactCommentAnchor | null,
  label = '',
): ArtifactCommentAction | null {
  switch (action.type) {
    case 'create': {
      const first = action.thread.comments[0]
      if (!first || !quote) return null
      // On a whiteboard, the item or connector the thread is about.
      if (typeof quote !== 'string') {
        return {
          type: 'create',
          threadId: action.thread.id,
          commentId: first.id,
          quote: label.slice(0, MAX_QUOTE),
          body: first.body,
          anchor: quote,
        }
      }
      // A long passage is kept by its opening, which still finds it.
      return {
        type: 'create',
        threadId: action.thread.id,
        commentId: first.id,
        quote: quote.slice(0, MAX_QUOTE),
        body: first.body,
      }
    }
    case 'reply':
      return {
        type: 'reply',
        threadId: action.threadId,
        commentId: action.comment.id,
        body: action.comment.body,
      }
    case 'edit':
      return {
        type: 'edit',
        threadId: action.threadId,
        commentId: action.commentId,
        body: action.body,
      }
    case 'delete':
      return { type: 'delete', threadId: action.threadId, commentId: action.commentId }
    default:
      return { type: action.type, threadId: action.threadId }
  }
}

async function comment(
  action: CommentAction,
  quote: string | ArtifactCommentAnchor | null = '',
  label = '',
) {
  const sent = toAction(action, quote, label)
  if (!sent) return
  if (
    sent.type === 'create' ||
    sent.type === 'reply' ||
    sent.type === 'edit' ||
    sent.type === 'reopen'
  ) {
    touched('comment')
  }
  try {
    let result: { comments: ArtifactCommentThreadView[] }
    switch (sent.type) {
      case 'create':
        result = await api.createArtifactComment(props.id, {
          threadId: sent.threadId,
          commentId: sent.commentId,
          quote: sent.quote,
          body: sent.body,
          ...(sent.anchor ? { anchor: sent.anchor } : {}),
        })
        break
      case 'reply':
        result = await api.replyArtifactComment(props.id, {
          threadId: sent.threadId,
          commentId: sent.commentId,
          body: sent.body,
        })
        break
      case 'edit':
        result = await api.editArtifactComment(props.id, {
          threadId: sent.threadId,
          commentId: sent.commentId,
          body: sent.body,
        })
        break
      case 'delete':
        result = await api.deleteArtifactComment(props.id, {
          threadId: sent.threadId,
          commentId: sent.commentId,
        })
        break
      case 'resolve':
        result = await api.resolveArtifactCommentThread(props.id, sent.threadId)
        break
      case 'reopen':
        result = await api.reopenArtifactCommentThread(props.id, sent.threadId)
        break
      case 'remove':
        result = await api.removeArtifactCommentThread(props.id, sent.threadId)
        break
    }
    comments.value = result.comments
  } catch (error) {
    report(error)
    void refreshComments(props.id)
  }
}

async function save(
  input: { content?: string; title?: string; reason?: string } = {},
): Promise<boolean> {
  const current = artifact.value
  if (!current) return false
  saving.value = true
  try {
    const detail = await api.saveArtifact(props.id, {
      // The room holds the content: a rename leaves it as it is there.
      content: input.content ?? null,
      baseVersion: latest.value,
      reason: input.reason ?? t('artifact.saveReason'),
      ...(input.title ? { title: input.title } : {}),
      ...(body.value?.quotes ? { quotes: body.value.quotes() } : {}),
      includeVersions: false,
    })
    if (props.id !== current.id) return false
    viewRequest++
    comments.value = detail.comments
    latest.value = detail.artifact.version
    show(detail.artifact)
    return true
  } catch (error) {
    // The reason is written for the agent; the user gets a plain line.
    if (error instanceof ApiError && error.localized.key === 'error.artifactInvalid') {
      report(t('artifact.invalidSave'))
    } else report(error)
    return false
  } finally {
    saving.value = false
  }
}

function restore() {
  if (!artifact.value) return
  void save({
    content: artifact.value.content,
    reason: t('artifact.restoreReason', { version: artifact.value.version }),
  })
}

async function startRename() {
  title.value = artifact.value?.title ?? ''
  renaming.value = true
  await nextTick()
  titleInput.value?.focus()
}

async function rename() {
  const next = title.value.trim()
  if (!next || next === artifact.value?.title) {
    renaming.value = false
    return
  }
  if (await save({ title: next, reason: t('artifact.renameReason') })) renaming.value = false
}
</script>

<template>
  <div class="artifact" :class="`is-${place}`">
    <div class="artifact-header" :class="{ 'has-nav': showNav }">
      <div v-if="showNav" class="artifact-nav" role="group" :aria-label="t('artifact.nav.group')">
        <DouTooltip :content="backLabel" placement="bottom" :disabled="!canBack">
          <DouButton
            size="sm"
            variant="ghost"
            :aria-label="backLabel"
            :disabled="!canBack"
            data-artifact-nav="back"
            @click="goBack"
          >
            <ArrowLeftIcon />
          </DouButton>
        </DouTooltip>
        <DouTooltip :content="forwardLabel" placement="bottom" :disabled="!canForward">
          <DouButton
            size="sm"
            variant="ghost"
            :aria-label="forwardLabel"
            :disabled="!canForward"
            data-artifact-nav="forward"
            @click="goForward"
          >
            <ArrowRightIcon />
          </DouButton>
        </DouTooltip>
      </div>
      <component :is="kindIcon[displayKind]" v-if="displayKind" class="artifact-kind" />
      <form v-if="renaming" class="artifact-title" @submit.prevent="rename">
        <DouInput
          ref="titleInput"
          v-model="title"
          size="sm"
          :aria-label="t('artifact.rename')"
          @keydown.esc="renaming = false"
        />
        <DouButton
          size="sm"
          variant="ghost"
          native-type="submit"
          :aria-label="t('artifact.rename')"
          :loading="saving"
        >
          <CheckIcon />
        </DouButton>
      </form>
      <div v-else class="artifact-title">
        <DouText weight="medium" truncate>{{ displayTitle }}</DouText>
        <DouButton
          v-if="artifact && !viewingOld"
          size="sm"
          variant="ghost"
          :aria-label="t('artifact.rename')"
          :title="t('artifact.rename')"
          @click="startRename"
        >
          <PencilIcon />
        </DouButton>
      </div>
      <CommentList
        v-if="artifact?.kind === 'document' && !viewingOld"
        :comments="comments"
        :kind="artifact.kind"
        :anchored="() => body?.anchored?.() ?? new Set()"
        @open="openComment"
        @action="comment"
      />
      <DouPopover
        v-if="artifact"
        v-model:open="menuOpen"
        :title="historyOpen ? t('artifact.versions') : undefined"
        :aria-label="historyOpen ? undefined : t('artifact.menu')"
        :width="historyOpen ? 'min(340px, calc(100vw - 24px))' : 144"
        :style="historyOpen ? undefined : { '--dou-popover-padding': '4px' }"
        :disabled="saving || switchingVersion"
        placement="bottom-end"
      >
        <DouButton
          variant="ghost"
          size="sm"
          :aria-label="t('artifact.menu')"
          :title="t('artifact.menu')"
          :disabled="saving || switchingVersion"
        >
          <MoreHorizontalIcon />
        </DouButton>
        <template #content="{ close }">
          <ArtifactHistory
            v-if="historyOpen"
            :versions="versions"
            :selected="artifact.version"
            :status="historyStatus"
            @retry="retryHistory"
            @select="
              (version) => {
                close()
                void switchVersion(version === versions[0]?.version ? undefined : version)
              }
            "
          />
          <DouButton
            v-else
            block
            variant="ghost"
            size="sm"
            class="artifact-menu-item"
            @click="menu = 'history'"
          >
            <template #leading><HistoryIcon /></template>
            {{ t('artifact.versions') }}
          </DouButton>
        </template>
      </DouPopover>
      <ViewActions :view="{ kind: 'artifact', id }" :place="place" />
    </div>
    <div v-if="viewingOld" class="history-context">
      <div class="history-navigation" role="group" :aria-label="t('artifact.versions')">
        <DouButton
          size="sm"
          variant="ghost"
          :aria-label="t('artifact.newerVersion')"
          :title="t('artifact.newerVersion')"
          :disabled="saving || switchingVersion || historyNeighbors.newer === undefined"
          @click="historyNeighbors.newer !== undefined && switchVersion(historyNeighbors.newer)"
        >
          <ChevronUpIcon />
        </DouButton>
        <DouButton
          size="sm"
          variant="ghost"
          :aria-label="t('artifact.olderVersion')"
          :title="t('artifact.olderVersion')"
          :disabled="saving || switchingVersion || historyNeighbors.older === undefined"
          @click="historyNeighbors.older !== undefined && switchVersion(historyNeighbors.older)"
        >
          <ChevronDownIcon />
        </DouButton>
      </div>
      <DouText size="sm" variant="secondary" class="history-caption">
        {{ t('artifact.viewing', { version: artifact?.version ?? 0 }) }}
      </DouText>
      <div class="history-actions">
        <DouButton
          size="sm"
          variant="ghost"
          :disabled="saving || switchingVersion"
          @click="switchVersion()"
        >
          {{ t('artifact.returnToLatest') }}
        </DouButton>
        <DouButton
          size="sm"
          variant="outline"
          :disabled="switchingVersion"
          :loading="saving"
          @click="restore"
        >
          <template #leading><RotateCcwIcon /></template>
          {{ t('artifact.restore') }}
        </DouButton>
      </div>
    </div>
    <div class="artifact-body">
      <component
        :is="bodies[artifact.kind]"
        v-if="artifact && (viewingOld || shared)"
        :key="`${artifact.id}:${viewingOld ? artifact.version : `live-${roomGeneration}`}`"
        ref="body"
        v-bind="commentProps"
        :shared="viewingOld ? undefined : (shared ?? undefined)"
        :content="bodyContent"
        :project-id="artifact.projectId"
        :artifact-id="artifact.id"
        :readonly="viewingOld"
        :label="artifact.title"
        @decision-summary="onDecisionSummary"
        @asks="onDocumentAsks"
        @navigate="onNavigateArtifact"
      />
    </div>
    <div v-if="feedbackBarState" class="artifact-feedback-bar" role="status" aria-live="polite">
      <template v-if="feedbackBarState === 'sent' && sentRound">
        <DouText size="sm" variant="secondary" truncate class="artifact-feedback-text">
          {{ t('artifact.feedback.sentRound', { round: sentRound.round }) }}
        </DouText>
        <DouButton size="sm" variant="ghost" :loading="withdrawing" @click="withdrawSentFeedback">
          <template #leading><UndoIcon /></template>
          {{
            withdrawSeconds > 0
              ? t('artifact.feedback.withdrawCountdown', { seconds: withdrawSeconds })
              : t('artifact.feedback.withdraw')
          }}
        </DouButton>
      </template>
      <template v-else-if="feedbackBarState === 'decision'">
        <DouText size="sm" variant="secondary" truncate class="artifact-feedback-text">
          {{ decisionBarText }}
        </DouText>
        <DouButton
          size="sm"
          variant="soft"
          tone="primary"
          :loading="sending"
          :disabled="!canSubmitDecision"
          @click="sendFeedback({ explicit: true })"
        >
          <template #leading><SendIcon /></template>
          {{ t('artifact.feedback.submitRound') }}
        </DouButton>
      </template>
      <template v-else-if="feedbackBarState === 'ask'">
        <DouText size="sm" variant="secondary" truncate class="artifact-feedback-text">
          {{ askBarText }}
        </DouText>
        <DouButton
          size="sm"
          variant="soft"
          tone="primary"
          :loading="sending"
          :disabled="!canSubmitAsk"
          @click="sendFeedback({ explicit: true })"
        >
          <template #leading><SendIcon /></template>
          {{ t('artifact.feedback.submitAnswers') }}
        </DouButton>
      </template>
      <template v-else-if="feedbackBarState === 'pending'">
        <DouText size="sm" variant="secondary" truncate class="artifact-feedback-text">
          {{
            quietPaused
              ? t('artifact.feedback.pendingPaused', { count: pendingChanges })
              : quietSeconds > 0
                ? t('artifact.feedback.pendingCountdown', {
                    count: pendingChanges,
                    seconds: quietSeconds,
                  })
                : pendingChanges === 1
                  ? t('artifact.feedback.pendingChanges')
                  : t('artifact.feedback.pendingCount', { count: pendingChanges })
          }}
        </DouText>
        <div class="artifact-feedback-actions">
          <DouButton size="sm" variant="ghost" :disabled="sending" @click="toggleQuietPause">
            <template #leading>
              <PlayIcon v-if="quietPaused" />
              <PauseIcon v-else />
            </template>
            {{ t(quietPaused ? 'artifact.feedback.resume' : 'artifact.feedback.pause') }}
          </DouButton>
          <DouButton
            size="sm"
            variant="soft"
            tone="primary"
            :loading="sending"
            @click="sendFeedback({ explicit: true })"
          >
            <template #leading><SendIcon /></template>
            {{ t('artifact.feedback.sendNow') }}
          </DouButton>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.artifact {
  position: relative;
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.artifact-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px 4px 16px;
  border-bottom: 1px solid var(--dou-color-border);
}

.artifact-header.has-nav {
  padding-left: 8px;
}

.artifact-nav {
  display: flex;
  flex: none;
  align-items: center;
  gap: 2px;
}

.artifact-kind {
  flex: none;
  color: var(--dou-color-text-tertiary);
}

.artifact-title {
  display: flex;
  flex: 1;
  align-items: center;
  gap: 4px;
  min-width: 0;
}

.artifact-menu-item {
  justify-content: flex-start;
}

.history-context {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 8px;
  padding: 4px 8px 4px 16px;
  border-bottom: 1px solid var(--dou-color-border);
}

.history-caption {
  font-variant-numeric: tabular-nums;
}

.history-navigation {
  display: flex;
  flex: none;
  gap: 2px;
}

.history-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 4px;
  margin-inline-start: auto;
}

.artifact-body {
  flex: 1;
  min-height: 0;
}

.artifact-feedback-bar {
  position: absolute;
  left: 50%;
  bottom: max(12px, var(--ariadne-safe-bottom));
  transform: translateX(-50%);
  z-index: 15;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  max-width: calc(100% - 24px);
  padding: 6px 8px 6px 12px;
  border: 1px solid var(--dou-color-border);
  border-radius: var(--dou-radius-container);
  background: var(--dou-color-bg-surface);
  box-shadow: var(--dou-shadow-md, 0 4px 12px rgb(0 0 0 / 0.08));
}

.artifact-feedback-actions {
  display: flex;
  flex: none;
  align-items: center;
  gap: 4px;
}

.artifact-feedback-text {
  flex: 1;
  min-width: 0;
  font-variant-numeric: tabular-nums;
}
</style>
