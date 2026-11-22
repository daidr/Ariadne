<script setup lang="ts">
import type { ThreadView } from '@ariadne/core'
import { DouButton, DouDropdown, DouDropdownItem } from 'dou-ui'
import { ArchiveIcon, ListTreeIcon, MoreHorizontalIcon } from '@dou-ui/icons'
import { Comment, Fragment, Text, useSlots, type VNode } from 'vue'
import { t } from '../i18n'
import { completeThread, openMain } from '../store'

/**
 * The "…" menu of a thread. In a side tab group it is the group's own menu, whose items
 * come in through the slot, so a thread has one menu, not two.
 */
defineProps<{ thread?: ThreadView | null; label: string }>()

const slots = useSlots()

/** True when a slot renders at least one real node rather than only comments or whitespace. */
function hasRenderedNodes(nodes: VNode[] | undefined): boolean {
  if (!nodes) return false
  for (const node of nodes) {
    if (node.type === Comment) continue
    if (node.type === Text) {
      if (typeof node.children === 'string' && node.children.trim().length > 0) return true
      continue
    }
    if (node.type === Fragment) {
      if (Array.isArray(node.children) && hasRenderedNodes(node.children as VNode[])) return true
      continue
    }
    return true
  }
  return false
}

// Re-check during render rather than caching in a computed so non-reactive slot prop updates
// from a parent (such as DouDock's #extra slot) are always reflected.
const hasLeadingItems = () => hasRenderedNodes(slots.default?.())
</script>

<template>
  <DouDropdown placement="bottom-end">
    <DouButton variant="ghost" size="sm" :aria-label="label" :title="label">
      <MoreHorizontalIcon />
    </DouButton>
    <template #menu>
      <slot />
      <DouDropdownItem
        v-if="thread?.stage === 'review'"
        value="archive"
        :divided="hasLeadingItems()"
        @click="completeThread(thread.id)"
      >
        <template #prefix><ArchiveIcon aria-hidden="true" /></template>
        {{ t('changes.skipMerge') }}
      </DouDropdownItem>
      <DouDropdownItem
        v-if="thread"
        value="trace"
        :divided="thread.stage !== 'review' && hasLeadingItems()"
        @click="openMain({ kind: 'trace', conversationId: thread.id })"
      >
        <template #prefix><ListTreeIcon /></template>
        {{ t('trace.open') }}
      </DouDropdownItem>
    </template>
  </DouDropdown>
</template>
