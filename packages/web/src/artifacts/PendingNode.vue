<script setup lang="ts">
import { DouFlowPort, type NodeViewProps } from '@dou-ui/flow'
import type { PendingNodeData } from './pending-node'

/**
 * A placeholder: a dashed outline around what will be here. Its ports are those of the
 * shape that takes its place, so connectors drawn to it stay; they only show while a
 * connector is being drawn to it.
 */
defineProps<NodeViewProps<PendingNodeData>>()
</script>

<template>
  <div class="pending-node">
    {{ data.text }}
    <div class="pending-node-ports">
      <DouFlowPort v-for="port in ports" :id="port.id" :key="port.id" />
    </div>
  </div>
</template>

<style>
/* Not scoped: the export reads the class to draw the node the same way. */
.pending-node {
  position: relative;
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

.pending-node-ports {
  position: absolute;
  inset: 0;
  pointer-events: none;
}

/*
 * The ports show only while a connector is being drawn, as on the whiteboard's own shapes.
 * A node type of one's own has no public way to ask for that (daidr/dou-ui#413), so these
 * rules follow the editor's inner state classes; replace them when it has one.
 */
.pending-node-ports .dou-flow-port {
  opacity: 0;
}

.dou-flow-editor.is-connecting .pending-node-ports .dou-flow-port.is-input.is-candidate {
  opacity: 1;
}
</style>
