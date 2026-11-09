<script setup lang="ts">
import type { BranchChanges, ChangedFile, FileVersions, MergeMethod } from '@ariadne/plugin-sdk'
import { DouCodeDiff } from '@dou-ui/code'
import '@dou-ui/code/style.css'
import {
  DouAlert,
  DouButton,
  DouEmpty,
  DouPopover,
  DouSegmentedControl,
  DouSpinner,
  DouTag,
  DouText,
  DouTextarea,
  DouTree,
  type TreeNodeData,
} from 'dou-ui'
import {
  ChevronDownIcon,
  fileIconOf,
  GitMergeIcon,
  MessageIcon,
  ScanSearchIcon,
  RotateCwIcon,
} from '@dou-ui/icons'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api, ApiError } from '../api'
import { t, tm } from '../i18n'
import { closeView, openView, report, reviewThread, state, type TurnChangesRef } from '../store'
import PullRequestTag from '../components/PullRequestTag.vue'
import ViewActions from '../full/ViewActions.vue'
import ChangedFiles from './ChangedFiles.vue'
import { fileTree, folderKeys, lineTotals, mostChanged, type FileNode } from './files'
import { draftsChanged, flushComments } from './comment-sender'
import { languageOf } from './languages'
import { draftsOf, useLineComments } from './line-comments'
import type { ReviewRoundView, SentLineComment } from '@ariadne/core'
import ReviewMark from '../review/ReviewMark.vue'
import { reviewNotes, useReviewComments } from '../review/review-comments'

/**
 * What a thread's branch changed against its base, with merging it; or, with `turn`, what
 * one of its turns changed in the workspace. Lines of either can be commented on, and the
 * comments go to the thread together.
 */
const props = defineProps<{
  threadId: string
  turn?: TurnChangesRef
  path?: string
  /** Where it is shown: in a tab on the right side, or full screen. */
  place: 'side' | 'full'
}>()
const view = computed(() => ({
  kind: 'changes' as const,
  threadId: props.threadId,
  turn: props.turn,
  path: props.path,
}))

const thread = computed(
  () => state.detail?.threads.find((item) => item.id === props.threadId) ?? null,
)

// Too narrow for the file list beside the diff (a tab on the side): the list folds into
// the file's name above the diff.
const root = ref<HTMLElement | null>(null)
const narrow = ref(false)
let resized: ResizeObserver | null = null
onMounted(() => {
  resized = new ResizeObserver(([entry]) => {
    narrow.value = (entry?.contentRect.width ?? Infinity) < NARROW_PX
  })
  if (root.value) resized.observe(root.value)
})
onBeforeUnmount(() => resized?.disconnect())
const filesOpen = ref(false)

// One turn's changes or the whole branch's, from one to the other in place.
const scope = computed(() => (props.turn ? 'turn' : 'branch'))
const scopeOptions = computed(() => [
  { value: 'turn', label: t('changes.scopeTurn') },
  { value: 'branch', label: t('changes.scopeBranch') },
])
const turnRef = ref<TurnChangesRef | null>(null)
watch(
  () => props.turn,
  (turn) => {
    if (turn) turnRef.value = turn
  },
  { immediate: true },
)
function switchScope(value: unknown) {
  if (value === scope.value) return
  const next =
    value === 'turn' && turnRef.value
      ? { kind: 'changes' as const, threadId: props.threadId, turn: turnRef.value }
      : { kind: 'changes' as const, threadId: props.threadId }
  const before = view.value
  // Full screen keeps the thread beside it; a tab gives its place to the other.
  void openView(next, props.place === 'full' ? { full: true, from: state.full?.threadId } : {})
  if (props.place === 'side') closeView(before)
}
const changes = ref<BranchChanges | null>(null)
const mergeable = ref(false)
// Handed to the user for review, it can also end without a merge here: they let it go, or
// took it in some other way. The review's way out is "go merge", so this is the other one.
const reviewing = computed(() => thread.value?.stage === 'review' && !props.turn)
async function skipMerge() {
  await reviewThread(props.threadId)
  closeView(view.value)
}
const loading = ref(true)
const problem = ref<string | null>(null)

const tree = computed(() => fileTree(changes.value?.files ?? []))
const expanded = ref<string[]>([])
const selected = ref<string | null>(null)
const file = computed<ChangedFile | null>(
  () => changes.value?.files.find((item) => item.path === selected.value) ?? null,
)
const totals = computed(() => lineTotals(changes.value?.files ?? []))

/** The file to open: the one asked for, the open one while still changed, or the first. */
function pickFile(files: ChangedFile[]) {
  const has = (path: string | null | undefined) => !!path && files.some((f) => f.path === path)
  if (has(props.path) && !opened.value) selected.value = props.path!
  else if (!has(selected.value)) selected.value = mostChanged(files)?.path ?? null
  opened.value = true
}
const opened = ref(false)

/** Below this width, a tab on the side, the file list folds into the file's name. */
const NARROW_PX = 720

async function load() {
  if (props.turn) {
    const { turn } = props
    changes.value = {
      branch: thread.value?.workspace?.branch ?? '',
      base: thread.value?.workspace?.base ?? '',
      from: turn.from,
      head: turn.to,
      commits: [],
      files: turn.files,
      uncommitted: [],
    }
    mergeable.value = false
    problem.value = null
    loading.value = false
    expanded.value = folderKeys(tree.value)
    pickFile(turn.files)
    return
  }
  try {
    const result = await api.getChanges(props.threadId)
    changes.value = result.changes
    mergeable.value = result.mergeable
    problem.value = null
    expanded.value = folderKeys(tree.value)
    pickFile(result.changes?.files ?? [])
  } catch (error) {
    problem.value = error instanceof ApiError ? tm(error.localized) : String(error)
  } finally {
    loading.value = false
  }
}

// Again whenever the thread moves on: it may have committed since. The thread is replaced
// only when something of it changed, and `activeAt` does at every step, which is what runs
// this and the two watches further down while it works.
watch(
  () => [props.threadId, props.turn?.turnId, thread.value?.stage, thread.value?.merged?.sha],
  (now, before) => {
    // Another thread or turn starts over at the file it asks for.
    if (!before || now[0] !== before[0] || now[1] !== before[1]) opened.value = false
    void load()
  },
  { immediate: true },
)

const versions = ref<FileVersions | null>(null)
const versionsKey = ref('')
watch(
  () => [file.value, changes.value?.from, changes.value?.head] as const,
  async ([current, from, head]) => {
    if (!current || !from || !head) {
      versions.value = null
      return
    }
    const key = `${from}:${head}:${current.path}`
    if (key === versionsKey.value) return
    versionsKey.value = key
    versions.value = null
    try {
      const { file: loaded } = await api.getChangedFile(props.threadId, {
        from,
        head,
        path: current.path,
        previousPath: current.previousPath,
      })
      if (versionsKey.value === key) versions.value = loaded
    } catch (error) {
      report(error)
    }
  },
  { immediate: true },
)

function select(node: TreeNodeData) {
  const { file: changed } = node as FileNode
  if (changed) selected.value = changed.path
}

// Merged as it stands: nothing came after the last merge.
const upToDate = computed(
  () => !!thread.value?.merged && thread.value.merged.head === changes.value?.head,
)
const base = computed(() => changes.value?.base ?? thread.value?.workspace?.base ?? '')

const mergeOpen = ref(false)
// The way this project was last merged; the autopilot uses the same.
const method = ref<MergeMethod>(state.detail?.project.settings.autopilot.method ?? 'squash')
const message = ref('')
const merging = ref(false)
const result = ref<{ tone: 'warning' | 'neutral'; text: string } | null>(null)
// Comments waiting go first; a thread still at work, on them or anything else, would keep
// changing what was merged.
const busy = computed(() => {
  const stage = thread.value?.stage
  return stage === 'working' || stage === 'queued' || drafts.value.length > 0
})
watch(mergeOpen, (open) => {
  if (!open) return
  method.value = state.detail?.project.settings.autopilot.method ?? method.value
  if (!message.value) message.value = thread.value?.title ?? ''
  void flushComments(props.threadId)
})

async function merge() {
  merging.value = true
  result.value = null
  try {
    const { outcome } = await api.mergeThread(props.threadId, {
      method: method.value,
      message: message.value,
    })
    mergeOpen.value = false
    if (outcome.kind === 'conflict') {
      result.value = {
        tone: 'warning',
        text: t('changes.conflict', { base: base.value, files: outcome.files.join('、') }),
      }
    } else if (outcome.kind === 'empty') {
      result.value = { tone: 'neutral', text: t('changes.nothing', { base: base.value }) }
    } else {
      // Merged: nothing is left to show.
      closeView(view.value)
      return
    }
    await load()
  } catch (error) {
    mergeOpen.value = false
    result.value = {
      tone: 'warning',
      text: error instanceof ApiError ? tm(error.localized) : String(error),
    }
  } finally {
    merging.value = false
  }
}

// What the user sent the thread about its lines, kept to show on them.
const sent = ref<SentLineComment[]>([])
watch(
  () => [props.threadId, thread.value?.activeAt] as const,
  async ([id]) => {
    try {
      const { comments } = await api.getLineComments(id)
      if (props.threadId === id) sent.value = comments
    } catch (error) {
      report(error)
    }
  },
  { immediate: true },
)

// Comments on lines of either version, sent to the thread a moment after.
const lineComments = useLineComments(
  () => props.threadId,
  () => file.value?.path ?? null,
  () =>
    versions.value && !versions.value.skipped
      ? { original: versions.value.before ?? '', modified: versions.value.after ?? '' }
      : null,
  // As with the reviewer's: what was sent is about the branch, not one turn of it.
  () => (props.turn ? [] : sent.value),
)
// The reviewer's comments on the branch, with what the author did about each.
const reviews = ref<ReviewRoundView[]>([])
const notes = computed(() => (props.turn ? [] : reviewNotes(reviews.value)))
watch(
  () => [props.threadId, thread.value?.review?.rounds, thread.value?.review?.reviewing] as const,
  async ([id, rounds]) => {
    if (!rounds) {
      reviews.value = []
      return
    }
    try {
      const { rounds: found } = await api.getReviews(id)
      if (props.threadId === id) reviews.value = found
    } catch (error) {
      report(error)
    }
  },
  { immediate: true },
)
const reviewComments = useReviewComments(
  () => props.threadId,
  () => notes.value,
  () => file.value?.path ?? null,
  () => versions.value?.after ?? null,
)
const reviewCount = (path: string) => notes.value.filter((n) => n.comment.path === path).length
const gutters = computed(() => [lineComments.gutter.value, reviewComments.gutter.value])
const widgets = computed(() => [...lineComments.widgets.value, ...reviewComments.widgets.value])
// Side by side where there is room, else one column, until the toolbar switches it. A file
// only one side has (added or deleted) is one column: the other would be empty.
const diffMode = ref<'split' | 'inline' | 'auto'>('auto')
const oneSided = computed(
  () => !!versions.value && (!versions.value.before || !versions.value.after),
)
const drafts = computed(() => draftsOf(props.threadId))
const commentCount = (path: string) =>
  drafts.value.filter((c) => c.path === path).length +
  sent.value.filter((c) => c.path === path).length
// Comments go to the thread on their own once the user stops writing them; leaving sends
// what waits at once.
watch(drafts, () => draftsChanged(props.threadId), { deep: true })
onBeforeUnmount(() => void flushComments(props.threadId))

const methodOptions = computed(() => [
  { value: 'squash', label: t('changes.method.squash') },
  { value: 'merge', label: t('changes.method.merge') },
])
</script>

<template>
  <div ref="root" class="changes" :class="[`is-${place}`, { 'is-narrow': narrow }]">
    <div class="changes-header">
      <div class="changes-title">
        <DouText weight="medium" truncate>{{ thread?.title }}</DouText>
        <DouText v-if="turn" variant="tertiary" size="sm" class="changes-meta">
          {{ t('changes.files', { count: turn.total.files }) }}
          <span class="changes-added">+{{ turn.total.added }}</span>
          <span class="changes-removed">−{{ turn.total.removed }}</span>
        </DouText>
        <DouText v-else-if="changes" variant="tertiary" size="sm" class="changes-meta">
          {{ changes.branch }} → {{ changes.base }} ·
          {{ t('changes.commits', { count: changes.commits.length }) }} ·
          {{ t('changes.files', { count: changes.files.length }) }}
          <span class="changes-added">+{{ totals.added }}</span>
          <span class="changes-removed">−{{ totals.removed }}</span>
        </DouText>
      </div>
      <DouSegmentedControl
        v-if="turnRef"
        :model-value="scope"
        :options="scopeOptions"
        size="sm"
        class="changes-scope"
        @update:model-value="switchScope"
      />
      <ReviewMark v-if="thread && !turn" :thread="thread" />
      <DouButton
        v-if="!turn"
        variant="ghost"
        size="sm"
        :aria-label="t('changes.refresh')"
        :title="t('changes.refresh')"
        @click="load"
      >
        <RotateCwIcon />
      </DouButton>
      <PullRequestTag v-if="thread && !mergeable && !turn" :thread="thread" />
      <DouTag v-if="mergeable && upToDate" tone="success" size="sm">
        <template #leading><GitMergeIcon /></template>
        {{ t('changes.merged', { base }) }}
      </DouTag>
      <DouButton v-if="reviewing" size="sm" variant="ghost" @click="skipMerge">
        {{ t('changes.skipMerge') }}
      </DouButton>
      <DouPopover
        v-if="mergeable && !upToDate && changes?.files.length"
        v-model:open="mergeOpen"
        trigger="click"
        placement="bottom-end"
        :title="t('changes.merge', { base })"
        :width="340"
      >
        <DouButton size="sm" tone="primary">
          <template #leading><GitMergeIcon /></template>
          {{ t('changes.merge', { base }) }}
        </DouButton>
        <template #content>
          <form class="merge-form" @submit.prevent="merge">
            <DouAlert v-if="busy" tone="warning" size="sm">{{ t('changes.mergeBusy') }}</DouAlert>
            <DouSegmentedControl v-model="method" :options="methodOptions" size="sm" block />
            <DouTextarea
              v-model="message"
              :aria-label="t('changes.message')"
              :placeholder="t('changes.message')"
              :rows="3"
              autosize
            />
            <DouButton size="sm" tone="primary" native-type="submit" :loading="merging">
              {{ t('changes.mergeConfirm') }}
            </DouButton>
          </form>
        </template>
      </DouPopover>
      <ViewActions :view="view" :place="place" />
    </div>

    <DouAlert v-if="problem" tone="danger" class="changes-alert">{{ problem }}</DouAlert>
    <DouAlert
      v-if="result"
      :tone="result.tone"
      closable
      class="changes-alert"
      @close="result = null"
    >
      {{ result.text }}
    </DouAlert>
    <DouAlert v-if="changes?.uncommitted.length" tone="neutral" class="changes-alert">
      {{ t('changes.uncommitted', { count: changes.uncommitted.length }) }}
    </DouAlert>

    <div v-if="loading" class="changes-empty"><DouSpinner /></div>
    <div v-else-if="!changes || !changes.files.length" class="changes-empty">
      <DouEmpty
        :description="changes ? t('changes.empty', { base: changes.base }) : t('changes.noBranch')"
      />
    </div>
    <div v-else class="changes-body">
      <div v-if="!narrow" class="changes-files">
        <ChangedFiles
          v-model:expanded="expanded"
          :tree="tree"
          :selected="selected"
          :count="changes.files.length"
          :review-count="reviewCount"
          :comment-count="commentCount"
          @select="select"
        />
      </div>
      <div class="changes-diff">
        <div v-if="file" class="diff-head">
          <DouPopover
            v-if="narrow"
            v-model:open="filesOpen"
            trigger="click"
            placement="bottom-start"
            :width="320"
          >
            <DouButton variant="ghost" size="sm" class="diff-file">
              <span class="diff-file-path">{{ file.path }}</span>
              <DouText variant="tertiary" size="sm" class="diff-file-count">
                {{ changes.files.length }}
              </DouText>
              <template #trailing><ChevronDownIcon /></template>
            </DouButton>
            <template #content>
              <ChangedFiles
                v-model:expanded="expanded"
                :tree="tree"
                :selected="selected"
                :count="changes.files.length"
                :review-count="reviewCount"
                :comment-count="commentCount"
                class="diff-file-tree"
                @select="(node) => ((filesOpen = false), select(node))"
              />
            </template>
          </DouPopover>
          <DouText v-else size="sm" weight="medium" truncate>{{ file.path }}</DouText>
          <DouText v-if="file.previousPath" variant="tertiary" size="sm" truncate>
            {{ t('changes.renamed', { path: file.previousPath }) }}
          </DouText>
        </div>
        <div v-if="!versions" class="changes-empty"><DouSpinner /></div>
        <div v-else-if="versions.skipped" class="changes-empty">
          <DouEmpty
            :description="t(versions.skipped === 'binary' ? 'changes.binary' : 'changes.large')"
          />
        </div>
        <DouCodeDiff
          v-else
          :key="versionsKey"
          class="diff"
          :original="versions.before ?? ''"
          :model-value="versions.after ?? ''"
          :language="languageOf(file?.path ?? '')"
          :gutters="gutters"
          :widgets="widgets"
          :mode="oneSided ? 'inline' : diffMode"
          wrap
          @update:mode="(mode: 'split' | 'inline') => (diffMode = mode)"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.changes {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.changes-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px 4px 16px;
  border-bottom: 1px solid var(--dou-color-border);
}

/* A tab on the side is narrow: what does not fit goes to a second line. */
.is-side .changes-header {
  flex-wrap: wrap;
  row-gap: 4px;
}

.changes-title {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
}

.changes-meta {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.changes-added {
  color: var(--dou-color-success);
  margin-left: 4px;
}

.changes-removed {
  color: var(--dou-color-danger);
  margin-left: 4px;
}

.changes-alert {
  margin: 8px 16px 0;
}

.changes-empty {
  flex: 1;
  display: grid;
  place-items: center;
}

.changes-body {
  display: flex;
  flex: 1;
  min-height: 0;
}

.changes-files {
  flex: none;
  width: 240px;
  max-width: 35%;
  overflow: auto;
  padding: 8px;
  border-right: 1px solid var(--dou-color-border-subtle);
}

.changes-diff {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
}

.diff-file {
  min-width: 0;
  max-width: 100%;
  margin-left: -8px;
}

.diff-file-path {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.diff-file-count {
  margin-left: 6px;
}

.diff-file-tree {
  max-height: 60vh;
  overflow: auto;
}

.changes-scope {
  flex: none;
}

.diff-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px 6px;
}

/* The diff draws its own frame, with the change count in it. */
.diff {
  flex: 1;
  min-height: 0;
  margin: 0 12px 12px;
  width: auto;
}

/* Beside a line: a mark where comments are. */
.diff :deep(.line-comment-mark) {
  color: var(--dou-color-primary);
}

.diff :deep(.line-comment-sent-mark),
.diff :deep(.review-comment-mark) {
  color: var(--dou-color-text-tertiary);
}

.merge-form {
  display: flex;
  flex-direction: column;
  gap: 8px;
  align-items: stretch;
}
</style>
