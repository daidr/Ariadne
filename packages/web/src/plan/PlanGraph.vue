<script setup lang="ts">
import type { PlanView } from '@ariadne/core'
import {
  bezierEdge,
  defineEdgeType,
  defineNodeType,
  DouFlowControls,
  DouFlowEditor,
  flowKit,
  groupNode,
  type FlowDocument,
  type FlowSelection,
} from '@dou-ui/flow'
import '@dou-ui/flow/style.css'
import {
  computed,
  markRaw,
  nextTick,
  onBeforeUnmount,
  onMounted,
  provide,
  ref,
  useTemplateRef,
  watch,
} from 'vue'
import { t } from '../i18n'
import { isGroupId, toFlowDocument, touchedBy, type PlanMark } from './layout'
import PlanGroupNode from './PlanGroupNode.vue'
import PlanNode from './PlanNode.vue'
import {
  collapsedGroupsOf,
  loadSeen,
  markNodeSeen,
  PLAN_GROUP_TOGGLE,
  planUi,
  showingCompleted,
  toggleGroupCollapsed,
  unseenNodes,
} from './ui'

const props = defineProps<{ plan: PlanView | null; compact?: boolean }>()
const selected = defineModel<string | null>('selected', { default: null })

provide(PLAN_GROUP_TOGGLE, (groupId: string) => {
  if (!props.plan) return
  toggleGroupCollapsed(props.plan.projectId, groupId)
})

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
  defineNodeType({
    ...groupNode,
    component: markRaw(PlanGroupNode),
    resizable: false,
    label: (node) => String(node.data?.label ?? ''),
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
const extensions = [flowKit({ group: true, menu: false, toolbar: false })]

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
const showCompleted = computed(() => (props.plan ? showingCompleted(props.plan.projectId) : false))
const collapsedGroups = computed(() =>
  props.plan ? collapsedGroupsOf(props.plan.projectId) : new Set<string>(),
)

const source = computed<FlowDocument>(() =>
  toFlowDocument(props.plan, {
    preview: preview.value,
    fresh: fresh.value,
    proposed: proposed.value,
    compact: props.compact,
    showCompleted: showCompleted.value,
    collapsedGroups: collapsedGroups.value,
  }),
)
const doc = ref<FlowDocument>(source.value)
watch(source, (next) => {
  doc.value = next
  if (selected.value && !next.nodes.some((node) => node.id === selected.value)) {
    selected.value = null
  }
})

// Opened, the graph shows all of itself while it stays readable; past that it shows what
// wants the user: nodes waiting on them, changed since they looked, or in a change asked.
const editor = useTemplateRef<{
  $el: HTMLElement
  setViewport: (viewport: { x: number; y: number; zoom: number }, animate?: boolean) => void
  getNodeRect: (id: string) => { x: number; y: number; width: number; height: number } | null
}>('editor')
const viewport = ref({ x: 0, y: 0, zoom: 1 })
// In the side pane the whole graph is shown down to FIT; one too large even for that goes
// back to a size its words can be read at.
const READABLE = props.compact ? 0.75 : 0.6
const FIT = props.compact ? 0.45 : READABLE
const PAD = 24
function focusIds(): string[] {
  const plan = props.plan
  if (!plan) return []
  const visible = new Set(doc.value.nodes.map((node) => node.id))
  if (preview.value) {
    return [
      ...touchedBy(openProposals.value.find((p) => p.id === planUi.previewId)?.ops ?? []),
    ].filter((id) => visible.has(id))
  }
  const waiting = plan.nodes
    .filter((node) => node.status === 'waiting' && visible.has(node.id))
    .map((node) => node.id)
  return [...new Set([...waiting, ...proposed.value, ...fresh.value])].filter((id) =>
    visible.has(id),
  )
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
  if (zoom < FIT) {
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
// Re-frame when switching projects, previewing a change, or toggling completed items.
watch([preview, showCompleted, () => props.plan?.projectId], async () => {
  await nextTick()
  frameWhenMeasured()
})

const selection = ref<FlowSelection>({ nodes: [], edges: [] })
watch(selection, (next) => {
  selected.value = next.nodes.find((id) => !isGroupId(id)) ?? null
})
watch(selected, (id) => {
  if (id && props.plan) markNodeSeen(props.plan, id)
  const current = selection.value.nodes.find((nid) => !isGroupId(nid)) ?? null
  if (current !== id) {
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
    readonly
    acyclic
    direction="vertical"
    :searchable="!compact"
    :bordered="!compact"
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
