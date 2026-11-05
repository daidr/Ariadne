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
  ChevronLeftIcon,
  fileIconOf,
  GitMergeIcon,
  MessageIcon,
  RotateCwIcon,
  SendIcon,
} from '@dou-ui/icons'
import { computed, ref, watch } from 'vue'
import { api, ApiError } from '../api'
import { t, tm } from '../i18n'
import {
  openMain,
  openThread,
  report,
  sendLineComments,
  state,
  type TurnChangesRef,
} from '../store'
import PullRequestTag from '../components/PullRequestTag.vue'
import { fileTree, folderKeys, lineTotals, orderedFiles, type FileNode } from './files'
import { languageOf } from './languages'
import { draftsOf, sendable, useLineComments } from './line-comments'

/**
 * What a thread's branch changed against its base, with merging it; or, with `turn`, what
 * one of its turns changed in the workspace. Lines of either can be commented on, and the
 * comments go to the thread together.
 */
const props = defineProps<{ threadId: string; turn?: TurnChangesRef; path?: string }>()

const thread = computed(
  () => state.detail?.threads.find((item) => item.id === props.threadId) ?? null,
)
const changes = ref<BranchChanges | null>(null)
const mergeable = ref(false)
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
  else if (!has(selected.value)) selected.value = orderedFiles(tree.value)[0]?.path ?? null
  opened.value = true
}
const opened = ref(false)

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

// Again whenever the thread moves on: it may have committed since.
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

const STATUS_TONE = {
  added: 'success',
  deleted: 'danger',
  renamed: 'primary',
  copied: 'primary',
  modified: 'warning',
  type: 'warning',
} as const
const STATUS_LETTER = {
  added: 'A',
  deleted: 'D',
  renamed: 'R',
  copied: 'C',
  modified: 'M',
  type: 'T',
}

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
const method = ref<MergeMethod>('squash')
const message = ref('')
const merging = ref(false)
const result = ref<{ tone: 'warning' | 'neutral'; text: string } | null>(null)
watch(mergeOpen, (open) => {
  if (open && !message.value) message.value = thread.value?.title ?? ''
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

// Comments on lines of the new version, sent to the thread together.
const { extension: comments } = useLineComments(
  () => props.threadId,
  () => file.value?.path ?? null,
  () => versions.value?.after ?? null,
)
const extensions = [comments]
// Side by side, where lines can be commented on; the toolbar switches to one column.
const diffMode = ref<'split' | 'inline'>('split')
const drafts = computed(() => draftsOf(props.threadId))
const commentCount = (path: string) => drafts.value.filter((c) => c.path === path).length
const sending = ref(false)
async function sendComments() {
  sending.value = true
  try {
    const outgoing = drafts.value
    const ids = outgoing.map((draft) => draft.id)
    if (await sendLineComments(props.threadId, sendable(outgoing), ids)) openThread(props.threadId)
  } finally {
    sending.value = false
  }
}

const methodOptions = computed(() => [
  { value: 'squash', label: t('changes.method.squash') },
  { value: 'merge', label: t('changes.method.merge') },
])
</script>

<template>
  <div class="changes">
    <div class="changes-header">
      <DouButton variant="ghost" size="sm" @click="openMain({ kind: 'conversation' })">
        <template #leading><ChevronLeftIcon /></template>
        {{ t('changes.back') }}
      </DouButton>
      <div class="changes-title">
        <DouText weight="medium" truncate>{{ thread?.title }}</DouText>
        <DouText v-if="turn" variant="tertiary" size="sm" class="changes-meta">
          {{ t('changes.turn') }} ·
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
      <DouButton
        v-if="drafts.length"
        size="sm"
        variant="outline"
        :loading="sending"
        @click="sendComments"
      >
        <template #leading><SendIcon /></template>
        {{ t('changes.sendComments', { count: drafts.length }) }}
      </DouButton>
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
      <DouPopover
        v-else-if="mergeable && changes?.files.length"
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
      <div class="changes-files">
        <DouTree
          v-model:expanded="expanded"
          :selected="selected"
          :data="tree"
          selectable
          expand-on-click
          size="sm"
          :aria-label="t('changes.files', { count: changes.files.length })"
          @select="select"
        >
          <template #node="{ node }">
            <span class="file-row" :title="(node as FileNode).file?.path ?? node.label">
              <component
                :is="fileIconOf((node as FileNode).label.split('.').at(-1) ?? '')"
                v-if="(node as FileNode).file"
                class="file-icon"
              />
              <span class="file-name">{{ node.label }}</span>
              <template v-if="(node as FileNode).file">
                <DouText
                  v-if="commentCount((node as FileNode).file!.path)"
                  size="sm"
                  variant="tertiary"
                  class="file-comments"
                  :title="t('changes.commented')"
                >
                  <MessageIcon />{{ commentCount((node as FileNode).file!.path) }}
                </DouText>
                <DouText
                  size="sm"
                  :tone="STATUS_TONE[(node as FileNode).file!.status]"
                  class="file-status"
                >
                  {{ STATUS_LETTER[(node as FileNode).file!.status] }}
                </DouText>
              </template>
            </span>
          </template>
        </DouTree>
      </div>
      <div class="changes-diff">
        <div v-if="file" class="diff-head">
          <DouText size="sm" weight="medium" truncate>{{ file.path }}</DouText>
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
          :extensions="extensions"
          v-model:mode="diffMode"
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
  padding: 8px 16px;
  border-bottom: 1px solid var(--dou-color-border);
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
  overflow: auto;
  padding: 8px;
  border-right: 1px solid var(--dou-color-border-subtle);
}

.file-row {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  width: 100%;
}

.file-icon {
  flex: none;
  color: var(--dou-color-text-tertiary);
}

.file-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.file-status {
  flex: none;
  font-family: var(--dou-font-family-mono, monospace);
}

.changes-diff {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
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

/* Beside a line: a plus while the pointer is on it, a mark where comments are. */
.diff :deep(.line-comment-add) {
  color: var(--dou-color-text-tertiary);
  opacity: 0;
}

.diff :deep(.line-comment-add:hover) {
  opacity: 1;
}

.diff :deep(.line-comment-mark) {
  color: var(--dou-color-primary);
}

.file-comments {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 2px;
}

.file-comments svg {
  width: 12px;
  height: 12px;
}

.merge-form {
  display: flex;
  flex-direction: column;
  gap: 8px;
  align-items: stretch;
}
</style>
