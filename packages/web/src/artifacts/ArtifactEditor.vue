<script setup lang="ts">
import type {
  ArtifactCommentAction,
  ArtifactCommentThreadView,
  ArtifactVersionView,
  ArtifactView,
} from '@ariadne/core'
import { DouAlert, DouButton, DouInput, DouSelect, DouText, type CommentAction } from 'dou-ui'
import { CheckIcon, PencilIcon, RotateCcwIcon, SaveIcon } from '@dou-ui/icons'
import { computed, defineAsyncComponent, nextTick, ref, watch } from 'vue'
import { api, ApiError } from '../api'
import { locale, t, tm } from '../i18n'
import { report, state, unsavedEdits } from '../store'
import ViewActions from '../full/ViewActions.vue'
import { authorName } from './author'
import CommentList from './CommentList.vue'
import { kindIcon } from './kinds'

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
const body = ref<{ quotes?: () => Record<string, string>; anchored?: () => Set<string> } | null>(
  null,
)
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
const dirty = computed(() => !viewingOld.value && draft.value !== null)

const versionOptions = computed(() =>
  versions.value.map((v) => ({
    value: v.version,
    label: `${t('artifact.version', { version: v.version })} · ${authorName(v.author)}${v.reason ? ` · ${v.reason}` : ''}`,
  })),
)

/** What the editor starts from: the version shown, or edits carried over from elsewhere. */
const bodyContent = ref('')

function show(next: ArtifactView) {
  artifact.value = next
  bodyContent.value = next.content
  draft.value = null
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
  else unsavedEdits.set(props.id, { version: latest.value, content })
})

async function load(version?: number) {
  try {
    const detail = await api.getArtifact(props.id, version)
    versions.value = detail.versions
    comments.value = detail.comments
    latest.value = detail.versions[0]?.version ?? detail.artifact.version
    show(detail.artifact)
    const carried = unsavedEdits.get(props.id)
    if (version === undefined && carried?.version === latest.value) {
      bodyContent.value = carried.content
      draft.value = carried.content
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

/** Comments go to the newest version of a document only. */
const commentProps = computed(() =>
  artifact.value?.kind === 'document' && !viewingOld.value
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
function toAction(action: CommentAction, quote: string): ArtifactCommentAction | null {
  switch (action.type) {
    case 'create': {
      const first = action.thread.comments[0]
      if (!first || !quote) return null
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

async function comment(action: CommentAction, quote = '') {
  const sent = toAction(action, quote)
  if (!sent) return
  try {
    comments.value = (await api.commentArtifact(props.id, sent)).comments
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
  conflict.value = null
  try {
    const detail = await api.saveArtifact(props.id, {
      content: input.content ?? draft.value ?? current.content,
      baseVersion: latest.value,
      reason: input.reason ?? t('artifact.saveReason'),
      ...(input.title ? { title: input.title } : {}),
      ...(body.value?.quotes ? { quotes: body.value.quotes() } : {}),
    })
    versions.value = detail.versions
    comments.value = detail.comments
    latest.value = detail.artifact.version
    show(detail.artifact)
    return true
  } catch (error) {
    if (error instanceof ApiError && error.status === 409) conflict.value = tm(error.localized)
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
  if (
    await save({
      content: artifact.value?.content,
      title: next,
      reason: t('artifact.renameReason'),
    })
  ) {
    renaming.value = false
    draft.value = kept
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
      <DouButton
        v-else
        size="sm"
        tone="primary"
        :disabled="!dirty"
        :loading="saving && !renaming"
        @click="save()"
      >
        <template #leading><SaveIcon /></template>
        {{ t('artifact.save') }}
      </DouButton>
      <ViewActions :view="{ kind: 'artifact', id }" :place="place" />
    </div>
    <DouAlert v-if="viewingOld" tone="neutral" class="artifact-alert">
      {{ t('artifact.viewing', { version: artifact?.version ?? 0 }) }}
    </DouAlert>
    <DouAlert v-else-if="conflict" tone="warning" class="artifact-alert">
      {{ conflict }}
    </DouAlert>
    <DouText v-else-if="dirty" variant="tertiary" size="sm" class="artifact-dirty">
      {{ t('artifact.unsaved') }}
    </DouText>
    <div class="artifact-body">
      <component
        :is="bodies[artifact.kind]"
        v-if="artifact"
        ref="body"
        v-bind="commentProps"
        :content="bodyContent"
        :project-id="artifact.projectId"
        :artifact-id="artifact.id"
        :readonly="viewingOld"
        :label="artifact.title"
        @draft="onDraft"
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

.artifact-dirty {
  padding: 4px 16px 0;
}

.artifact-body {
  flex: 1;
  min-height: 0;
}
</style>
