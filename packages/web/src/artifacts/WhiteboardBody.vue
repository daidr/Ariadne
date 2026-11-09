<script setup lang="ts">
import type { ArtifactCommentThreadView, WhiteboardContent } from '@ariadne/core'
import {
  DouFlowControls,
  DouFlowEditor,
  renderFlowPNG,
  type FlowDocument,
  type FlowNode,
} from '@dou-ui/flow'
import { collaboration } from '@dou-ui/flow/collaboration'
import '@dou-ui/flow/style.css'
import type { CommentAction, CommentThread } from 'dou-ui'
import { computed, nextTick, ref, shallowRef, useTemplateRef, watch } from 'vue'
import { api } from '../api'
import { commentAuthor, toCommentThread } from './comment-threads'
import type { SharedArtifact } from './shared'
import { whiteboardExtensions } from './whiteboard-extensions'

/**
 * A whiteboard: the flow editor with the whiteboard kit. The newest version is edited
 * together through `shared`; an older one is shown read-only from `content`.
 */
const props = defineProps<{
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

async function onComment(action: CommentAction) {
  if (action.type !== 'create') return emit('comment', action, null)
  let anchor = anchorOf(action.thread.id)
  // A thread started with the comment tool is put on its item just after.
  for (let tries = 0; !anchor && tries < 3; tries++) {
    await nextTick()
    await new Promise((resolve) => requestAnimationFrame(resolve))
    anchor = anchorOf(action.thread.id)
  }
  // Where on the item it is pinned stays in the board itself (its commentPins).
  emit(
    'comment',
    action,
    anchor && ('node' in anchor ? { node: anchor.node } : { edge: anchor.edge }),
  )
}

const ready = computed(() => !props.shared || props.shared.ready.value)

// ---- pictures of what the user marked, for the agent ----

/** The board as the agent last heard about it. */
let heard = ''
const nodesOf = (doc: FlowDocument) => new Map(doc.nodes.map((n) => [n.id, JSON.stringify(n)]))

/** Start counting the user's marks from the board as it is now. */
function markHeard() {
  heard = JSON.stringify(board.value)
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

defineExpose({ markedPictures, markHeard })
</script>

<template>
  <div class="whiteboard">
    <DouFlowEditor
      v-if="ready"
      ref="editor"
      v-model="board"
      v-model:active-comment="active"
      :extensions="extensions"
      :readonly="readonly"
      :aria-label="label"
      :comments="comments ? threads : undefined"
      :author="comments ? me : undefined"
      fit-view
      class="whiteboard-editor"
      @update:comments="(next: CommentThread[]) => (threads = next)"
      @comment-action="onComment"
    >
      <DouFlowControls :tidy="false" placement="bottom-left" />
    </DouFlowEditor>
  </div>
</template>

<style scoped>
.whiteboard {
  height: 100%;
  min-height: 0;
}

.whiteboard-editor {
  width: 100%;
  height: 100%;
}
</style>
