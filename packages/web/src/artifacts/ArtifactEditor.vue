<script setup lang="ts">
import type {
  ArtifactCommentAction,
  ArtifactCommentAnchor,
  ArtifactCommentThreadView,
  ArtifactVersionView,
  ArtifactView,
} from '@ariadne/core'
import { DouButton, DouInput, DouPopover, DouText, type CommentAction } from 'dou-ui'
import {
  CheckIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  HistoryIcon,
  MoreHorizontalIcon,
  PencilIcon,
  RotateCcwIcon,
  SendIcon,
  UndoIcon,
} from '@dou-ui/icons'
import {
  computed,
  defineAsyncComponent,
  effectScope,
  nextTick,
  onBeforeUnmount,
  ref,
  shallowRef,
  watch,
  type EffectScope,
} from 'vue'
import { api, ApiError } from '../api'
import { t } from '../i18n'
import { report, state } from '../store'
import ViewActions from '../full/ViewActions.vue'
import ArtifactHistory from './ArtifactHistory.vue'
import CommentList from './CommentList.vue'
import type { DecisionGroupSummary } from './decision-groups'
import { kindIcon } from './kinds'
import { useSharedArtifact, type SharedArtifact } from './shared'

/** `place` is where it is shown: in a tab on the right side, or full screen. */
const props = defineProps<{ id: string; place: 'side' | 'full' }>()

// Each kind has its own dou-ui package; load only the one in use.
const bodies = {
  document: defineAsyncComponent(() => import('./DocumentBody.vue')),
  whiteboard: defineAsyncComponent(() => import('./WhiteboardBody.vue')),
  dashboard: defineAsyncComponent(() => import('./DashboardBody.vue')),
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
  applyRecommendedPicks?: () => number
} | null>(null)
/** The newest version; an older one is shown read-only. */
const latest = ref(0)
const saving = ref(false)
const switchingVersion = ref(false)
const renaming = ref(false)
const title = ref('')
const titleInput = ref<InstanceType<typeof DouInput> | null>(null)

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
  const id = props.id
  try {
    const detail = await api.getArtifact(id, { version, versions: version !== undefined })
    if (request !== viewRequest || id !== props.id) return
    if (version !== undefined) versions.value = detail.versions
    comments.value = detail.comments
    latest.value = detail.versions[0]?.version ?? detail.artifact.version
    show(detail.artifact)
  } catch (error) {
    if (request === viewRequest && id === props.id) report(error)
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

watch(historyOpen, async (open, _, onCleanup) => {
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
 * 15-second idle pause (paused while active decision groups await explicit submission).
 */
const QUIET_MS = 15_000
const WITHDRAW_WINDOW_MS = 60_000
let quiet: ReturnType<typeof setTimeout> | null = null
let sentRoundTimer: ReturnType<typeof setTimeout> | null = null
const waiting = ref(false)
const pendingChanges = ref(0)
const pendingNotes = ref(0)
/** A round is being sent with pictures, which takes a moment. */
const sending = ref(false)
const withdrawing = ref(false)
const sentRound = ref<{ artifactId: string; round: number; hadPending: number } | null>(null)
const decisionSummary = ref<DecisionGroupSummary | null>(null)

const hasActiveDecisionGroups = computed(
  () =>
    artifact.value?.kind === 'whiteboard' &&
    !viewingOld.value &&
    !!decisionSummary.value?.hasActiveDecisionGroups,
)

function clearSentRound() {
  if (sentRoundTimer) clearTimeout(sentRoundTimer)
  sentRoundTimer = null
  sentRound.value = null
}

watch(
  () => props.id,
  () => {
    renaming.value = false
    menu.value = null
    artifact.value = null
    decisionSummary.value = null
    clearSentRound()
    void load()
  },
  { immediate: true },
)
// Keep the live view in sync while the user is editing.
watch(
  () => {
    const summary = state.artifacts.find((a) => a.id === props.id)
    return summary ? { version: summary.version, updatedBy: summary.updatedBy } : null
  },
  (next) => {
    if (next && next.version !== latest.value && !viewingOld.value && !switchingVersion.value) {
      if (next.updatedBy !== 'user') clearSentRound()
      void load()
    }
  },
)
// Someone commented (an agent answering): take the comments in, leaving edits open.
watch(
  () => state.artifacts.find((a) => a.id === props.id)?.commentedAt,
  async (at, before) => {
    if (!at || at === before) return
    try {
      comments.value = (await api.getArtifact(props.id, { versions: false })).comments
    } catch (error) {
      report(error)
    }
  },
)

// ---- editing together ----

/** The room of the artifact open now, joined while its newest version is shown. */
const shared = shallowRef<SharedArtifact | null>(null)
let room: { id: string; scope: EffectScope } | null = null
const roomGeneration = shallowRef(0)

function join(id: string) {
  const scope = effectScope()
  room = { id, scope }
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
  shared.value = scope.run(() => useSharedArtifact(id, onLockedEdit)) ?? null
  roomGeneration.value++
  shared.value?.doc.on('update', (_update: Uint8Array, origin: unknown) => {
    if (origin !== shared.value?.provider.value && !sending.value) touched()
  })
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
  if (quiet) clearTimeout(quiet)
  quiet = null
  if (hasActiveDecisionGroups.value) return
  quiet = setTimeout(() => void sendFeedback(), QUIET_MS)
}

function touched(kind: 'edit' | 'comment' = 'edit') {
  clearSentRound()
  waiting.value = true
  pendingChanges.value++
  if (kind === 'comment') pendingNotes.value++
  scheduleQuietSend()
}

function onDecisionSummary(summary: DecisionGroupSummary) {
  decisionSummary.value = summary
  if (summary.hasActiveDecisionGroups && quiet) {
    clearTimeout(quiet)
    quiet = null
  }
}

async function sendFeedback(leaving = false, explicit = false) {
  if (quiet) clearTimeout(quiet)
  quiet = null
  if (!room) return
  if (leaving) {
    if (!waiting.value || hasActiveDecisionGroups.value) return
    waiting.value = false
    pendingChanges.value = 0
    pendingNotes.value = 0
    return api.sendFeedback(room.id, [], true)
  }
  if (sending.value) return
  if (!explicit && (!waiting.value || hasActiveDecisionGroups.value)) return

  const id = room.id
  const hadPending = Math.max(1, pendingChanges.value)
  sending.value = true
  try {
    if (explicit && hasActiveDecisionGroups.value) {
      const applied = body.value?.applyRecommendedPicks?.() ?? 0
      if (applied > 0) {
        await nextTick()
        await new Promise((resolve) => setTimeout(resolve, 120))
      }
    }
    waiting.value = false
    pendingChanges.value = 0
    pendingNotes.value = 0
    const pictures = (await body.value?.markedPictures?.()) ?? []
    const projectId = artifact.value?.projectId ?? ''
    const images = await Promise.all(
      pictures.map(async (picture) => (await api.uploadFile(projectId, picture)).file.id),
    )
    const result = await api.sendFeedback(id, images)
    body.value?.markHeard?.()
    if (result?.feedback && result.feedback.round > 0) {
      clearSentRound()
      sentRound.value = { artifactId: id, round: result.feedback.round, hadPending }
      sentRoundTimer = setTimeout(clearSentRound, WITHDRAW_WINDOW_MS)
    }
  } catch (error) {
    if (explicit) {
      waiting.value = true
      pendingChanges.value = hadPending
      report(error)
    } else {
      // The server sends what waits on its own a little later, without the pictures.
      console.warn('feedback not sent', error)
    }
  } finally {
    sending.value = false
  }
}

async function withdrawSentFeedback() {
  const target = sentRound.value
  if (!target || withdrawing.value) return
  withdrawing.value = true
  try {
    const { withdrawn } = await api.withdrawFeedback(target.artifactId, target.round)
    clearSentRound()
    if (withdrawn) {
      waiting.value = true
      pendingChanges.value = target.hadPending
      scheduleQuietSend()
    } else {
      report(t('artifact.feedback.withdrawTooLate'))
    }
  } catch (error) {
    report(error)
  } finally {
    withdrawing.value = false
  }
}

const feedbackBarState = computed<'sent' | 'decision' | 'pending' | null>(() => {
  if (!artifact.value || viewingOld.value || artifact.value.kind === 'dashboard') return null
  if (sentRound.value && sentRound.value.artifactId === artifact.value.id) return 'sent'
  if (hasActiveDecisionGroups.value) return 'decision'
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

function leave() {
  clearSentRound()
  if (!room) return
  void sendFeedback(true)
  waiting.value = false
  pendingChanges.value = 0
  pendingNotes.value = 0
  room.scope.stop()
  room = null
  shared.value = null
}
onBeforeUnmount(leave)

// Going to another tab or window is leaving for a while: what waits goes now.
function hidden() {
  if (document.visibilityState === 'hidden') void sendFeedback()
}
document.addEventListener('visibilitychange', hidden)
// Closing the page cancels what is still being sent: what waits goes at once, without
// pictures, in a request that outlives the page.
function closing() {
  if (!room || hasActiveDecisionGroups.value || (!waiting.value && !sending.value)) return
  waiting.value = false
  pendingChanges.value = 0
  pendingNotes.value = 0
  void api.sendFeedback(room.id, [], true)
}
window.addEventListener('pagehide', closing)
onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', hidden)
  window.removeEventListener('pagehide', closing)
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
    void load()
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
    <div class="artifact-header">
      <component :is="kindIcon[artifact.kind]" v-if="artifact" class="artifact-kind" />
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
        <DouText weight="medium" truncate>{{ artifact?.title }}</DouText>
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
      />
    </div>
    <div v-if="feedbackBarState" class="artifact-feedback-bar" role="status" aria-live="polite">
      <template v-if="feedbackBarState === 'sent' && sentRound">
        <DouText size="sm" variant="secondary" truncate class="artifact-feedback-text">
          {{ t('artifact.feedback.sentRound', { round: sentRound.round }) }}
        </DouText>
        <DouButton size="sm" variant="ghost" :loading="withdrawing" @click="withdrawSentFeedback">
          <template #leading><UndoIcon /></template>
          {{ t('artifact.feedback.withdraw') }}
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
          @click="sendFeedback(false, true)"
        >
          <template #leading><SendIcon /></template>
          {{ t('artifact.feedback.submitRound') }}
        </DouButton>
      </template>
      <template v-else-if="feedbackBarState === 'pending'">
        <DouText size="sm" variant="secondary" truncate class="artifact-feedback-text">
          {{
            pendingChanges === 1
              ? t('artifact.feedback.pendingChanges')
              : t('artifact.feedback.pendingCount', { count: pendingChanges })
          }}
        </DouText>
        <DouButton
          size="sm"
          variant="soft"
          tone="primary"
          :loading="sending"
          @click="sendFeedback(false, true)"
        >
          <template #leading><SendIcon /></template>
          {{ t('artifact.feedback.sendNow') }}
        </DouButton>
      </template>
    </div>
  </div>
</template>

<style scoped>
.artifact {
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
  display: flex;
  flex: none;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 6px 12px 6px 16px;
  border-top: 1px solid var(--dou-color-border);
  background: var(--dou-color-surface-subtle, var(--dou-color-surface, var(--dou-color-bg)));
}

.artifact-feedback-text {
  flex: 1;
  min-width: 0;
  font-variant-numeric: tabular-nums;
}
</style>
