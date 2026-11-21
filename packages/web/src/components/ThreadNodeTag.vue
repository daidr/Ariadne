<script setup lang="ts">
import type { ThreadView } from '@ariadne/core'
import { DouDropdown, DouDropdownItem, DouDropdownRadioGroup, DouTag } from 'dou-ui'
import { UnlinkIcon, WorkflowIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'
import { setThreadNode, state } from '../store'

/**
 * The plan node a thread is linked to. Clicking opens the node list to move or unlink it
 * directly, without adding a chip to threads that are not linked.
 */
const props = defineProps<{ thread: ThreadView }>()

const nodes = computed(() => state.plan?.nodes ?? [])
const linked = computed(() =>
  props.thread.nodeId
    ? (nodes.value.find((node) => node.id === props.thread.nodeId) ?? null)
    : null,
)

function pick(value: string | number | undefined) {
  if (typeof value !== 'string' || !value || value === props.thread.nodeId) return
  void setThreadNode(props.thread.id, value)
}
</script>

<template>
  <DouDropdown v-if="linked" placement="bottom-start">
    <DouTag
      class="node-tag"
      size="sm"
      role="button"
      tabindex="0"
      :title="`${t('thread.planNode')}: ${linked.title}`"
    >
      <template #leading><WorkflowIcon /></template>
      <span class="node-tag-title">{{ linked.title }}</span>
    </DouTag>
    <template #menu>
      <DouDropdownRadioGroup :model-value="linked.id" @update:model-value="pick">
        <DouDropdownItem v-for="node in nodes" :key="node.id" :value="node.id">
          {{ node.title }}
        </DouDropdownItem>
      </DouDropdownRadioGroup>
      <DouDropdownItem value="unlink" divided @click="setThreadNode(thread.id, null)">
        <template #prefix><UnlinkIcon aria-hidden="true" /></template>
        {{ t('thread.planNodeUnlink') }}
      </DouDropdownItem>
    </template>
  </DouDropdown>
</template>

<style scoped>
.node-tag {
  min-width: 0;
  max-width: 100%;
  cursor: pointer;
}

.node-tag-title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
