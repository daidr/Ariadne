<script setup lang="ts">
import type { SentLineComment } from '@ariadne/core'
import type { ChangedFile, FileVersions } from '@ariadne/plugin-sdk'
import {
  codeLocales,
  DouCodeDiff,
  splitLines,
  type CodeDiffGutter,
  type CodeDiffHunk,
  type CodeDiffWidget,
} from '@dou-ui/code'
import '@dou-ui/code/style.css'
import {
  ArrowDownIcon,
  ArrowUpIcon,
  DiffSplitIcon,
  DiffUnifiedIcon,
  FoldVerticalIcon,
  UnfoldVerticalIcon,
} from '@dou-ui/icons'
import { DouAlert, DouButton, DouEmpty, DouSpinner, DouText, DouTooltip } from 'dou-ui'
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  shallowRef,
  useTemplateRef,
  watch,
  watchPostEffect,
} from 'vue'
import { ApiError } from '../api'
import { locale, t, tm } from '../i18n'
import { useReviewComments, type ReviewNote } from '../review/review-comments'
import { languageOf } from './languages'
import { useLineComments } from './line-comments'

const SPLIT_WIDTH = 560

const props = defineProps<{
  threadId: string
  file: ChangedFile
  loadVersions: ((
    path: string,
    previousPath: string | null,
    signal: AbortSignal,
  ) => Promise<FileVersions>) & {
    peek?: (path: string, previousPath: string | null) => FileVersions | null
  }
  sent: readonly SentLineComment[]
  notes: ReviewNote[]
}>()
const mode = defineModel<'split' | 'inline' | 'auto'>('mode', { required: true })

const rootRef = useTemplateRef<HTMLElement>('root')
const diffRef = useTemplateRef<InstanceType<typeof DouCodeDiff>>('diff')

const versions = shallowRef<FileVersions | null>(null)
const problem = shallowRef<string | null>(null)
watch(
  [() => props.loadVersions, () => props.file.path, () => props.file.previousPath],
  async ([load, path, previousPath], _before, cleanup) => {
    const cached = load.peek?.(path, previousPath ?? null) ?? null
    if (cached) {
      versions.value = cached
      problem.value = null
      return
    }
    versions.value = null
    problem.value = null
    const controller = new AbortController()
    const { signal } = controller
    cleanup(() => controller.abort())
    try {
      const result = await load(path, previousPath ?? null, signal)
      if (!signal.aborted) versions.value = result
    } catch (error) {
      if (!signal.aborted)
        problem.value = error instanceof ApiError ? tm(error.localized) : String(error)
    }
  },
  { immediate: true },
)

const line = useLineComments(
  () => props.threadId,
  () => props.file.path,
  () =>
    versions.value && !versions.value.skipped
      ? { original: versions.value.before ?? '', modified: versions.value.after ?? '' }
      : null,
  () => props.sent,
)
const review = useReviewComments(
  () => props.threadId,
  () => props.notes,
  () => props.file.path,
  () => versions.value?.after ?? null,
)
const gutters = computed(() => [line.gutter.value, review.gutter.value])
const widgets = computed(() => [...line.widgets.value, ...review.widgets.value])
const showDiff = computed(() => !problem.value && !!versions.value && !versions.value.skipped)
const oneSided = computed(() => !versions.value?.before || !versions.value?.after)

const roomy = ref(true)
let resizeObserver: ResizeObserver | null = null
onMounted(() => {
  if (typeof ResizeObserver === 'undefined' || !rootRef.value) return
  const width = rootRef.value.getBoundingClientRect().width
  if (width > 0) roomy.value = width >= SPLIT_WIDTH
  resizeObserver = new ResizeObserver(([entry]) => {
    if (entry && entry.contentRect.width > 0) {
      roomy.value = entry.contentRect.width >= SPLIT_WIDTH
    }
  })
  resizeObserver.observe(rootRef.value)
})
onBeforeUnmount(() => resizeObserver?.disconnect())

const oneSidedOverride = ref<'split' | 'inline' | null>(null)
watch([() => props.file.path, oneSided], () => {
  oneSidedOverride.value = null
})

const effectiveMode = computed<'split' | 'inline'>(() => {
  if (oneSided.value) return oneSidedOverride.value ?? 'inline'
  if (mode.value !== 'auto') return mode.value
  return roomy.value ? 'split' : 'inline'
})

function toggleView() {
  const next = effectiveMode.value === 'split' ? 'inline' : 'split'
  if (oneSided.value) {
    oneSidedOverride.value = next
  } else {
    mode.value = next
  }
}

const codeMessages = computed(() => codeLocales[locale.value] ?? codeLocales['zh-CN']!)

const hunks = computed(() => (showDiff.value ? (diffRef.value?.hunks ?? null) : null))
const hasHunks = computed(() => (hunks.value?.length ?? 0) > 0)

const stats = computed<{ added: number; removed: number } | null>(() => {
  if (props.file.added !== null || props.file.removed !== null) {
    return { added: props.file.added ?? 0, removed: props.file.removed ?? 0 }
  }
  if (hunks.value) {
    let added = 0
    let removed = 0
    for (const hunk of hunks.value) {
      added += hunk.modifiedCount
      removed += hunk.originalCount
    }
    return { added, removed }
  }
  return null
})

const showNoChanges = computed(
  () => showDiff.value && hunks.value !== null && hunks.value.length === 0,
)

const statsSummary = computed(() => {
  if (!stats.value) return ''
  return codeMessages.value.diffStats
    .replace('{added}', String(stats.value.added))
    .replace('{removed}', String(stats.value.removed))
})

function canHideRun(count: number, before: boolean, after: boolean): boolean {
  if (count <= 0) return false
  const from = before ? 3 : 0
  const to = count - (after ? 3 : 0)
  return to - from >= 4
}

function hasCollapsibleUnchanged(
  original: string,
  hunkList: readonly CodeDiffHunk[],
  gutterList: readonly CodeDiffGutter[],
  widgetList: readonly CodeDiffWidget[],
): boolean {
  if (!hunkList.length || !original) return false
  const total = splitLines(original).length
  const kept = { a: new Set<number>(), b: new Set<number>() }
  const keep = (side: 'original' | 'modified', lineNumber: number) =>
    kept[side === 'original' ? 'a' : 'b'].add(lineNumber)
  for (const gutter of gutterList) {
    for (const marker of gutter.markers ?? []) {
      if (marker.visible !== 'hover') keep(marker.side, marker.line)
    }
  }
  for (const widget of widgetList) keep(widget.side, widget.line)

  const cut = (a: number, b: number, count: number, before: boolean, after: boolean) => {
    let from = 0
    for (let i = 0; i < count; i++) {
      if (!kept.a.has(a + i) && !kept.b.has(b + i)) continue
      if (i > from && canHideRun(i - from, from > 0 || before, true)) return true
      from = Math.max(from, i)
    }
    return canHideRun(count - from, from > 0 || before, after)
  }

  let a = 1
  let b = 1
  for (let i = 0; i < hunkList.length; i++) {
    const hunk = hunkList[i]!
    if (cut(a, b, hunk.originalStart - a, i > 0, true)) return true
    a = hunk.originalStart + hunk.originalCount
    b = hunk.modifiedStart + hunk.modifiedCount
  }
  return cut(a, b, total - a + 1, hunkList.length > 0, false)
}

const hasCollapsible = computed(() =>
  showDiff.value && versions.value?.before
    ? hasCollapsibleUnchanged(
        versions.value.before,
        hunks.value ?? [],
        gutters.value,
        widgets.value,
      )
    : false,
)
const hasHiddenLines = ref(true)

watch(
  [() => versions.value?.before, () => versions.value?.after, hasCollapsible],
  ([, , collapsible]) => {
    hasHiddenLines.value = collapsible
  },
  { immediate: true },
)

function syncHiddenBars() {
  if (!hasCollapsible.value) {
    hasHiddenLines.value = false
    return
  }
  const diffEl = rootRef.value?.querySelector('.dou-code-diff')
  if (diffEl) hasHiddenLines.value = !!diffEl.querySelector('.dou-code-diff__bar')
}

let barObserver: MutationObserver | null = null
let barSyncQueued = false
function stopBarObserver() {
  barObserver?.disconnect()
  barObserver = null
  barSyncQueued = false
}

function scheduleBarSync() {
  if (barSyncQueued) return
  barSyncQueued = true
  queueMicrotask(() => {
    barSyncQueued = false
    syncHiddenBars()
  })
}

watchPostEffect(() => {
  stopBarObserver()
  const visible = showDiff.value
  void versions.value
  void diffRef.value
  const rootEl = rootRef.value
  if (!visible || !rootEl || typeof MutationObserver === 'undefined') return
  const diffEl = rootEl.querySelector('.dou-code-diff')
  if (!diffEl) return
  barObserver = new MutationObserver(scheduleBarSync)
  barObserver.observe(diffEl, { childList: true, subtree: true })
})
onBeforeUnmount(stopBarObserver)

function previousChange() {
  diffRef.value?.previous()
  void nextTick(syncHiddenBars)
}

function nextChange() {
  diffRef.value?.next()
  void nextTick(syncHiddenBars)
}

function toggleCollapse() {
  if (hasHiddenLines.value) {
    diffRef.value?.expandAll()
    hasHiddenLines.value = false
  } else {
    diffRef.value?.collapseAll()
    hasHiddenLines.value = true
  }
}
</script>

<template>
  <section ref="root" class="file-diff" :aria-label="file.path">
    <header class="file-diff-header">
      <div class="file-diff-title">
        <DouText size="sm" weight="medium" truncate :title="file.path" class="file-diff-path">
          {{ file.path }}
        </DouText>
        <DouText
          v-if="file.previousPath"
          variant="tertiary"
          size="sm"
          truncate
          :title="t('changes.renamed', { path: file.previousPath })"
          class="file-diff-renamed"
        >
          {{ t('changes.renamed', { path: file.previousPath }) }}
        </DouText>
      </div>

      <div v-if="stats || showDiff" class="file-diff-actions">
        <span v-if="showNoChanges" class="file-diff-stats">
          <span class="file-diff-summary">{{ codeMessages.diffNoChanges }}</span>
        </span>
        <span v-else-if="stats" class="file-diff-stats">
          <span class="file-diff-count is-added" aria-hidden="true">+{{ stats.added }}</span>
          <span class="file-diff-count is-removed" aria-hidden="true">−{{ stats.removed }}</span>
          <span class="file-diff-sr-only">{{ statsSummary }}</span>
        </span>

        <template v-if="showDiff">
          <DouTooltip :content="codeMessages.diffPrevious" shortcut="shift+f7">
            <DouButton
              variant="ghost"
              size="sm"
              :disabled="!hasHunks"
              :aria-label="codeMessages.diffPrevious"
              @click="previousChange"
            >
              <ArrowUpIcon aria-hidden="true" />
            </DouButton>
          </DouTooltip>
          <DouTooltip :content="codeMessages.diffNext" shortcut="f7">
            <DouButton
              variant="ghost"
              size="sm"
              :disabled="!hasHunks"
              :aria-label="codeMessages.diffNext"
              @click="nextChange"
            >
              <ArrowDownIcon aria-hidden="true" />
            </DouButton>
          </DouTooltip>
          <DouTooltip
            v-if="hasCollapsible"
            :content="hasHiddenLines ? codeMessages.diffExpandAll : codeMessages.diffCollapse"
          >
            <DouButton
              variant="ghost"
              size="sm"
              :aria-label="hasHiddenLines ? codeMessages.diffExpandAll : codeMessages.diffCollapse"
              @click="toggleCollapse"
            >
              <UnfoldVerticalIcon v-if="hasHiddenLines" aria-hidden="true" />
              <FoldVerticalIcon v-else aria-hidden="true" />
            </DouButton>
          </DouTooltip>
          <DouTooltip
            :content="effectiveMode === 'split' ? codeMessages.diffInline : codeMessages.diffSplit"
          >
            <DouButton
              variant="ghost"
              size="sm"
              :aria-label="
                effectiveMode === 'split' ? codeMessages.diffInline : codeMessages.diffSplit
              "
              @click="toggleView"
            >
              <DiffUnifiedIcon v-if="effectiveMode === 'split'" aria-hidden="true" />
              <DiffSplitIcon v-else aria-hidden="true" />
            </DouButton>
          </DouTooltip>
        </template>
      </div>
    </header>
    <DouAlert v-if="problem" tone="danger" class="file-diff-problem">{{ problem }}</DouAlert>
    <div v-else-if="!versions" class="file-diff-loading"><DouSpinner size="sm" /></div>
    <DouEmpty
      v-else-if="versions.skipped"
      :description="t(versions.skipped === 'binary' ? 'changes.binary' : 'changes.large')"
    />
    <DouCodeDiff
      v-else
      ref="diff"
      class="diff"
      :original="versions.before ?? ''"
      :model-value="versions.after ?? ''"
      :language="languageOf(file.path)"
      :gutters="gutters"
      :widgets="widgets"
      :mode="effectiveMode"
      :toolbar="false"
      :bordered="false"
      wrap
    />
  </section>
</template>

<style scoped>
.file-diff {
  position: relative;
}

.file-diff-header {
  position: sticky;
  top: 0;
  z-index: 5;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-height: 36px;
  padding: 0 6px 0 12px;
  background-color: var(--dou-color-bg-canvas);
  border-bottom: 1px solid var(--dou-color-border);
}

.file-diff-title {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: 1 1 0;
  min-width: 0;
  overflow: hidden;
}

.file-diff-path {
  flex: 0 1 auto;
  min-width: 0;
}

.file-diff-renamed {
  flex: 0 2 auto;
  min-width: 0;
}

.file-diff-actions {
  display: flex;
  align-items: center;
  gap: 2px;
  flex: none;
}

.file-diff-stats {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin-right: 4px;
  font-family: var(--dou-font-family-mono);
  font-size: var(--dou-font-size-sm);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  flex: none;
}

.file-diff-count.is-added {
  color: var(--dou-color-success-text);
}

.file-diff-count.is-removed {
  color: var(--dou-color-danger-text);
}

.file-diff-summary {
  color: var(--dou-color-text-secondary);
  font-family: var(--dou-font-family);
}

.file-diff-sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
}

@media (pointer: coarse) {
  .file-diff-header {
    min-height: 44px;
  }

  .file-diff-actions :deep(.dou-button) {
    position: relative;
    min-width: 36px;
    min-height: 36px;
  }

  .file-diff-actions :deep(.dou-button)::after {
    position: absolute;
    content: '';
    inset: -2px;
  }
}

.file-diff-loading {
  min-height: 96px;
  display: grid;
  place-items: center;
}

.file-diff-problem {
  margin: 8px 12px;
}

/* Let the outer virtual list own vertical scrolling while keeping horizontal scroll for long lines. */
.diff :deep(.dou-code-editor__scroller) {
  overflow-x: auto;
  overflow-y: hidden;
}

.diff :deep(.line-comment-mark) {
  color: var(--dou-color-primary);
}

.diff :deep(.line-comment-sent-mark),
.diff :deep(.review-comment-mark) {
  color: var(--dou-color-text-tertiary);
}
</style>
