<script setup lang="ts">
import type { PlanView } from '@ariadne/core'
import {
  bezierEdge,
  defineEdgeType,
  defineNodeType,
  DouFlowControls,
  DouFlowEditor,
  flowKit,
  type FlowConnection,
  type FlowDocument,
  type FlowSelection,
  type FlowTransaction,
} from '@dou-ui/flow'
import '@dou-ui/flow/style.css'
import { computed, markRaw, ref, watch } from 'vue'
import { t, tm } from '../i18n'
import { editPlan } from '../store'
import { editOf, reasonOf } from './edit'
import { freshChanges, previewOps, toFlowDocument, touchedBy, type PlanMark } from './layout'
import PlanNode from './PlanNode.vue'
import { loadSeen, planUi, savedPositions, savePositions, seenRevision } from './ui'

const props = defineProps<{ plan: PlanView | null; compact?: boolean; editable?: boolean }>()
const selected = defineModel<string | null>('selected', { default: null })

const nodeTypes = [
  defineNodeType({
    type: 'plan',
    component: markRaw(PlanNode),
    ports: [
      { id: 'in', kind: 'input', side: 'top', max: Number.POSITIVE_INFINITY },
      { id: 'out', kind: 'output', side: 'bottom', max: Number.POSITIVE_INFINITY },
    ],
    label: (node) => String(node.data?.title ?? ''),
  }),
]

// Edges a previewed change adds or drops are drawn in the colors their nodes are marked in.
const markStroke: Record<Exclude<PlanMark, null>, string> = {
  added: 'var(--dou-color-success)',
  removed: 'var(--dou-color-danger)',
  changed: 'var(--dou-color-primary)',
}
const edgeTypes = [
  defineEdgeType({
    ...bezierEdge,
    style: (edge) => {
      const mark = edge.data?.mark as PlanMark | undefined
      if (!mark) return bezierEdge.style?.(edge)
      return { stroke: markStroke[mark], strokeDasharray: '6 4', strokeWidth: 2 }
    },
  }),
]
// No groups: a plan node belongs to the plan, not to another node.
const extensions = [flowKit({ group: false })]

const openProposals = computed(() => props.plan?.proposals.filter((p) => p.status === 'open') ?? [])
const preview = computed(() => {
  const proposal = openProposals.value.find((p) => p.id === planUi.previewId)
  return props.plan && proposal ? previewOps(props.plan, proposal.ops) : null
})
watch(
  () => props.plan,
  (plan) => plan && loadSeen(plan),
  { immediate: true },
)
const fresh = computed(() =>
  props.plan ? freshChanges(props.plan, seenRevision(props.plan)).nodes : new Set<string>(),
)
const proposed = computed(() => {
  const ids = new Set<string>()
  for (const proposal of openProposals.value) for (const id of touchedBy(proposal.ops)) ids.add(id)
  return ids
})

const source = computed<FlowDocument>(() => {
  void planUi.layoutVersion
  const positions = props.plan ? savedPositions(props.plan.projectId) : {}
  return toFlowDocument(props.plan, {
    positions,
    preview: preview.value,
    fresh: fresh.value,
    proposed: proposed.value,
  })
})
// The editor works on its own copy while the user edits; every change to the plan, or a
// change the server refused, puts the plan back as it is.
const doc = ref<FlowDocument>(source.value)
watch(source, (next) => {
  doc.value = next
})

const editing = computed(() => !!props.editable && !preview.value)

function canConnect({ source: from, target: to }: FlowConnection, current: FlowDocument) {
  if (from.node === to.node) return false
  return !current.edges.some((e) => e.source.node === from.node && e.target.node === to.node)
}

async function onTransaction(tr: FlowTransaction) {
  if (!editing.value || !props.plan) return
  const plan = props.plan
  const { ops, moved } = editOf(tr.steps, tr.before, t('plan.newNode'))
  if (Object.keys(moved).length) savePositions(plan.projectId, moved)
  if (!ops.length) return
  const titleOf = (id: string) => plan.nodes.find((node) => node.id === id)?.title ?? id
  const ok = await editPlan(tm(reasonOf(ops, titleOf)), ops)
  if (!ok) doc.value = source.value
}

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
    v-model="doc"
    v-model:selection="selection"
    :extensions="extensions"
    :node-types="nodeTypes"
    :edge-types="edgeTypes"
    :readonly="!editing"
    acyclic
    :can-connect="canConnect"
    fit-view
    :fit-min-zoom="0.6"
    direction="vertical"
    :searchable="!compact"
    edge-type="bezier"
    :aria-label="t('plan.title')"
    class="plan-graph"
    :class="{ 'plan-graph-editing': editing }"
    @transaction="onTransaction"
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
