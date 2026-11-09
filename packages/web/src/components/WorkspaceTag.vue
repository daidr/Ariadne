<script setup lang="ts">
import type { ThreadView } from '@ariadne/core'
import { DouCopyButton, DouPopover, DouTag, DouText } from 'dou-ui'
import { GitBranchIcon } from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { t } from '../i18n'

/**
 * The branch a thread works on. Opened, it says where to try its changes before merging:
 * the worktree's path while it is there, the command to switch to the branch once it was
 * removed. Either is copied with one press.
 */
const props = defineProps<{ workspace: NonNullable<ThreadView['workspace']> }>()

const open = ref(false)

// The worktree's path while it is there; once removed, the branch is all there is.
const text = computed(() =>
  props.workspace.removed ? `git switch ${props.workspace.branch}` : props.workspace.label,
)
</script>

<template>
  <DouPopover v-model:open="open" trigger="click" placement="bottom-start">
    <DouTag
      class="workspace-tag"
      size="sm"
      role="button"
      tabindex="0"
      :aria-expanded="open"
      :title="t('thread.workspace')"
      @keydown.enter.prevent="open = !open"
    >
      <template #leading><GitBranchIcon /></template>
      {{ workspace.branch }}
    </DouTag>
    <template #content>
      <div class="workspace-row">
        <DouText size="sm" mono class="workspace-text">{{ text }}</DouText>
        <DouCopyButton :text="text" size="sm" />
      </div>
    </template>
  </DouPopover>
</template>

<style scoped>
.workspace-tag {
  min-width: 0;
  max-width: 100%;
  cursor: pointer;
}

.workspace-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.workspace-text {
  flex: 1;
  min-width: 0;
  overflow-wrap: anywhere;
}
</style>
