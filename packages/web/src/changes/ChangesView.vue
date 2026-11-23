<script setup lang="ts">
import type { BranchChanges, ChangedFile, MergeMethod } from '@ariadne/plugin-sdk'
import {
  DouAlert,
  DouButton,
  DouDropdownItem,
  DouEmpty,
  DouPopover,
  DouSegmentedControl,
  DouSpinner,
  DouSplitter,
  DouTag,
  DouText,
  DouTextarea,
  DouVirtualList,
  toast,
  useElementSize,
  type TreeNodeData,
  type VirtualListExpose,
} from 'dou-ui'
import {
  AlertTriangleIcon,
  ArchiveIcon,
  ChevronDownIcon,
  GitMergeIcon,
  PanelLeftIcon,
} from '@dou-ui/icons'
import { computed, nextTick, onBeforeUnmount, ref, useTemplateRef, watch } from 'vue'
import { api } from '../api'
import { t } from '../i18n'
import { useSidebarLayout } from '../sidebar-layout'
import {
  closeView,
  openView,
  report,
  completeThread,
  onLiveEvent,
  state,
  type TurnChangesRef,
} from '../store'
import PullRequestTag from '../components/PullRequestTag.vue'
import ThreadTools from '../components/ThreadTools.vue'
import ViewActions from '../full/ViewActions.vue'
import ChangedFileDiff from './ChangedFileDiff.vue'
import ChangedFiles from './ChangedFiles.vue'
import { fileTree, folderKeys, lineTotals, orderedFiles, type FileNode } from './files'
import { draftsChanged, flushComments } from './comment-sender'
import { draftsOf } from './line-comments'
import type { ReviewRoundView, SentLineComment } from '@ariadne/core'
import ReviewMark from '../review/ReviewMark.vue'
import { reviewNotes } from '../review/review-comments'
import { sameComparison, useBranchUpdates } from './branch-updates'
import { fileVersionsFor } from './file-versions'

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

const root = useTemplateRef<HTMLElement>('root')
const { width: containerWidth } = useElementSize(root)
const {
  size: fileSize,
  min: fileMin,
  max: fileMaxLimit,
  collapsed: filesCollapsed,
  save: saveFilesSidebar,
} = useSidebarLayout('files')
const fileMax = computed(() =>
  containerWidth.value > 0
    ? Math.max(fileMin, Math.min(fileMaxLimit, Math.floor(containerWidth.value * 0.45)))
    : fileMaxLimit,
)
// In a narrow pane, the existing file picker takes the place of the sidebar.
const narrow = computed(() => containerWidth.value > 0 && containerWidth.value < NARROW_PX)
const filesHidden = computed({
  get: () => narrow.value || filesCollapsed.value,
  set: (value) => {
    if (!narrow.value) filesCollapsed.value = value
  },
})
const filesSidebarLabel = computed(() =>
  t(filesCollapsed.value ? 'changes.expandFiles' : 'changes.collapseFiles'),
)
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
const visible = () =>
  !!root.value?.getClientRects().length && (props.place === 'full' || state.full === null)
const branch = useBranchUpdates(() => (props.turn ? null : props.threadId), visible)
const { hasUpdate, loading, mergeable, problem, showLatest } = branch
const changes = computed<BranchChanges | null>(() => {
  const { turn } = props
  return turn
    ? {
        branch: thread.value?.workspace?.branch ?? '',
        base: thread.value?.workspace?.base ?? '',
        from: turn.from,
        head: turn.to,
        commits: [],
        files: turn.files,
        uncommitted: [],
      }
    : branch.changes.value
})
const from = computed(() => changes.value?.from ?? '')
const head = computed(() => changes.value?.head ?? '')
const loadVersions = computed(() => fileVersionsFor(props.threadId, from.value, head.value))

onBeforeUnmount(
  onLiveEvent((event) => {
    if (props.turn || document.hidden || !visible() || event.projectId !== thread.value?.projectId)
      return
    if (
      (event.type === 'turn.changed' || event.type === 'turn.finished') &&
      event.data.conversationId === props.threadId
    ) {
      void branch.check(true)
      return
    }
    if (
      event.type === 'branch.merged' ||
      event.type === 'branch.merge_undone' ||
      event.type === 'pull_request.closed'
    ) {
      if (event.data.threadId === props.threadId) {
        void branch.check(true)
      } else {
        void branch.check(false)
      }
      return
    }
    if (
      (event.type === 'branch.conflicted' ||
        event.type === 'pull_request.synced' ||
        event.type === 'workspace.opened' ||
        event.type === 'workspace.removed') &&
      event.data.threadId === props.threadId
    ) {
      void branch.check(true)
    }
  }),
)
watch(
  [
    () => thread.value?.workspace?.branch,
    () => thread.value?.workspace?.base,
    () => thread.value?.workspace?.removed,
    () => thread.value?.merged?.sha,
    () => state.detail?.project.settings.delivery.mode,
  ],
  () => {
    if (!document.hidden && visible()) void branch.check(true)
  },
)
watch(
  () => state.stream,
  (status) => {
    if (status === 'open') void branch.check()
  },
)
// Handed to the user for review, it can also end without a merge here: they let it go, or
// took it in some other way. Completion accepts delivery independently of merging.
const reviewing = computed(() => thread.value?.stage === 'review' && !props.turn)
async function skipMerge() {
  await completeThread(props.threadId)
  closeView(view.value)
}

const tree = computed(() => fileTree(changes.value?.files ?? []))
const files = computed(() => orderedFiles(tree.value))
const expanded = ref<string[]>([])
const selected = ref<string | null>(null)
const diffList = useTemplateRef<VirtualListExpose>('diffList')
const comparisonKey = computed(() =>
  JSON.stringify([props.threadId, changes.value?.from, changes.value?.head]),
)
const totals = computed(() => lineTotals(changes.value?.files ?? []))

const PREFETCH_AHEAD = 12
const PREFETCH_BEHIND = 2

function prefetchAround(index: number) {
  if (!from.value || !head.value || !files.value.length) return
  const start = Math.max(0, index - PREFETCH_BEHIND)
  const end = Math.min(files.value.length, index + PREFETCH_AHEAD)
  loadVersions.value.prefetch(files.value.slice(start, end))
}

function goToFile(path: string | null) {
  const index = files.value.findIndex((file) => file.path === path)
  if (index < 0) return
  selected.value = path
  prefetchAround(index)
  diffList.value?.scrollToIndex(index, 'start')
}

/** Below this width, a tab on the side, the file list folds into the file's name. */
const NARROW_PX = 720

watch(
  changes,
  (next, previous) => {
    if (next && (!previous || !sameComparison(previous, next))) {
      expanded.value = folderKeys(tree.value)
    }
  },
  { immediate: true },
)
watch(
  [diffList, () => props.threadId, () => props.turn?.turnId, () => props.path],
  ([_list, id, turnId, path], [, beforeId, beforeTurn, beforePath]) => {
    const start = id !== beforeId || turnId !== beforeTurn || path !== beforePath
    const preferred = start ? path : (selected.value ?? path)
    const target = files.value.find((file) => file.path === preferred) ?? files.value[0]
    goToFile(target?.path ?? null)
  },
  { flush: 'post' },
)

function select(node: TreeNodeData) {
  const { file: changed } = node as FileNode
  if (!changed) return
  filesOpen.value = false
  goToFile(changed.path)
}

watch(
  [files, loadVersions],
  ([list]) => {
    if (!list.length) return
    const index = selected.value ? list.findIndex((file) => file.path === selected.value) : 0
    prefetchAround(index >= 0 ? index : 0)
  },
  { immediate: true },
)

let selectionFrame: number | null = null
onBeforeUnmount(() => {
  if (selectionFrame !== null) cancelAnimationFrame(selectionFrame)
})

function updateSelection() {
  if (selectionFrame !== null) return
  selectionFrame = requestAnimationFrame(async () => {
    selectionFrame = null
    await nextTick()
    const viewport = diffList.value?.el()
    if (!viewport) return
    const top = viewport.getBoundingClientRect().top + 1
    const active = Array.from(viewport.querySelectorAll<HTMLElement>('[data-file-path]')).find(
      (item) => item.getBoundingClientRect().bottom > top,
    )
    if (active?.dataset.filePath) {
      selected.value = active.dataset.filePath
      const index = files.value.findIndex((file) => file.path === active.dataset.filePath)
      if (index >= 0) prefetchAround(index)
    }
  })
}

function estimateFileHeight(file: ChangedFile) {
  return Math.min(640, 96 + 20 * ((file.added ?? 0) + (file.removed ?? 0)))
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
watch(hasUpdate, (value) => {
  if (value) mergeOpen.value = false
})

async function merge() {
  const expectedHead = changes.value?.head
  if (!expectedHead || hasUpdate.value) return
  merging.value = true
  try {
    const { outcome } = await api.mergeThread(props.threadId, {
      method: method.value,
      message: message.value,
      expectedHead,
    })
    mergeOpen.value = false
    if (outcome.kind === 'conflict') {
      toast.warning(t('changes.conflict', { base: base.value, files: outcome.files.join('、') }), {
        duration: 10_000,
        closable: true,
      })
    } else if (outcome.kind === 'empty') {
      toast(t('changes.nothing', { base: base.value }))
    } else if (outcome.kind === 'moved') {
      await branch.check(true)
      if (!hasUpdate.value) toast.warning(t('changes.newChanges'))
      return
    } else {
      // Merged: nothing is left to show.
      closeView(view.value)
      return
    }
    await branch.check(true)
  } catch (error) {
    mergeOpen.value = false
    report(error)
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

// The reviewer's comments on the branch, with what the author did about each.
const reviews = ref<ReviewRoundView[]>([])
const notes = computed(() => (props.turn ? [] : reviewNotes(reviews.value)))
const sentComments = computed(() => (props.turn ? [] : sent.value))
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
const reviewCountByPath = computed(() => {
  const counts = new Map<string, number>()
  for (const item of notes.value) {
    counts.set(item.comment.path, (counts.get(item.comment.path) ?? 0) + 1)
  }
  return counts
})
const reviewCount = (path: string) => reviewCountByPath.value.get(path) ?? 0
// Share the toolbar's layout choice across files.
const diffMode = ref<'split' | 'inline' | 'auto'>('auto')
const drafts = computed(() => draftsOf(props.threadId))
const commentCountByPath = computed(() => {
  const counts = new Map<string, number>()
  for (const item of drafts.value) counts.set(item.path, (counts.get(item.path) ?? 0) + 1)
  for (const item of sent.value) counts.set(item.path, (counts.get(item.path) ?? 0) + 1)
  return counts
})
const commentCount = (path: string) => commentCountByPath.value.get(path) ?? 0
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
      <DouButton
        v-if="!narrow && files.length"
        variant="ghost"
        size="sm"
        :aria-label="filesSidebarLabel"
        :title="filesSidebarLabel"
        :aria-expanded="!filesCollapsed"
        @click="filesCollapsed = !filesCollapsed"
      >
        <PanelLeftIcon aria-hidden="true" />
      </DouButton>
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
      <div v-if="!turn" class="changes-update-slot">
        <DouPopover v-if="hasUpdate" :aria-label="t('changes.newChanges')" placement="bottom-end">
          <DouButton
            size="sm"
            variant="ghost"
            tone="warning"
            :aria-label="t('changes.newChanges')"
            :title="t('changes.newChanges')"
          >
            <AlertTriangleIcon />
          </DouButton>
          <template #content>
            <div class="changes-update-content">
              <DouText size="sm" weight="regular">{{ t('changes.newChanges') }}</DouText>
              <DouButton size="sm" variant="soft" @click="showLatest">
                {{ t('changes.refresh') }}
              </DouButton>
            </div>
          </template>
        </DouPopover>
      </div>
      <PullRequestTag v-if="thread && !mergeable && !turn" :thread="thread" />
      <DouTag v-if="mergeable && upToDate" tone="success" size="sm">
        <template #leading><GitMergeIcon /></template>
        {{ t('changes.merged', { base }) }}
      </DouTag>
      <DouPopover
        v-if="mergeable && !upToDate && changes?.files.length"
        v-model:open="mergeOpen"
        trigger="click"
        placement="bottom-end"
        :title="t('changes.merge', { base })"
        :width="340"
      >
        <DouButton size="sm" tone="primary" :disabled="hasUpdate">
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
      <ThreadTools v-if="reviewing" :label="t('thread.more')">
        <DouDropdownItem value="archive" @click="skipMerge">
          <template #prefix><ArchiveIcon aria-hidden="true" /></template>
          {{ t('changes.skipMerge') }}
        </DouDropdownItem>
      </ThreadTools>
      <ViewActions :view="view" :place="place" />
    </div>

    <DouAlert v-if="problem" tone="danger" class="changes-alert">{{ problem }}</DouAlert>
    <DouAlert v-if="changes?.uncommitted.length" tone="neutral" class="changes-alert">
      {{ t('changes.uncommitted', { count: changes.uncommitted.length }) }}
    </DouAlert>

    <div v-if="loading" class="changes-empty"><DouSpinner /></div>
    <div v-else-if="!changes || !changes.files.length" class="changes-empty">
      <DouEmpty
        :description="changes ? t('changes.empty', { base: changes.base }) : t('changes.noBranch')"
      />
    </div>
    <DouSplitter
      v-else
      v-model="fileSize"
      v-model:collapsed="filesHidden"
      unit="px"
      :min="fileMin"
      :max="fileMax"
      :disabled="narrow"
      :aria-label="t('changes.resizeFiles')"
      class="changes-body"
      collapsible
      @change="saveFilesSidebar"
    >
      <template #start>
        <ChangedFiles
          v-if="!narrow"
          v-model:expanded="expanded"
          :tree="tree"
          :selected="selected"
          :count="changes.files.length"
          :review-count="reviewCount"
          :comment-count="commentCount"
          class="changes-files"
          @select="select"
        />
      </template>
      <template #end>
        <div class="changes-diff">
          <div v-if="narrow" class="diff-picker">
            <DouPopover
              v-model:open="filesOpen"
              trigger="click"
              placement="bottom-start"
              :width="320"
            >
              <DouButton variant="ghost" size="sm" class="diff-file">
                {{ t('changes.files', { count: files.length }) }}
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
                  @select="select"
                />
              </template>
            </DouPopover>
          </div>
          <DouVirtualList
            :key="comparisonKey"
            ref="diffList"
            class="diff-list"
            :items="files"
            item-key="path"
            :estimate-size="estimateFileHeight"
            :overscan="1"
            :aria-label="t('changes.files', { count: files.length })"
            @scroll="updateSelection"
          >
            <template #default="{ item, index }">
              <ChangedFileDiff
                v-model:mode="diffMode"
                class="diff-item"
                :class="{ 'diff-first': index === 0, 'diff-last': index === files.length - 1 }"
                :data-file-path="item.path"
                :thread-id="threadId"
                :file="item"
                :load-versions="loadVersions"
                :sent="sentComments"
                :notes="notes"
              />
            </template>
          </DouVirtualList>
        </div>
      </template>
    </DouSplitter>
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
  margin: 8px 8px 0;
}

.changes-update-slot {
  display: flex;
  flex: none;
  inline-size: var(--dou-control-height-sm);
  block-size: var(--dou-control-height-sm);
}

.changes-update-content {
  display: flex;
  align-items: center;
  gap: 12px;
}

.changes-empty {
  flex: 1;
  display: grid;
  place-items: center;
}

.changes-body {
  --dou-splitter-line: var(--dou-color-border-subtle);
  flex: 1;
  min-height: 0;
}

.is-narrow .changes-body {
  --dou-splitter-line: transparent;
}

.changes-files {
  padding: 8px;
}

.changes-diff {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-width: 0;
  min-height: 0;
}

.diff-file {
  min-width: 0;
  max-width: 100%;
  margin-left: -8px;
}

.diff-file-tree {
  max-height: 60vh;
  overflow: auto;
}

.changes-scope {
  flex: none;
}

.diff-picker {
  padding: 4px 12px;
  border-bottom: 1px solid var(--dou-color-border-subtle);
}

.diff-list {
  flex: 1;
  min-height: 0;
  container-type: size;
  scroll-padding-top: 36px;
}

@media (pointer: coarse) {
  .diff-list {
    scroll-padding-top: 44px;
  }
}

.diff-item:not(.diff-first) {
  border-top: 1px solid var(--dou-color-border);
}

/* Let the last file jump to the top, even when its diff is short. */
.diff-last {
  min-block-size: 100cqh;
}

.merge-form {
  display: flex;
  flex-direction: column;
  gap: 8px;
  align-items: stretch;
}
</style>
