<script setup lang="ts">
import type { SentLineComment } from '@ariadne/core'
import type { ChangedFile, FileVersions } from '@ariadne/plugin-sdk'
import { DouCodeDiff } from '@dou-ui/code'
import '@dou-ui/code/style.css'
import { DouAlert, DouEmpty, DouSpinner, DouText } from 'dou-ui'
import { computed, shallowRef, watch } from 'vue'
import { ApiError } from '../api'
import { codeEditorWorker } from '../highlighting/code-worker-pool'
import { t, tm } from '../i18n'
import { useReviewComments, type ReviewNote } from '../review/review-comments'
import { useCodeLanguage } from './languages'
import { useLineComments } from './line-comments'

const props = defineProps<{
  threadId: string
  file: ChangedFile
  commentPath?: string
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

const effectiveCommentPath = computed(() => props.commentPath ?? props.file.path)
const line = useLineComments(
  () => props.threadId,
  () => effectiveCommentPath.value,
  () =>
    versions.value && !versions.value.skipped
      ? { original: versions.value.before ?? '', modified: versions.value.after ?? '' }
      : null,
  () => props.sent,
)
const review = useReviewComments(
  () => props.threadId,
  () => props.notes,
  () => effectiveCommentPath.value,
  () => versions.value?.after ?? null,
)
const gutters = computed(() =>
  [line.gutter.value, review.gutter.value].filter(
    (gutter) => gutter.hoverMarker || (gutter.markers?.length ?? 0) > 0,
  ),
)
const widgets = computed(() => [...line.widgets.value, ...review.widgets.value])
const oneSided = computed(() => !versions.value?.before || !versions.value?.after)
const { language } = useCodeLanguage(() => props.file.path)
</script>

<template>
  <section class="file-diff" :aria-label="file.path">
    <header v-if="problem || !versions || versions.skipped" class="file-diff-header">
      <DouText size="sm" weight="medium" truncate :title="file.path">{{ file.path }}</DouText>
      <DouText v-if="file.previousPath" variant="tertiary" size="sm" truncate>
        {{ t('changes.renamed', { path: file.previousPath }) }}
      </DouText>
    </header>
    <DouAlert v-if="problem" tone="danger" class="file-diff-problem">{{ problem }}</DouAlert>
    <div v-else-if="!versions" class="file-diff-loading"><DouSpinner size="sm" /></div>
    <DouEmpty
      v-else-if="versions.skipped"
      :description="t(versions.skipped === 'binary' ? 'changes.binary' : 'changes.large')"
    />
    <DouCodeDiff
      v-else
      class="diff"
      :original="versions.before ?? ''"
      :model-value="versions.after ?? ''"
      :language="language"
      :worker="codeEditorWorker()"
      :sync-highlight-lines="0"
      :gutters="gutters"
      :widgets="widgets"
      :mode="oneSided ? 'inline' : mode"
      :bordered="false"
      :scroll-y="false"
      sticky-toolbar
      wrap
      @update:mode="mode = $event"
    >
      <template #title>
        <span class="file-diff-title">
          <DouText size="sm" weight="medium" truncate :title="file.path">{{ file.path }}</DouText>
          <DouText v-if="file.previousPath" variant="tertiary" size="sm" truncate>
            {{ t('changes.renamed', { path: file.previousPath }) }}
          </DouText>
        </span>
      </template>
    </DouCodeDiff>
  </section>
</template>

<style scoped>
.file-diff-header {
  position: sticky;
  top: 0;
  z-index: 4;
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 36px;
  padding: 0 12px;
  background-color: var(--dou-color-bg-surface);
  border-bottom: 1px solid var(--dou-color-border);
}

@media (pointer: coarse) {
  .file-diff-header {
    min-height: 44px;
  }
}

.file-diff-title {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.file-diff-loading {
  min-height: 96px;
  display: grid;
  place-items: center;
}

.file-diff-problem {
  margin: 8px 12px;
}

.diff :deep(.line-comment-mark) {
  color: var(--dou-color-primary);
}

.diff :deep(.line-comment-sent-mark),
.diff :deep(.review-comment-mark) {
  color: var(--dou-color-text-tertiary);
}
</style>
