<script setup lang="ts">
import type { ThreadView } from '@ariadne/core'
import { DouButton, DouDropdown, DouDropdownItem, toast, useClipboard } from 'dou-ui'
import {
  ArchiveIcon,
  CopyIcon,
  GitBranchIcon,
  ListTreeIcon,
  MessagesIcon,
  MoreHorizontalIcon,
} from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'
import { completeThread, openMain, openThread, state } from '../store'

/**
 * The "…" menu of a thread. In a side tab group it is the group's own menu, whose items
 * come in through the slot, so a thread has one menu, not two.
 */
const props = defineProps<{ thread?: ThreadView | null; label: string }>()

const clipboard = useClipboard()

const workspace = computed(() => {
  const ws = props.thread?.workspace
  if (!ws?.branch) return null
  const useSwitch = ws.removed || !ws.label
  const text = useSwitch ? `git switch ${ws.branch}` : ws.label
  return {
    text,
    label: t(useSwitch ? 'thread.copyBranchSwitch' : 'thread.copyWorkspacePath'),
    description: useSwitch ? text : ws.branch,
    copied: t(useSwitch ? 'thread.copiedBranchSwitch' : 'thread.copiedWorkspacePath'),
  }
})

const reviewed = computed(() => {
  const id = props.thread?.reviewOf
  return id ? (state.detail?.threads.find((item) => item.id === id) ?? null) : null
})

const firstThreadItem = computed<'workspace' | 'reviewed' | 'archive' | 'trace' | null>(() => {
  if (!props.thread) return null
  if (workspace.value) return 'workspace'
  if (reviewed.value) return 'reviewed'
  if (props.thread.stage === 'review') return 'archive'
  return 'trace'
})

async function copyWorkspace() {
  const target = workspace.value
  if (!target) return
  try {
    await clipboard.copy(target.text)
    toast.success(target.copied)
  } catch {
    toast.danger(t('thread.copyFailed'))
  }
}
</script>

<template>
  <DouDropdown placement="bottom-end">
    <DouButton variant="ghost" size="sm" :aria-label="label" :title="label">
      <MoreHorizontalIcon />
    </DouButton>
    <template #menu>
      <slot />
      <DouDropdownItem
        v-if="workspace"
        value="workspace"
        :divided="firstThreadItem === 'workspace'"
        :title="workspace.text"
        @click="copyWorkspace"
      >
        <template #prefix><GitBranchIcon aria-hidden="true" /></template>
        {{ workspace.label }}
        <template #description>
          <span class="thread-tools-desc thread-tools-mono">{{ workspace.description }}</span>
        </template>
        <template #suffix><CopyIcon aria-hidden="true" /></template>
      </DouDropdownItem>
      <DouDropdownItem
        v-if="reviewed"
        value="reviewed"
        :divided="firstThreadItem === 'reviewed'"
        :title="reviewed.title"
        @click="openThread(reviewed.id)"
      >
        <template #prefix><MessagesIcon aria-hidden="true" /></template>
        {{ t('review.author') }}
        <template #description>
          <span class="thread-tools-desc">{{ reviewed.title }}</span>
        </template>
      </DouDropdownItem>
      <DouDropdownItem
        v-if="thread?.stage === 'review'"
        value="archive"
        :divided="firstThreadItem === 'archive'"
        @click="completeThread(thread.id)"
      >
        <template #prefix><ArchiveIcon aria-hidden="true" /></template>
        {{ t('changes.skipMerge') }}
      </DouDropdownItem>
      <DouDropdownItem
        v-if="thread"
        value="trace"
        :divided="firstThreadItem === 'trace'"
        @click="openMain({ kind: 'trace', conversationId: thread.id })"
      >
        <template #prefix><ListTreeIcon /></template>
        {{ t('trace.open') }}
      </DouDropdownItem>
    </template>
  </DouDropdown>
</template>

<style scoped>
.thread-tools-desc {
  display: block;
  max-width: 240px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.thread-tools-mono {
  font-family: var(--dou-font-family-mono);
}
</style>
