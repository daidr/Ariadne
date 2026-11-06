<script setup lang="ts">
import { DouSplitter } from 'dou-ui'
import { computed } from 'vue'
import { resize, type PaneNode } from './layout'
import { setSide, state } from '../store'
import PaneGroup from './PaneGroup.vue'
import { MIN_HEIGHT, MIN_WIDTH } from './sizes'

/** One node of the right side: a group of tabs, or two nodes split by a divider. */
const props = defineProps<{ node: PaneNode }>()
defineOptions({ name: 'SidePane' })

// Neither side of a divider gets too narrow to read, however deep the splits go.
const limit = computed(() =>
  props.node.type === 'split' && props.node.direction === 'vertical'
    ? `${MIN_HEIGHT}px`
    : `${MIN_WIDTH}px`,
)
</script>

<template>
  <PaneGroup v-if="node.type === 'group'" :group="node" />
  <DouSplitter
    v-else
    :model-value="node.size"
    :direction="node.direction"
    :min="limit"
    :min-end="limit"
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
