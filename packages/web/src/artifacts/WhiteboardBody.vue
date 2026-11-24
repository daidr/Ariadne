<script setup lang="ts">
import type { ArtifactCommentThreadView, WhiteboardContent } from '@ariadne/core'
import {
  DouFlowControls,
  DouFlowEditor,
  renderFlowPNG,
  type FlowDocument,
  type FlowNode,
  type FlowViewport,
} from '@dou-ui/flow'
import { collaboration } from '@dou-ui/flow/collaboration'
import '@dou-ui/flow/style.css'
import { CloseIcon, LocateFixedIcon } from '@dou-ui/icons'
import { DouButton, DouText, type CommentAction, type CommentThread } from 'dou-ui'
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, useTemplateRef, watch } from 'vue'
import { api } from '../api'
import { t } from '../i18n'
import { artifactFocusTarget, clearArtifactFocus } from './artifact-focus'
import { commentAuthor, toCommentThread } from './comment-threads'
import {
  areNodesOutsideViewport,
  collectAutoRecommendedOptionIds,
  summarizeDecisionGroups,
  type DecisionGroupData,
  type DecisionGroupSummary,
} from './decision-groups'
import type { SharedArtifact } from './shared'
import { whiteboardExtensions } from './whiteboard-extensions'

/**
 * A whiteboard: the flow editor with the whiteboard kit. The newest version is edited
 * together through `shared`; an older one is shown read-only from `content`.
 */
const props = defineProps<{
  artifactId?: string
  content: string
  projectId: string
  readonly: boolean
  label?: string
  shared?: SharedArtifact
  /** `undefined` turns comments off, as for an older version. */
  comments?: ArtifactCommentThreadView[]
}>()
const emit = defineEmits<{
  comment: [action: CommentAction, anchor: { node: string } | { edge: string } | null]
  decisionSummary: [summary: DecisionGroupSummary]
}>()
const active = defineModel<string | null>('activeComment', { default: null })

// Pasted and dropped images are uploaded as files; the board keeps their address.
// A failed upload is announced by the editor itself.
const upload = async (file: File) => (await api.uploadFile(props.projectId, file)).file.url
const base = whiteboardExtensions(upload)
const extensions = props.shared
  ? [
      ...base,
      collaboration({
        nodes: props.shared.doc.getMap('nodes'),
        edges: props.shared.doc.getMap('edges'),
        awareness: props.shared.awareness,
        user: { name: commentAuthor('user').name },
      }),
    ]
  : base

const board = shallowRef<FlowDocument>({ version: 1, nodes: [], edges: [] })
const viewport = ref<FlowViewport>({ x: 0, y: 0, zoom: 1 })
const container = useTemplateRef<HTMLElement>('container')
const editor = useTemplateRef<InstanceType<typeof DouFlowEditor>>('editor')
watch(
  () => props.content,
  (content) => {
    if (!props.shared) board.value = JSON.parse(content) as WhiteboardContent as FlowDocument
  },
  { immediate: true },
)

const me = commentAuthor('user')
const threads = ref<CommentThread[]>([])
watch(
  () => props.comments,
  (comments) => (threads.value = (comments ?? []).map(toCommentThread)),
  { immediate: true },
)

const anchorOf = (id: string) => editor.value?.getCommentAnchors()[id] ?? null

function onComment(action: CommentAction) {
  if (action.type !== 'create') return emit('comment', action, null)
  const anchor = anchorOf(action.thread.id)
  // Where on the item it is pinned stays in the board itself (its commentPins).
  emit(
    'comment',
    action,
    anchor && ('node' in anchor ? { node: anchor.node } : { edge: anchor.edge }),
  )
}

const ready = computed(() => !props.shared || props.shared.ready.value)

// ---- decision groups & offscreen AI additions ----

const decisionSummary = computed<DecisionGroupSummary>(() => summarizeDecisionGroups(board.value))
watch(decisionSummary, (summary) => emit('decisionSummary', summary), { immediate: true })

interface OffscreenUpdate {
  nodeIds: string[]
  round?: number
}

const offscreenUpdate = ref<OffscreenUpdate | null>(null)
const knownNodeIds = new Set<string>()
let pendingRemoteCheck = false

function onDocUpdate(_update: Uint8Array, origin: unknown) {
  if (ready.value && origin === props.shared?.provider.value) {
    pendingRemoteCheck = true
  }
}

props.shared?.doc.on('update', onDocUpdate)
onBeforeUnmount(() => {
  props.shared?.doc.off('update', onDocUpdate)
})

watch(
  ready,
  (now) => {
    if (!now) return
    knownNodeIds.clear()
    for (const node of board.value.nodes) knownNodeIds.add(node.id)
  },
  { immediate: true },
)

watch(
  () => board.value.nodes,
  (nodes) => {
    if (!ready.value) return
    const added: FlowNode[] = []
    const currentIds = new Set<string>()
    for (const node of nodes) {
      currentIds.add(node.id)
      if (!knownNodeIds.has(node.id)) added.push(node)
    }
    knownNodeIds.clear()
    for (const id of currentIds) knownNodeIds.add(id)

    if (!pendingRemoteCheck) return
    pendingRemoteCheck = false
    if (!added.length || !container.value) return
    const canvasSize = {
      width: container.value.clientWidth,
      height: container.value.clientHeight,
    }
    if (!areNodesOutsideViewport(added, viewport.value, canvasSize)) return

    let round: number | undefined
    for (const node of added) {
      if (node.type !== 'group') continue
      const r = (node.data as DecisionGroupData | undefined)?.round
      if (typeof r === 'number' && r > (round ?? 0)) round = r
    }
    offscreenUpdate.value = { nodeIds: added.map((n) => n.id), round }
  },
)

// Dismiss the offscreen prompt automatically once the user pans those nodes into view.
watch(viewport, (vp) => {
  const pending = offscreenUpdate.value
  if (!pending || !container.value) return
  const byId = new Map(board.value.nodes.map((n) => [n.id, n]))
  const targets = pending.nodeIds
    .map((id) => byId.get(id))
    .filter((n): n is FlowNode => n !== undefined)
  if (!targets.length) {
    offscreenUpdate.value = null
    return
  }
  const outside = areNodesOutsideViewport(targets, vp, {
    width: container.value.clientWidth,
    height: container.value.clientHeight,
  })
  if (!outside) offscreenUpdate.value = null
})

function locateOffscreen() {
  const pending = offscreenUpdate.value
  if (!pending) return
  editor.value?.fit(pending.nodeIds)
  offscreenUpdate.value = null
}

// Focus a specific group or option when opened via a cross-artifact reference card.
watch(
  [artifactFocusTarget, ready, () => board.value.nodes.length],
  ([target, isReady]) => {
    if (!target || !isReady) return
    const currentId = props.artifactId
    if (!currentId || target.artifactId !== currentId) return
    if (!board.value.nodes.some((n) => n.id === target.nodeId)) return
    const seq = target.seq
    const nodeId = target.nodeId
    clearArtifactFocus(seq)
    void nextTick(() => {
      editor.value?.fit([nodeId])
    })
  },
  { immediate: true },
)

function applyRecommendedPicks(): number {
  const ids = collectAutoRecommendedOptionIds(board.value)
  if (!ids.length) return 0
  editor.value?.transact((tr) => {
    for (const id of ids) tr.updateData(id, { chosen: true })
  })
  return ids.length
}

// ---- pictures of what the user marked, for the agent ----

/** The board as the agent last heard about it. */
let heard = ''
const nodesOf = (doc: FlowDocument) => new Map(doc.nodes.map((n) => [n.id, JSON.stringify(n)]))

/** Start counting the user's marks from the board as it is now. */
function markHeard() {
  heard = JSON.stringify(board.value)
}

function getHeard() {
  return heard
}

function setHeard(snapshot: string) {
  heard = snapshot
}

function currentSnapshot() {
  return JSON.stringify(board.value)
}
watch(ready, (now) => now && markHeard(), { immediate: true })

const inside = (x: number, y: number, n: FlowNode) =>
  x >= n.x && x <= n.x + (n.width ?? 0) && y >= n.y && y <= n.y + (n.height ?? 0)

/** The option a node is in, by its container or by where it lies. */
function optionOf(doc: FlowDocument, node: FlowNode): FlowNode | null {
  const byId = new Map(doc.nodes.map((n) => [n.id, n]))
  for (let at: FlowNode | undefined = node; at; at = at.parent ? byId.get(at.parent) : undefined) {
    if (at.type === 'option') return at
  }
  const cx = node.x + (node.width ?? 0) / 2
  const cy = node.y + (node.height ?? 0) / 2
  return doc.nodes.find((n) => n.type === 'option' && inside(cx, cy, n)) ?? null
}

/**
 * Pictures of each option the user changed something on since the agent last heard, the
 * marks over it included. Taken once the user stops; nothing for a board without options.
 */
async function markedPictures(): Promise<Blob[]> {
  const doc = board.value
  const before = heard ? nodesOf(JSON.parse(heard) as FlowDocument) : new Map<string, string>()
  const touched = new Set<FlowNode>()
  for (const node of doc.nodes) {
    if (before.get(node.id) === JSON.stringify(node)) continue
    const option = optionOf(doc, node)
    if (option) touched.add(option)
  }
  const pictures: Blob[] = []
  for (const option of touched) {
    const ids = new Set(
      doc.nodes.filter((n) => n === option || optionOf(doc, n) === option).map((n) => n.id),
    )
    const part: FlowDocument = {
      version: 1,
      nodes: doc.nodes.filter((n) => ids.has(n.id)),
      edges: doc.edges.filter((e) => ids.has(e.source.node) && ids.has(e.target.node)),
    }
    pictures.push(await renderFlowPNG(part, { extensions: base, padding: 16, pixelRatio: 1 }))
  }
  return pictures
}

defineExpose({
  markedPictures,
  markHeard,
  getHeard,
  setHeard,
  currentSnapshot,
  applyRecommendedPicks,
})
</script>

<template>
  <div ref="container" class="whiteboard">
    <DouFlowEditor
      v-if="ready"
      ref="editor"
      v-model="board"
      v-model:viewport="viewport"
      v-model:active-comment="active"
      :extensions="extensions"
      :readonly="readonly"
      :aria-label="label"
      :comments="comments ? threads : undefined"
      :author="comments ? me : undefined"
      fit-view
      :bordered="false"
      class="whiteboard-editor"
      @update:comments="(next: CommentThread[]) => (threads = next)"
      @comment-action="onComment"
    >
      <DouFlowControls :tidy="false" placement="bottom-left" />
    </DouFlowEditor>
    <div v-if="offscreenUpdate" class="whiteboard-offscreen" role="status" aria-live="polite">
      <DouText size="sm">
        {{
          offscreenUpdate.round
            ? t('artifact.board.newRoundAdded', { round: offscreenUpdate.round })
            : t('artifact.board.newContentAdded')
        }}
      </DouText>
      <DouButton size="sm" variant="soft" tone="primary" @click="locateOffscreen">
        <template #leading><LocateFixedIcon /></template>
        {{ t('artifact.board.locate') }}
      </DouButton>
      <DouButton
        size="sm"
        variant="ghost"
        :aria-label="t('view.close')"
        @click="offscreenUpdate = null"
      >
        <CloseIcon />
      </DouButton>
    </div>
  </div>
</template>

<style scoped>
.whiteboard {
  position: relative;
  height: 100%;
  min-height: 0;
}

.whiteboard-editor {
  width: 100%;
  height: 100%;
}

.whiteboard-offscreen {
  position: absolute;
  right: 16px;
  bottom: 16px;
  z-index: 10;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px 6px 12px;
  border: 1px solid var(--dou-color-border);
  border-radius: var(--dou-radius-lg, 10px);
  background: var(--dou-color-surface, var(--dou-color-bg));
  box-shadow: var(--dou-shadow-md, 0 4px 12px rgb(0 0 0 / 0.08));
}
</style>
