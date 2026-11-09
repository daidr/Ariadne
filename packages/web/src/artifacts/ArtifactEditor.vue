<script setup lang="ts">
import type {
  ArtifactCommentAction,
  ArtifactCommentThreadView,
  ArtifactVersionView,
  ArtifactView,
} from '@ariadne/core'
import { DouAlert, DouButton, DouInput, DouSelect, DouText, type CommentAction } from 'dou-ui'
import { CheckIcon, PencilIcon, RotateCcwIcon } from '@dou-ui/icons'
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
import { locale, t, tm } from '../i18n'
import { report, state, unsavedEdits } from '../store'
import ViewActions from '../full/ViewActions.vue'
import { authorName } from './author'
import CommentList from './CommentList.vue'
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
const versions = ref<ArtifactVersionView[]>([])
const comments = ref<ArtifactCommentThreadView[]>([])
/** The comment thread open in the text. */
const activeComment = ref<string | null>(null)
const body = ref<{
  quotes?: () => Record<string, string>
  anchored?: () => Set<string>
  markedPictures?: () => Promise<Blob[]>
  markHeard?: () => void
} | null>(null)
/** The newest version; an older one is shown read-only. */
const latest = ref(0)
/** Content edited and not saved yet; `null` when nothing changed. */
const draft = ref<string | null>(null)
const saving = ref(false)
const conflict = ref<string | null>(null)
const renaming = ref(false)
const title = ref('')
const titleInput = ref<InstanceType<typeof DouInput> | null>(null)

const viewingOld = computed(() => !!artifact.value && artifact.value.version !== latest.value)
/** Documents and whiteboards are edited together: changes go out as they are made. */
const together = computed(() => artifact.value?.kind !== 'dashboard')
const dirty = computed(() => !viewingOld.value && !together.value && draft.value !== null)

const versionOptions = computed(() =>
  versions.value.map((v) => ({
    value: v.version,
    label: `${t('artifact.version', { version: v.version })} · ${authorName(v.author)}${v.reason ? ` · ${v.reason}` : ''}`,
  })),
)

/** What the editor starts from: the version shown, or edits carried over from elsewhere. */
const bodyContent = ref('')
/**
 * The version carried edits were made on, when someone saved a newer one meanwhile: saving
 * them names it, so the server turns them down as a conflict instead of writing over it.
 */
const draftBase = ref<number | null>(null)

function show(next: ArtifactView) {
  artifact.value = next
  bodyContent.value = next.content
  draft.value = null
  draftBase.value = null
}

/** The body reports no draft once it is back where it started, which may be carried edits. */
function onDraft(content: string | null) {
  const carried = bodyContent.value !== artifact.value?.content ? bodyContent.value : null
  draft.value = content ?? carried
}

// Unsaved edits outlive this editor: taking the artifact full screen or back to the side
// shows it anew, with the edits still there.
watch(draft, (content) => {
  // Looking at an older version keeps them.
  if (viewingOld.value) return
  if (content === null) unsavedEdits.delete(props.id)
  else unsavedEdits.set(props.id, { version: draftBase.value ?? latest.value, content })
})

async function load(version?: number) {
  try {
    const detail = await api.getArtifact(props.id, version)
    versions.value = detail.versions
    comments.value = detail.comments
    latest.value = detail.versions[0]?.version ?? detail.artifact.version
    show(detail.artifact)
    const carried = unsavedEdits.get(props.id)
    if (version === undefined && carried) {
      bodyContent.value = carried.content
      draft.value = carried.content
      if (carried.version !== latest.value) draftBase.value = carried.version
    }
    conflict.value = null
  } catch (error) {
    report(error)
  }
}

watch(
  () => props.id,
  () => {
    renaming.value = false
    void load()
  },
  { immediate: true },
)
// Someone else saved (an agent, or this user in another tab): reload unless edits are open.
watch(
  () => state.artifacts.find((a) => a.id === props.id)?.version,
  (version) => {
    if (version && version !== latest.value && !dirty.value && !viewingOld.value) void load()
  },
)
// Someone commented (an agent answering): take the comments in, leaving edits open.
watch(
  () => state.artifacts.find((a) => a.id === props.id)?.commentedAt,
  async (at, before) => {
    if (!at || at === before) return
    try {
      comments.value = (await api.getArtifact(props.id)).comments
    } catch (error) {
      report(error)
    }
  },
)

// ---- editing together ----

/** The room of the artifact open now, joined while its newest version is shown. */
const shared = shallowRef<SharedArtifact | null>(null)
let room: { id: string; scope: EffectScope } | null = null
watch(
  () => (artifact.value && together.value && !viewingOld.value ? artifact.value.id : null),
  (id) => {
    if (room?.id === id) return
    leave()
    if (!id) return
    const scope = effectScope()
    shared.value = scope.run(() => useSharedArtifact(id)) ?? null
    room = { id, scope }
    shared.value?.doc.on('update', (_update: Uint8Array, origin: unknown) => {
      // The page's own edits; what comes from the server is the provider's.
      if (origin !== shared.value?.provider.value) touched()
    })
  },
)

/**
 * What the user does here reaches the agent on its own, a moment after they stop: the
 * changes, comments and picks together, with pictures of the options they marked.
 */
const QUIET_MS = 10_000
let quiet: ReturnType<typeof setTimeout> | null = null
let waiting = false
/** A round is being sent with pictures, which takes a moment. */
let sending = false

function touched() {
  waiting = true
  if (quiet) clearTimeout(quiet)
  quiet = setTimeout(() => void sendFeedback(), QUIET_MS)
}

async function sendFeedback(leaving = false) {
  if (quiet) clearTimeout(quiet)
  quiet = null
  if (!waiting || !room) return
  waiting = false
  const id = room.id
  if (leaving) return api.sendFeedback(id, [], true)
  sending = true
  try {
    const pictures = (await body.value?.markedPictures?.()) ?? []
    const projectId = artifact.value?.projectId ?? ''
    const images = await Promise.all(
      pictures.map(async (picture) => (await api.uploadFile(projectId, picture)).file.id),
    )
    await api.sendFeedback(id, images)
    body.value?.markHeard?.()
  } catch (error) {
    // The server sends what waits on its own a little later, without the pictures.
    console.warn('feedback not sent', error)
  } finally {
    sending = false
  }
}

function leave() {
  if (!room) return
  void sendFeedback(true)
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
  if (!room || (!waiting && !sending)) return
  waiting = false
  void api.sendFeedback(room.id, [], true)
}
window.addEventListener('pagehide', closing)
onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', hidden)
  window.removeEventListener('pagehide', closing)
})

/** Comments go to the newest version of a document or whiteboard. */
const commentProps = computed(() =>
  artifact.value && together.value && !viewingOld.value
    ? {
        comments: comments.value,
        activeComment: activeComment.value,
        'onUpdate:activeComment': (id: string | null) => (activeComment.value = id),
        onComment: comment,
      }
    : {},
)

/** The longest quote the server keeps (MAX_COMMENT_QUOTE). */
const MAX_QUOTE = 1000

/** The editor's comment change, sent as the server takes it. */
function toAction(
  action: CommentAction,
  quote: string | { node: string } | { edge: string } | null,
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
          quote: '',
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
  quote: string | { node: string } | { edge: string } | null = '',
) {
  const sent = toAction(action, quote)
  if (!sent) return
  if (sent.type === 'create' || sent.type === 'reply') touched()
  try {
    comments.value = (await api.commentArtifact(props.id, sent)).comments
  } catch (error) {
    report(error)
    void load()
  }
}

/**
 * A dashboard's layout is saved when the user is done arranging it, or leaves with changes;
 * documents and whiteboards are written as they are edited.
 */
function autosave() {
  if (dirty.value && !saving.value) void save()
}
onBeforeUnmount(() => {
  if (!dirty.value) return
  // The editor shown next (full screen, or the tab opened again) starts from what is
  // saved, not from these edits a second time.
  unsavedEdits.delete(props.id)
  if (!saving.value) void save()
})

async function save(
  input: { content?: string; title?: string; reason?: string } = {},
): Promise<boolean> {
  const current = artifact.value
  if (!current) return false
  saving.value = true
  conflict.value = null
  try {
    const detail = await api.saveArtifact(props.id, {
      // Edited together, the room holds the content: a rename leaves it as it is there.
      content:
        together.value && input.content === undefined
          ? null
          : (input.content ?? draft.value ?? current.content),
      // Content given here is the newest version's own (a rename, a restore), not the edits.
      baseVersion: input.content === undefined ? (draftBase.value ?? latest.value) : latest.value,
      reason: input.reason ?? t('artifact.saveReason'),
      ...(input.title ? { title: input.title } : {}),
      ...(body.value?.quotes ? { quotes: body.value.quotes() } : {}),
    })
    versions.value = detail.versions
    comments.value = detail.comments
    latest.value = detail.artifact.version
    // The edits are saved; said here too, as nothing watches the draft once the editor is gone.
    if (input.content === undefined) unsavedEdits.delete(props.id)
    show(detail.artifact)
    return true
  } catch (error) {
    if (error instanceof ApiError && error.status === 409) {
      // Someone saved meanwhile: theirs is shown, and the user is told theirs was not kept.
      const lost = tm(error.localized)
      unsavedEdits.delete(props.id)
      draft.value = null
      await load()
      conflict.value = lost
    }
    // The reason is written for the agent; the user gets a plain line.
    else if (error instanceof ApiError && error.localized.key === 'error.artifactInvalid') {
      state.error = t('artifact.invalidSave')
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
  // A rename carries no unsaved edits with it: those stay a draft until saved.
  const kept = draft.value
  const keptBase = draftBase.value
  if (
    await save({
      ...(together.value ? {} : { content: artifact.value?.content }),
      title: next,
      reason: t('artifact.renameReason'),
    })
  ) {
    renaming.value = false
    // The editor goes on showing the edits, not the version just saved.
    if (kept !== null) bodyContent.value = kept
    draft.value = kept
    draftBase.value = keptBase
  }
}

const time = (at: number) =>
  new Date(at).toLocaleString(locale.value, { dateStyle: 'short', timeStyle: 'short' })
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
      <DouText
        v-if="artifact && place === 'full'"
        variant="tertiary"
        size="sm"
        class="artifact-meta"
      >
        {{
          t('artifact.updated', {
            who: authorName(artifact.updatedBy),
            time: time(artifact.updatedAt),
          })
        }}
      </DouText>
      <CommentList
        v-if="artifact?.kind === 'document' && !viewingOld"
        :comments="comments"
        :anchored="() => body?.anchored?.() ?? new Set()"
        @open="(id: string) => (activeComment = id)"
        @action="comment"
      />
      <div v-if="versionOptions.length > 1" class="artifact-versions">
        <DouSelect
          :model-value="artifact?.version ?? null"
          :options="versionOptions"
          size="sm"
          :aria-label="t('artifact.versions')"
          @update:model-value="(value) => load(Number(value))"
        />
      </div>
      <DouButton v-if="viewingOld" size="sm" variant="outline" :loading="saving" @click="restore">
        <template #leading><RotateCcwIcon /></template>
        {{ t('artifact.restore') }}
      </DouButton>
      <ViewActions :view="{ kind: 'artifact', id }" :place="place" />
    </div>
    <DouAlert v-if="viewingOld" tone="neutral" class="artifact-alert">
      {{ t('artifact.viewing', { version: artifact?.version ?? 0 }) }}
    </DouAlert>
    <DouAlert v-else-if="conflict && !together" tone="warning" class="artifact-alert">
      {{ conflict }}
    </DouAlert>
    <div class="artifact-body">
      <component
        :is="bodies[artifact.kind]"
        v-if="artifact && (!together || viewingOld || shared)"
        :key="`${artifact.id}:${viewingOld ? artifact.version : 'live'}`"
        ref="body"
        v-bind="commentProps"
        :shared="viewingOld ? undefined : (shared ?? undefined)"
        :content="bodyContent"
        :project-id="artifact.projectId"
        :artifact-id="artifact.id"
        :readonly="viewingOld"
        :label="artifact.title"
        @draft="onDraft"
        @settled="autosave"
      />
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

.artifact-meta {
  flex: none;
  white-space: nowrap;
}

.artifact-versions {
  flex: none;
  width: 200px;
}

/* A tab on the side is narrow: the version list gives up room before the title does. */
.is-side .artifact-versions {
  flex: 0 1 160px;
  min-width: 96px;
}

.artifact-alert {
  margin: 8px 16px 0;
}

.artifact-body {
  flex: 1;
  min-height: 0;
}
</style>
