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
  FolderGitIcon,
  GitBranchIcon,
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
import {
  fileTree,
  folderKeys,
  lineTotals,
  orderedFiles,
  repoFileTree,
  scopedFileKey,
  type FileNode,
} from './files'
import { draftsChanged, flushComments } from './comment-sender'
import { draftsOf } from './line-comments'
import type { ReviewRoundView, SentLineComment } from '@ariadne/core'
import ReviewMark from '../review/ReviewMark.vue'
import { reviewNotes } from '../review/review-comments'
import { useBranchUpdates } from './branch-updates'
import { fileVersionsFor, type FileVersionLoader } from './file-versions'

interface ChangeRepoGroup {
  key: string
  sourceId: string
  sourceName: string
  repoPath: string
  embedded: boolean
  mergeable: boolean
  branch: string
  base: string
  from: string
  head: string
  commits: BranchChanges['commits']
  files: ChangedFile[]
  uncommitted: string[]
  totals: { added: number; removed: number }
}

type DiffListItem =
  | {
      kind: 'repo-header'
      key: string
      group: ChangeRepoGroup
      label: string
    }
  | {
      kind: 'file'
      key: string
      group: ChangeRepoGroup
      file: ChangedFile
      commentPath: string
    }

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

const repoGroups = computed<ChangeRepoGroup[]>(() => {
  const { turn } = props
  if (turn) {
    if (turn.repos && turn.repos.length > 0) {
      return turn.repos.map((repo) => {
        const sourceId = repo.sourceId ?? ''
        const sourceName = repo.sourceName ?? ''
        const repoPath = repo.repoPath ?? ''
        const embedded = repoPath.length > 0
        return {
          key: `${sourceId}\0${repoPath}`,
          sourceId,
          sourceName,
          repoPath,
          embedded,
          mergeable: false,
          branch: repo.branch ?? (embedded ? '' : (thread.value?.workspace?.branch ?? '')),
          base: embedded ? '' : (thread.value?.workspace?.base ?? ''),
          from: repo.from,
          head: repo.to,
          commits: [],
          files: repo.files,
          uncommitted: [],
          totals: { added: repo.total.added, removed: repo.total.removed },
        }
      })
    }
    return [
      {
        key: '',
        sourceId: '',
        sourceName: '',
        repoPath: '',
        embedded: false,
        mergeable: false,
        branch: thread.value?.workspace?.branch ?? '',
        base: thread.value?.workspace?.base ?? '',
        from: turn.from,
        head: turn.to,
        commits: [],
        files: turn.files,
        uncommitted: [],
        totals: { added: turn.total.added, removed: turn.total.removed },
      },
    ]
  }
  if (branch.repos.value.length > 0) {
    return branch.repos.value.map((repo) => {
      const sourceId = repo.sourceId ?? ''
      const sourceName = repo.sourceName ?? ''
      const repoPath = repo.repoPath ?? ''
      return {
        key: `${sourceId}\0${repoPath}`,
        sourceId,
        sourceName,
        repoPath,
        embedded: !!repo.embedded,
        mergeable: !!repo.mergeable,
        branch: repo.changes.branch,
        base: repo.changes.base,
        from: repo.changes.from,
        head: repo.changes.head,
        commits: repo.changes.commits,
        files: repo.changes.files,
        uncommitted: repo.changes.uncommitted,
        totals: lineTotals(repo.changes.files),
      }
    })
  }
  if (branch.changes.value) {
    const current = branch.changes.value
    return [
      {
        key: '',
        sourceId: '',
        sourceName: '',
        repoPath: '',
        embedded: false,
        mergeable: mergeable.value,
        branch: current.branch,
        base: current.base,
        from: current.from,
        head: current.head,
        commits: current.commits,
        files: current.files,
        uncommitted: current.uncommitted,
        totals: lineTotals(current.files),
      },
    ]
  }
  return []
})

const isMultiRepo = computed(() => repoGroups.value.length > 1)
const singleRepo = computed(() => (repoGroups.value.length === 1 ? repoGroups.value[0]! : null))

function repoDisplayLabel(group: ChangeRepoGroup): string {
  if (group.sourceName && group.repoPath) return `${group.sourceName}/${group.repoPath}`
  if (group.repoPath) return group.repoPath
  if (group.sourceName) return group.sourceName
  return t('changes.primaryRepo')
}

function repoContextPrefix(group: ChangeRepoGroup): string {
  if (group.sourceName && group.repoPath) return `${group.sourceName}/${group.repoPath}`
  if (group.repoPath) return group.repoPath
  if (group.sourceName) return group.sourceName
  return ''
}

function commentPathFor(group: ChangeRepoGroup, path: string): string {
  const prefix = group.repoPath
    ? group.sourceName
      ? `${group.sourceName}/${group.repoPath}`
      : group.repoPath
    : group.sourceName && isMultiRepo.value
      ? group.sourceName
      : ''
  return prefix ? `${prefix}/${path}` : path
}

const singleRepoContext = computed(() =>
  singleRepo.value ? repoContextPrefix(singleRepo.value) : '',
)

const totalFilesCount = computed(() =>
  repoGroups.value.reduce((sum, group) => sum + group.files.length, 0),
)
const totalCommitsCount = computed(() =>
  repoGroups.value.reduce((sum, group) => sum + group.commits.length, 0),
)
const totalUncommittedCount = computed(() =>
  repoGroups.value.reduce((sum, group) => sum + group.uncommitted.length, 0),
)
const totals = computed(() =>
  repoGroups.value.reduce(
    (acc, group) => ({
      added: acc.added + group.totals.added,
      removed: acc.removed + group.totals.removed,
    }),
    { added: 0, removed: 0 },
  ),
)

const comparisonKey = computed(() =>
  JSON.stringify([
    props.threadId,
    repoGroups.value.map((group) => [group.sourceId, group.repoPath, group.from, group.head]),
  ]),
)

let loaderCacheKey = ''
const loaderCache = new Map<string, FileVersionLoader>()
function loaderFor(group: ChangeRepoGroup): FileVersionLoader {
  if (loaderCacheKey !== comparisonKey.value) {
    loaderCache.clear()
    loaderCacheKey = comparisonKey.value
  }
  const cacheKey = JSON.stringify([group.sourceId, group.repoPath, group.from, group.head])
  let loader = loaderCache.get(cacheKey)
  if (!loader) {
    loader = fileVersionsFor(props.threadId, group.from, group.head, {
      sourceId: group.sourceId || undefined,
      repoPath: group.repoPath || undefined,
    })
    loaderCache.set(cacheKey, loader)
  }
  return loader
}

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

const tree = computed<FileNode[]>(() => {
  if (!isMultiRepo.value) {
    return fileTree(singleRepo.value?.files ?? [])
  }
  return repoFileTree(
    repoGroups.value
      .filter((group) => group.files.length > 0)
      .map((group) => ({
        key: group.key,
        label: repoDisplayLabel(group),
        branch: group.branch,
        embedded: group.embedded,
        files: group.files,
        commentPrefix: group.repoPath
          ? group.sourceName
            ? `${group.sourceName}/${group.repoPath}`
            : group.repoPath
          : group.sourceName || '',
      })),
  )
})

const diffItems = computed<DiffListItem[]>(() => {
  if (!isMultiRepo.value) {
    const group = singleRepo.value
    if (!group) return []
    return orderedFiles(tree.value).map((file) => ({
      kind: 'file' as const,
      key: file.path,
      group,
      file,
      commentPath: commentPathFor(group, file.path),
    }))
  }
  const items: DiffListItem[] = []
  for (const group of repoGroups.value) {
    if (group.files.length === 0 && group.uncommitted.length === 0) continue
    items.push({
      kind: 'repo-header',
      key: `repo:${group.key}`,
      group,
      label: repoDisplayLabel(group),
    })
    for (const file of orderedFiles(fileTree(group.files))) {
      items.push({
        kind: 'file',
        key: scopedFileKey(group.key, file.path),
        group,
        file,
        commentPath: commentPathFor(group, file.path),
      })
    }
  }
  return items
})

const fileItems = computed(() =>
  diffItems.value.filter(
    (item): item is Extract<DiffListItem, { kind: 'file' }> => item.kind === 'file',
  ),
)

const expanded = ref<string[]>([])
const selected = ref<string | null>(null)
const diffList = useTemplateRef<VirtualListExpose>('diffList')

const PREFETCH_AHEAD = 12
const PREFETCH_BEHIND = 2

function prefetchAround(listIndex: number) {
  if (!diffItems.value.length) return
  const start = Math.max(0, listIndex - PREFETCH_BEHIND)
  const end = Math.min(diffItems.value.length, listIndex + PREFETCH_AHEAD)
  const byRepo = new Map<ChangeRepoGroup, ChangedFile[]>()
  for (const item of diffItems.value.slice(start, end)) {
    if (item.kind !== 'file' || !item.group.from || !item.group.head) continue
    const list = byRepo.get(item.group) ?? []
    list.push(item.file)
    byRepo.set(item.group, list)
  }
  for (const [group, files] of byRepo) {
    loaderFor(group).prefetch(files)
  }
}

function goToKey(key: string | null) {
  if (!key) return
  const index = diffItems.value.findIndex((item) => item.key === key)
  if (index < 0) return
  const item = diffItems.value[index]!
  if (item.kind === 'file') {
    selected.value = item.key
  }
  prefetchAround(index)
  diffList.value?.scrollToIndex(index, 'start')
}

function findInitialFileKey(preferred?: string | null): string | null {
  if (preferred) {
    const exact = fileItems.value.find(
      (item) =>
        item.key === preferred || item.commentPath === preferred || item.file.path === preferred,
    )
    if (exact) return exact.key
  }
  return fileItems.value[0]?.key ?? null
}

/** Below this width, a tab on the side, the file list folds into the file's name. */
const NARROW_PX = 720

watch(
  comparisonKey,
  (next, previous) => {
    if (next && next !== previous) {
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
    goToKey(findInitialFileKey(preferred))
  },
  { flush: 'post' },
)

function select(node: TreeNodeData) {
  const fileNode = node as FileNode
  if (fileNode.repo) {
    filesOpen.value = false
    goToKey(fileNode.key)
    return
  }
  if (!fileNode.file) return
  filesOpen.value = false
  goToKey(fileNode.key)
}

watch(
  [diffItems, comparisonKey],
  ([list]) => {
    if (!list.length) return
    const index = selected.value ? list.findIndex((item) => item.key === selected.value) : 0
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
    const active = Array.from(viewport.querySelectorAll<HTMLElement>('[data-file-key]')).find(
      (item) => item.getBoundingClientRect().bottom > top,
    )
    if (active?.dataset.fileKey) {
      selected.value = active.dataset.fileKey
      const index = diffItems.value.findIndex((item) => item.key === active.dataset.fileKey)
      if (index >= 0) prefetchAround(index)
    }
  })
}

function estimateItemHeight(item: DiffListItem) {
  if (item.kind === 'repo-header') {
    return item.group.uncommitted.length > 0 ? 76 : 44
  }
  return Math.min(640, 96 + 20 * ((item.file.added ?? 0) + (item.file.removed ?? 0)))
}

const primaryMergeableRepo = computed(
  () => repoGroups.value.find((group) => group.mergeable && !group.embedded) ?? null,
)
const mergeExpectedHead = computed(
  () => primaryMergeableRepo.value?.head ?? changes.value?.head ?? '',
)
// Merged as it stands: nothing came after the last merge.
const upToDate = computed(
  () => !!thread.value?.merged && thread.value.merged.head === mergeExpectedHead.value,
)
const base = computed(
  () =>
    primaryMergeableRepo.value?.base ?? changes.value?.base ?? thread.value?.workspace?.base ?? '',
)
const canMerge = computed(
  () =>
    !props.turn &&
    mergeable.value &&
    !upToDate.value &&
    (primaryMergeableRepo.value
      ? primaryMergeableRepo.value.files.length > 0
      : (changes.value?.files.length ?? 0) > 0),
)
const hasOtherChangedRepos = computed(
  () => isMultiRepo.value && repoGroups.value.some((group) => !group.mergeable),
)

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
  const expectedHead = mergeExpectedHead.value
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
        v-if="!narrow && fileItems.length"
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
        <div class="changes-title-row">
          <DouText weight="medium" truncate>{{ thread?.title }}</DouText>
          <DouTag
            v-if="!isMultiRepo && singleRepo?.embedded"
            size="sm"
            variant="soft"
            class="changes-repo-tag"
          >
            {{ t('changes.embeddedRepo') }}
          </DouTag>
        </div>
        <DouText v-if="turn" variant="tertiary" size="sm" class="changes-meta">
          <template v-if="isMultiRepo">
            {{ t('changes.repos', { count: repoGroups.length }) }} ·
          </template>
          <template v-else-if="singleRepoContext">
            {{ singleRepoContext
            }}<template v-if="singleRepo?.branch"> ({{ singleRepo.branch }})</template> ·
          </template>
          {{ t('changes.files', { count: turn.total.files }) }}
          <span class="changes-added">+{{ turn.total.added }}</span>
          <span class="changes-removed">−{{ turn.total.removed }}</span>
        </DouText>
        <DouText v-else-if="isMultiRepo" variant="tertiary" size="sm" class="changes-meta">
          {{ t('changes.repos', { count: repoGroups.length }) }} ·
          {{ t('changes.commits', { count: totalCommitsCount }) }} ·
          {{ t('changes.files', { count: totalFilesCount }) }}
          <span class="changes-added">+{{ totals.added }}</span>
          <span class="changes-removed">−{{ totals.removed }}</span>
        </DouText>
        <DouText v-else-if="singleRepo" variant="tertiary" size="sm" class="changes-meta">
          <template v-if="singleRepoContext">{{ singleRepoContext }} · </template>
          <template v-if="singleRepo.base && singleRepo.base !== singleRepo.branch">
            {{ singleRepo.branch }} → {{ singleRepo.base }}
          </template>
          <template v-else>{{ singleRepo.branch }}</template>
          · {{ t('changes.commits', { count: singleRepo.commits.length }) }} ·
          {{ t('changes.files', { count: singleRepo.files.length }) }}
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
      <DouTag v-if="mergeable && upToDate && !turn" tone="success" size="sm">
        <template #leading><GitMergeIcon /></template>
        {{ t('changes.merged', { base }) }}
      </DouTag>
      <DouPopover
        v-if="canMerge"
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
            <DouAlert v-if="hasOtherChangedRepos" tone="neutral" size="sm">
              {{ t('changes.mergePrimaryOnly', { base }) }}
            </DouAlert>
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
    <DouAlert
      v-if="
        (!isMultiRepo && singleRepo?.uncommitted.length) ||
        (isMultiRepo && totalFilesCount === 0 && totalUncommittedCount > 0)
      "
      tone="neutral"
      class="changes-alert"
    >
      {{
        t('changes.uncommitted', {
          count: isMultiRepo ? totalUncommittedCount : singleRepo!.uncommitted.length,
        })
      }}
    </DouAlert>

    <div v-if="loading" class="changes-empty"><DouSpinner /></div>
    <div v-else-if="repoGroups.length === 0 || totalFilesCount === 0" class="changes-empty">
      <DouEmpty
        :description="
          singleRepo?.base ? t('changes.empty', { base: singleRepo.base }) : t('changes.noBranch')
        "
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
          :count="totalFilesCount"
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
                {{ t('changes.files', { count: totalFilesCount }) }}
                <template #trailing><ChevronDownIcon /></template>
              </DouButton>
              <template #content>
                <ChangedFiles
                  v-model:expanded="expanded"
                  :tree="tree"
                  :selected="selected"
                  :count="totalFilesCount"
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
            :items="diffItems"
            item-key="key"
            :estimate-size="estimateItemHeight"
            :overscan="1"
            :aria-label="t('changes.files', { count: totalFilesCount })"
            @scroll="updateSelection"
          >
            <template #default="{ item, index }">
              <section
                v-if="item.kind === 'repo-header'"
                class="repo-section-header"
                :class="{ 'is-first': index === 0 }"
                :data-repo-key="item.group.key"
              >
                <div class="repo-section-main">
                  <FolderGitIcon class="repo-section-icon" aria-hidden="true" />
                  <DouText size="sm" weight="medium" truncate :title="item.label">
                    {{ item.label }}
                  </DouText>
                  <DouTag v-if="item.group.embedded" size="sm" variant="soft">
                    {{ t('changes.embeddedRepo') }}
                  </DouTag>
                  <span v-if="item.group.branch" class="repo-section-branch">
                    <GitBranchIcon aria-hidden="true" />
                    <template v-if="item.group.base && item.group.base !== item.group.branch">
                      {{ item.group.branch }} → {{ item.group.base }}
                    </template>
                    <template v-else>{{ item.group.branch }}</template>
                  </span>
                </div>
                <DouText variant="tertiary" size="sm" class="repo-section-meta">
                  <template v-if="!turn && item.group.commits.length > 0">
                    {{ t('changes.commits', { count: item.group.commits.length }) }} ·
                  </template>
                  {{ t('changes.files', { count: item.group.files.length }) }}
                  <span class="changes-added">+{{ item.group.totals.added }}</span>
                  <span class="changes-removed">−{{ item.group.totals.removed }}</span>
                </DouText>
                <DouAlert
                  v-if="item.group.uncommitted.length > 0"
                  tone="neutral"
                  size="sm"
                  class="repo-section-uncommitted"
                >
                  {{ t('changes.uncommitted', { count: item.group.uncommitted.length }) }}
                </DouAlert>
              </section>
              <ChangedFileDiff
                v-else
                v-model:mode="diffMode"
                class="diff-item"
                :class="{
                  'diff-first': index === 0 || diffItems[index - 1]?.kind === 'repo-header',
                  'diff-last': index === diffItems.length - 1,
                }"
                :data-file-path="item.file.path"
                :data-file-key="item.key"
                :thread-id="threadId"
                :file="item.file"
                :comment-path="item.commentPath"
                :load-versions="loaderFor(item.group)"
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

.changes-title-row {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.changes-repo-tag {
  flex: none;
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

.repo-section-header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 6px 12px;
  padding: 10px 12px;
  background-color: var(--dou-color-bg-subtle);
  border-bottom: 1px solid var(--dou-color-border);
}

.repo-section-header:not(.is-first) {
  border-top: 1px solid var(--dou-color-border);
  margin-top: 8px;
}

.repo-section-main {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  flex-wrap: wrap;
}

.repo-section-icon {
  flex: none;
  color: var(--dou-color-text-secondary);
}

.repo-section-branch {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: var(--dou-color-text-tertiary);
  font-family: var(--dou-font-family-mono, monospace);
}

.repo-section-branch svg {
  width: 12px;
  height: 12px;
  flex: none;
}

.repo-section-meta {
  flex: none;
  white-space: nowrap;
}

.repo-section-uncommitted {
  flex-basis: 100%;
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
