<script setup lang="ts">
import type { PlanView } from '@ariadne/core'
import { defineNodeType, DouFlowControls, DouFlowEditor, type FlowSelection } from '@dou-ui/flow'
import '@dou-ui/flow/style.css'
import { computed, markRaw, ref, watch } from 'vue'
import { t } from '../i18n'
import { toFlowDocument } from './layout'
import PlanNode from './PlanNode.vue'

const props = defineProps<{ plan: PlanView | null; compact?: boolean }>()
const selected = defineModel<string | null>('selected', { default: null })

const nodeTypes = [
  defineNodeType({
    type: 'plan',
    component: markRaw(PlanNode),
    ports: [
      { id: 'in', kind: 'input', side: 'top', max: Number.POSITIVE_INFINITY },
      { id: 'out', kind: 'output', side: 'bottom' },
    ],
    label: (node) => String(node.data?.title ?? ''),
  }),
]

// Read-only: the document is derived from the plan every time it changes.
const doc = computed(() => toFlowDocument(props.plan))
const selection = ref<FlowSelection>({ nodes: [], edges: [] })
watch(selection, (next) => {
  selected.value = next.nodes[0] ?? null
})
watch(selected, (id) => {
  if ((selection.value.nodes[0] ?? null) !== id) {
    selection.value = { nodes: id ? [id] : [], edges: [] }
  }
})
</script>

<template>
  <DouFlowEditor
    v-model:selection="selection"
    :model-value="doc"
    :node-types="nodeTypes"
    readonly
    fit-view
    :fit-min-zoom="0.6"
    direction="vertical"
    :searchable="!compact"
    edge-type="bezier"
    :aria-label="t('plan.title')"
    class="plan-graph"
  >
    <DouFlowControls :history="false" :tidy="false" placement="bottom-left" />
  </DouFlowEditor>
</template>

<style scoped>
.plan-graph {
  width: 100%;
  height: 100%;
  min-height: 0;
}
</style>
