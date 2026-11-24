<script setup lang="ts">
import type { ArtifactCommentAnchor, ArtifactCommentThreadView } from '@ariadne/core'
import { DouEditor, editorKit, type EditorDocument, type PartialBlock } from '@dou-ui/editor'
import { collaboration } from '@dou-ui/editor/collaboration'
import { whiteboardBlock } from '@dou-ui/editor/flow'
import { markdownToBlocks } from '@dou-ui/editor/markdown'
import '@dou-ui/editor/style.css'
import '@dou-ui/flow/style.css'
import type { CommentAction, CommentThread } from 'dou-ui'
import { computed, onBeforeUnmount, ref, shallowRef, useTemplateRef, watch } from 'vue'
import { anchoredIds, anchorQuotes, commentTarget } from './comment-anchors'
import { commentAuthor, toCommentThread } from './comment-threads'
import { api } from '../api'
import { t } from '../i18n'
import { askDocumentBlock, normalizeAskProps, type AskBlockProps } from './ask-block'
import {
  applyRecommendedDocumentAsks,
  readSharedDocumentAsks,
  summarizeDocumentAsks,
  type DocumentAskSummary,
} from './document-asks'
import { documentCodeLanguages, highlightDocumentCode } from './document-code'
import { documentLocks } from './document-locks'
import { pendingDocumentBlock } from './pending-block'
import { refDocumentBlock } from './ref-block'
import type { SharedArtifact } from './shared'

/**
 * A Markdown document in the block editor. The newest version is edited together through
 * `shared`, where the comments' marks travel with the text; an older one is shown
 * read-only from `content`, its comments marked again on the text they quote.
 */
const props = defineProps<{
  content: string
  projectId: string
  readonly: boolean
  label?: string
  /** `undefined` turns comments off, as for an older version. */
  comments?: ArtifactCommentThreadView[]
  shared?: SharedArtifact
}>()
const emit = defineEmits<{
  comment: [action: CommentAction, target: string | ArtifactCommentAnchor | null, quote?: string]
  'ask-summary': [summary: DocumentAskSummary]
}>()
const active = defineModel<string | null>('activeComment', { default: null })

const me = commentAuthor('user')
// The same blocks as the server's (`documentExtensions`): a block one side does not know
// is lost when the other writes the document. Images on a whiteboard are uploaded as files.
const upload = async (file: File) => (await api.uploadFile(props.projectId, file)).file.url
const blocks = [
  editorKit({
    codeBlock: {
      highlight: highlightDocumentCode,
      languages: documentCodeLanguages,
    },
  }),
  whiteboardBlock({ upload, commentable: true }),
  pendingDocumentBlock,
  askDocumentBlock,
  refDocumentBlock,
]
const extensions = props.shared
  ? [
      ...blocks,
      collaboration({
        map: props.shared.doc.getMap('document'),
        awareness: props.shared.awareness,
        user: { name: me.name },
      }),
      documentLocks(props.shared.awareness, () => t('artifact.aiEditing')),
    ]
  : blocks
const doc = shallowRef<EditorDocument<PartialBlock>>({ version: 1, blocks: [] })
// Bumped to remount the editor when content is replaced from outside.
const generation = ref(0)
const editor = useTemplateRef<InstanceType<typeof DouEditor>>('editor')

/** The threads as the editor shows them; changes here show at once, before the server. */
const threads = ref<CommentThread[]>([])
watch(
  () => props.comments,
  (comments) => (threads.value = (comments ?? []).map(toCommentThread)),
  { immediate: true },
)

function load() {
  // Edited together, the document comes from the room, marks and all.
  if (props.shared) return
  const read = markdownToBlocks(props.content, extensions) as PartialBlock[]
  doc.value = { version: 1, blocks: anchorQuotes(read, props.comments ?? []).blocks }
  generation.value++
}
watch(() => props.content, load, { immediate: true })

const ready = computed(() => !props.shared || props.shared.ready.value)

function emitAskSummary() {
  if (props.shared) {
    const entries = readSharedDocumentAsks(props.shared.doc.getMap('document'))
    emit('ask-summary', summarizeDocumentAsks(entries.map((entry) => entry.ask)))
    return
  }
  const asks: AskBlockProps[] = []
  const collect = (list: readonly PartialBlock[]) => {
    for (const block of list) {
      if (block.type === 'ask' && block.props) {
        const normalized = normalizeAskProps(block.props as Record<string, unknown>)
        if (normalized) asks.push(normalized)
      }
      if (Array.isArray(block.children) && block.children.length) collect(block.children)
    }
  }
  collect(doc.value.blocks)
  emit('ask-summary', summarizeDocumentAsks(asks))
}

if (props.shared) {
  const root = props.shared.doc.getMap('document')
  const onDocChange = () => emitAskSummary()
  root.observeDeep(onDocChange)
  watch(ready, (now) => now && emitAskSummary(), { immediate: true })
  onBeforeUnmount(() => root.unobserveDeep(onDocChange))
} else {
  watch(() => doc.value.blocks, emitAskSummary, { immediate: true })
}

function applyRecommendedPicks(): number {
  if (!props.shared) return 0
  return applyRecommendedDocumentAsks(props.shared.doc)
}

// A thread started elsewhere (an agent's) is marked on the text it quotes.
watch(
  () => props.comments,
  (comments) => {
    if (props.shared || !comments) return
    const marked = anchoredIds(doc.value.blocks)
    const missing = comments.some((c) => !c.anchor?.block && !marked.has(c.id) && c.quote)
    if (missing && anchorQuotes(doc.value.blocks, comments).found.size > marked.size) {
      load()
    }
  },
)

function onComment(action: CommentAction) {
  if (action.type !== 'create') return emit('comment', action, '')
  const anchor = editor.value?.getCommentAnchors()[action.thread.id]
  emit('comment', action, commentTarget(anchor), anchor?.text)
}

/** The commented text as it reads now, by thread; saved with an edit so threads follow it. */
function quotes(): Record<string, string> {
  const anchors = editor.value?.getCommentAnchors() ?? {}
  return Object.fromEntries(
    Object.entries(anchors).flatMap(([id, anchor]) =>
      anchor && !anchor.type ? [[id, anchor.text]] : [],
    ),
  )
}

/** Ids of the threads whose text is in the document. */
function anchored(): Set<string> {
  return new Set(
    Object.entries(editor.value?.getCommentAnchors() ?? {}).flatMap(([id, anchor]) =>
      anchor ? [id] : [],
    ),
  )
}

function revealComment(id: string) {
  editor.value?.scrollToComment(id)
}

defineExpose({ quotes, anchored, revealComment, applyRecommendedPicks })
</script>

<template>
  <div class="document">
    <DouEditor
      v-if="ready"
      ref="editor"
      :key="generation"
      v-model="doc"
      v-model:active-comment="active"
      :readonly="readonly"
      :extensions="extensions"
      :aria-label="label"
      :comments="comments ? threads : undefined"
      :author="comments ? me : undefined"
      class="document-text"
      @update:comments="(next: CommentThread[]) => (threads = next)"
      @comment-action="onComment"
    />
  </div>
</template>

<style scoped>
.document {
  height: 100%;
  overflow-y: auto;
  /* Room on the left for the block handle with its "+", which floats outside the text. */
  padding: 32px 48px 96px;
  box-sizing: border-box;
}

.document-text {
  --dou-editor-line-height: 1.8;
  --dou-editor-reading-width: 48em;
  --dou-editor-block-gap: 10px;
  --dou-editor-list-gap: 4px;
  --dou-editor-table-col-max-width: calc(0.75 * var(--dou-editor-reading-width));
}

.document-text :deep([data-ai-editing] > .dou-editor__content) {
  background-color: color-mix(in srgb, var(--dou-color-primary) 9%, transparent);
}
</style>
