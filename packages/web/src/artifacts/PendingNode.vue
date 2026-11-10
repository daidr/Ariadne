<script setup lang="ts">
import type { NodeViewProps } from '@dou-ui/flow'
import type { PendingNodeData } from './pending-node'

/**
 * A placeholder: a dashed outline around what will be here. Its ports are those of the
 * shape that takes its place (`ports: 'sides'`), so connectors drawn to it stay, and they
 * show when the whiteboard's own shapes' do.
 */
defineProps<NodeViewProps<PendingNodeData>>()
</script>

<template>
  <div class="pending-node">
    {{ data.text }}
  </div>
</template>

<style>
/* Not scoped: the export reads the class to draw the node the same way. */
.pending-node {
  display: flex;
  align-items: center;
  justify-content: center;
  box-sizing: border-box;
  height: 100%;
  padding: 8px 12px;
  border: 1px dashed currentColor;
  border-radius: 8px;
  color: var(--dou-color-text-tertiary);
  font-size: 13px;
  line-height: 1.5;
  text-align: center;
  white-space: pre-line;
}
</style>
