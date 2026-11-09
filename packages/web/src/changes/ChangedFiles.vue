<script setup lang="ts">
import { fileIconOf, MessageIcon, ScanSearchIcon } from '@dou-ui/icons'
import { DouText, DouTree, type TreeNodeData } from 'dou-ui'
import { t } from '../i18n'
import type { FileNode } from './files'

/** The changed files as a tree, with each file's status and comments. */
defineProps<{
  tree: FileNode[]
  expanded: string[]
  selected: string | null
  count: number
  reviewCount: (path: string) => number
  commentCount: (path: string) => number
}>()
const emit = defineEmits<{ 'update:expanded': [keys: string[]]; select: [node: TreeNodeData] }>()

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
</script>

<template>
  <DouTree
    :expanded="expanded"
    @update:expanded="(keys) => emit('update:expanded', (keys ?? []) as string[])"
    :selected="selected"
    :data="tree"
    selectable
    expand-on-click
    size="sm"
    :aria-label="t('changes.files', { count })"
    @select="(node: TreeNodeData) => emit('select', node)"
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
            v-if="reviewCount((node as FileNode).file!.path)"
            size="sm"
            variant="tertiary"
            class="file-comments"
            :title="t('review.commented')"
          >
            <ScanSearchIcon />{{ reviewCount((node as FileNode).file!.path) }}
          </DouText>
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
</template>

<style scoped>
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
</style>
