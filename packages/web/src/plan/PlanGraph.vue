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
import {
  computed,
  markRaw,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  useTemplateRef,
  watch,
} from 'vue'
import { t, tm } from '../i18n'
import { editPlan } from '../store'
import { editOf, liveThreads, reasonOf } from './edit'
import { offsetsOf, toFlowDocument, touchedBy, type PlanMark } from './layout'
import PlanNode from './PlanNode.vue'
import { loadSeen, markNodeSeen, planUi, savedOffsets, saveOffsets, unseenNodes } from './ui'

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
      // Work went ahead of this dependency: the edge it skipped shows it.
      if (!mark && edge.data?.skipped) {
        return { stroke: 'var(--dou-color-warning)', strokeDasharray: '4 4', strokeWidth: 1.5 }
      }
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
const fresh = computed(() => (props.plan ? unseenNodes(props.plan) : new Set<string>()))
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
    compact: props.compact,
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
  const threads = nodes.reduce((sum, node) => sum + liveThreads(node), 0)
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

// Opened, the graph shows all of itself while it stays readable; past that it shows what
// wants the user: nodes waiting on them, changed since they looked, or in a change asked.
const editor = useTemplateRef<{
  $el: HTMLElement
  setViewport: (viewport: { x: number; y: number; zoom: number }, animate?: boolean) => void
  getNodeRect: (id: string) => { x: number; y: number; width: number; height: number } | null
}>('editor')
const viewport = ref({ x: 0, y: 0, zoom: 1 })
const READABLE = props.compact ? 0.45 : 0.6
const PAD = 24
function focusIds(): string[] {
  const plan = props.plan
  if (!plan) return []
  if (preview.value)
    return [...touchedBy(openProposals.value.find((p) => p.id === planUi.previewId)?.ops ?? [])]
  const waiting = plan.nodes.filter((node) => node.status === 'waiting').map((node) => node.id)
  return [...new Set([...waiting, ...proposed.value, ...fresh.value])]
}
function boxOf(ids: string[]) {
  const rects = ids.flatMap((id) => editor.value?.getNodeRect(id) ?? [])
  if (!rects.length) return null
  const left = Math.min(...rects.map((r) => r.x))
  const top = Math.min(...rects.map((r) => r.y))
  const right = Math.max(...rects.map((r) => r.x + r.width))
  const bottom = Math.max(...rects.map((r) => r.y + r.height))
  return { x: left, y: top, width: right - left, height: bottom - top }
}
// The view is worked out here rather than by the editor's fit, so a graph too large to read
// whole is shown at a readable size, from its top left or around what wants the user.
function frame() {
  const flow = editor.value
  const el = flow?.$el
  if (!flow || !el?.clientWidth) return
  const width = el.clientWidth - PAD * 2
  const height = el.clientHeight - PAD * 2
  const zoomFor = (box: { width: number; height: number }) =>
    Math.min(1, width / box.width, height / box.height)
  let box = boxOf(doc.value.nodes.map((node) => node.id))
  if (!box) return
  let zoom = zoomFor(box)
  let centred = true
  if (zoom < READABLE) {
    const focus = boxOf(focusIds())
    if (focus) box = focus
    else centred = false
    zoom = READABLE
  }
  const x =
    centred || box.width * zoom <= width
      ? PAD + (width - box.width * zoom) / 2 - box.x * zoom
      : PAD - box.x * zoom
  const y =
    box.height * zoom <= height
      ? PAD + (height - box.height * zoom) / 2 - box.y * zoom
      : PAD - box.y * zoom
  flow.setViewport({ x, y, zoom }, false)
}
// Nodes are measured a frame or two after they mount; until then there is nothing to fit.
let frameRequest = 0
function frameWhenMeasured(tries = 30) {
  cancelAnimationFrame(frameRequest)
  frameRequest = requestAnimationFrame(() => {
    const first = doc.value.nodes[0]?.id
    if (first && !editor.value?.getNodeRect(first) && tries > 0) frameWhenMeasured(tries - 1)
    else frame()
  })
}
// The canvas shrinks when a node's detail opens beside or under it: the node picked is
// moved back into sight, clear of the zoom controls at the bottom.
const CONTROLS = 56
function reveal(id: string) {
  const flow = editor.value
  const el = flow?.$el
  const rect = flow?.getNodeRect(id)
  if (!flow || !el?.clientWidth || !rect) return
  const { x, y, zoom } = viewport.value
  const shift = (start: number, size: number, room: number, end: number) => {
    if (size > room - PAD - end) return PAD - start
    if (start < PAD) return PAD - start
    if (start + size > room - end) return room - end - start - size
    return 0
  }
  const dx = shift(rect.x * zoom + x, rect.width * zoom, el.clientWidth, PAD)
  const dy = shift(rect.y * zoom + y, rect.height * zoom, el.clientHeight, CONTROLS)
  if (dx || dy) flow.setViewport({ x: x + dx, y: y + dy, zoom }, true)
}
let resized: ResizeObserver | undefined
onMounted(() => {
  frameWhenMeasured()
  const el = editor.value?.$el
  if (!el) return
  // A node the user had panned away from stays where they left it.
  let width = el.clientWidth
  let height = el.clientHeight
  resized = new ResizeObserver(() => {
    const id = selected.value
    const rect = id ? editor.value?.getNodeRect(id) : undefined
    const { x, y, zoom } = viewport.value
    const seen =
      rect &&
      rect.x * zoom + x < width &&
      (rect.x + rect.width) * zoom + x > 0 &&
      rect.y * zoom + y < height &&
      (rect.y + rect.height) * zoom + y > 0
    width = el.clientWidth
    height = el.clientHeight
    if (id && seen) reveal(id)
  })
  resized.observe(el)
})
onBeforeUnmount(() => {
  cancelAnimationFrame(frameRequest)
  resized?.disconnect()
})
// A change shown on the graph is framed, so the nodes it adds are not off screen.
watch(preview, async () => {
  await nextTick()
  frameWhenMeasured()
})

const selection = ref<FlowSelection>({ nodes: [], edges: [] })
watch(selection, (next) => {
  selected.value = next.nodes[0] ?? null
})
watch(selected, (id) => {
  if (id && props.plan) markNodeSeen(props.plan, id)
  if ((selection.value.nodes[0] ?? null) !== id) {
    selection.value = { nodes: id ? [id] : [], edges: [] }
  }
})
</script>

<template>
  <DouFlowEditor
    ref="editor"
    v-model="doc"
    v-model:selection="selection"
    v-model:viewport="viewport"
    :extensions="extensions"
    :node-types="nodeTypes"
    :edge-types="edgeTypes"
    :readonly="!editing"
    acyclic
    :can-connect="canConnect"
    direction="vertical"
    :searchable="!compact"
    :bordered="!compact"
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
