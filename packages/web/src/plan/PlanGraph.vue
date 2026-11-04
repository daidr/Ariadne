<script setup lang="ts">
import type { PlanOp, PlanView } from '@ariadne/core'
import {
  bezierEdge,
  defineEdgeType,
  defineFlowExtension,
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
import { DouButton, DouDialog } from 'dou-ui'
import { computed, markRaw, ref, watch } from 'vue'
import { t, tm } from '../i18n'
import { editPlan } from '../store'
import { editOf, reasonOf } from './edit'
import { freshChanges, offsetsOf, toFlowDocument, touchedBy, type PlanMark } from './layout'
import PlanNode from './PlanNode.vue'
import { loadSeen, planUi, savedOffsets, saveOffsets, seenRevision } from './ui'

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
// Deleting nodes on the graph asks first, like the side panel does: a node can carry
// threads that would be detached from the plan.
const removing = ref<{ nodes: string[]; unlinks: PlanOp[] } | null>(null)
const askBeforeRemoving = defineFlowExtension({
  name: 'plan-remove-confirm',
  transaction(tr) {
    const nodes = tr.steps.flatMap((step) => (step.type === 'removeNode' ? [step.id] : []))
    if (!nodes.length || !editing.value) return
    // Edges selected on their own go too, once the user confirms.
    const unlinks = editOf(
      tr.steps.filter((step) => step.type === 'removeEdge'),
      tr.before,
      '',
    ).ops.filter((op) => op.op === 'unlink' && !nodes.includes(op.from) && !nodes.includes(op.to))
    tr.cancel(t('plan.removeAsk'))
    removing.value = { nodes, unlinks }
  },
})
// No groups: a plan node belongs to the plan, not to another node.
const extensions = [flowKit({ group: false }), askBeforeRemoving]

const openProposals = computed(() => props.plan?.proposals.filter((p) => p.status === 'open') ?? [])
const preview = computed(() => {
  const proposal = openProposals.value.find((p) => p.id === planUi.previewId)
  return proposal?.preview ?? null
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
  const offsets = props.plan ? savedOffsets(props.plan.projectId) : {}
  return toFlowDocument(props.plan, {
    offsets,
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
  const keep = Object.keys(moved).length > 0
  if (!ops.length) {
    // Only moved: the layout stays as it is, so offsets are taken from it.
    if (keep) saveOffsets(plan.projectId, offsetsOf(plan, moved))
    return
  }
  const titleOf = (id: string) => plan.nodes.find((node) => node.id === id)?.title ?? id
  if ((await editPlan(reasonOf(ops, titleOf, t), ops)) !== 'ok') {
    doc.value = source.value
    return
  }
  // The change can lay the plan out anew, and a new node only has a place in the new
  // layout: offsets are taken from that, and the graph is drawn again with them.
  const after = props.plan
  if (keep && after?.projectId === plan.projectId) {
    saveOffsets(plan.projectId, offsetsOf(after, moved))
    planUi.layoutVersion += 1
  }
}

const removeQuestion = computed(() => {
  const ids = removing.value?.nodes ?? []
  const nodes = (props.plan?.nodes ?? []).filter((node) => ids.includes(node.id))
  const threads = nodes.reduce((sum, node) => sum + node.threads.length, 0)
  if (nodes.length === 1 && nodes[0]) {
    const title = nodes[0].title
    return threads
      ? t('plan.removeConfirmThreads', { title, count: threads })
      : t('plan.removeConfirm', { title })
  }
  return threads
    ? t('plan.removeSelectedThreads', { count: nodes.length, threads })
    : t('plan.removeSelected', { count: nodes.length })
})

async function confirmRemove() {
  const plan = props.plan
  const pending = removing.value
  removing.value = null
  if (!plan || !pending) return
  const ops: PlanOp[] = [
    ...pending.unlinks,
    ...pending.nodes.map((id) => ({ op: 'remove' as const, id })),
  ]
  const titleOf = (id: string) => plan.nodes.find((node) => node.id === id)?.title ?? id
  if ((await editPlan(reasonOf(ops, titleOf, t), ops)) === 'ok') selected.value = null
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
  <DouDialog
    :open="!!removing"
    :title="t('plan.remove')"
    :width="420"
    @update:open="(open) => !open && (removing = null)"
  >
    {{ removeQuestion }}
    <template #footer>
      <DouButton variant="ghost" @click="removing = null">{{ t('settings.cancel') }}</DouButton>
      <DouButton tone="danger" @click="confirmRemove">{{ t('plan.remove') }}</DouButton>
    </template>
  </DouDialog>
</template>

<style scoped>
.plan-graph {
  width: 100%;
  height: 100%;
  min-height: 0;
}
</style>
