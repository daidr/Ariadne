<script setup lang="ts">
import type { ArtifactCommentAnchor, ArtifactCommentThreadView } from '@ariadne/core'
import { DouEditor, editorKit, type EditorDocument, type PartialBlock } from '@dou-ui/editor'
import { collaboration } from '@dou-ui/editor/collaboration'
import { whiteboardBlock } from '@dou-ui/editor/flow'
import { markdownToBlocks } from '@dou-ui/editor/markdown'
import '@dou-ui/editor/style.css'
import '@dou-ui/flow/style.css'
import type { CommentAction, CommentThread } from 'dou-ui'
import { computed, nextTick, ref, shallowRef, useTemplateRef, watch } from 'vue'
import { anchoredIds, anchorQuotes, commentTarget } from './comment-anchors'
import { commentAuthor, toCommentThread } from './comment-threads'
import { api } from '../api'
import { t } from '../i18n'
import { documentCodeLanguages, highlightDocumentCode } from './document-code'
import { documentLocks } from './document-locks'
import { pendingDocumentBlock } from './pending-block'
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
const scrollArea = useTemplateRef<HTMLElement>('scrollArea')

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

// The anchors list only threads the editor was given, so a new thread's text is read once
// `threads` has reached it.
async function onComment(action: CommentAction) {
  if (action.type !== 'create') return emit('comment', action, '')
  await nextTick()
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
  const container = scrollArea.value
  const instance = editor.value
  const view = instance?.view
  if (!container || !view) return
  const anchor = instance.getCommentAnchors()[id]
  if (!anchor) return
  const { top, bottom } = view.coordsAtPos(anchor.from)
  const viewportTop = container.getBoundingClientRect().top + container.clientTop
  if (top >= viewportTop && bottom <= viewportTop + container.clientHeight) return
  const viewportCenter = viewportTop + container.clientHeight / 2
  container.scrollTop += (top + bottom) / 2 - viewportCenter
}

defineExpose({ quotes, anchored, revealComment })
</script>

<template>
  <div ref="scrollArea" class="document">
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
}

.document-text :deep(.dou-editor-table col:not([style*='width:'])) {
  /* DouUI budgets 96px per automatic column. Apply that minimum to each column. */
  min-inline-size: 96px;
}

.document-text :deep(.dou-editor-table :is(th, td)) {
  /* Limit intrinsic sizing. Explicit widths on the columns still take precedence. */
  max-inline-size: calc(0.75 * var(--dou-editor-reading-width));
  overflow-wrap: anywhere;
}

.document-text :deep([data-comment] .dou-editor__inline-code) {
  /* The comment wraps the code. Keep its tint and underline visible through the code styling. */
  padding-block: 0;
  background-color: color-mix(in srgb, var(--dou-color-text) 6%, transparent);
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--dou-color-text) 5%, transparent);
}

.document-text :deep([data-ai-editing] > .dou-editor__content) {
  background-color: color-mix(in srgb, var(--dou-color-primary) 9%, transparent);
}
</style>
