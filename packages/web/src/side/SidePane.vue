<script setup lang="ts">
import { DouSplitter } from 'dou-ui'
import { resize, type PaneNode } from './layout'
import { setSide, state } from '../store'
import PaneGroup from './PaneGroup.vue'

/** One node of the right side: a group of tabs, or two nodes split by a divider. */
defineProps<{ node: PaneNode }>()
defineOptions({ name: 'SidePane' })
</script>

<template>
  <PaneGroup v-if="node.type === 'group'" :group="node" />
  <DouSplitter
    v-else
    :model-value="node.size"
    :direction="node.direction"
    :min="20"
    :max="80"
    class="side-split"
    @update:model-value="(size) => setSide(resize(state.side, node.id, size))"
  >
    <template #start><SidePane :node="node.first" /></template>
    <template #end><SidePane :node="node.second" /></template>
  </DouSplitter>
</template>

<style scoped>
.side-split {
  height: 100%;
  min-height: 0;
  min-width: 0;
}
</style>
