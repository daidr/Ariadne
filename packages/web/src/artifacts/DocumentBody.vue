<script setup lang="ts">
import type { ArtifactCommentThreadView } from '@ariadne/core'
import { DouEditor, editorKit, type EditorDocument, type PartialBlock } from '@dou-ui/editor'
import { collaboration } from '@dou-ui/editor/collaboration'
import { markdownToBlocks } from '@dou-ui/editor/markdown'
import '@dou-ui/editor/style.css'
import type { CommentAction, CommentThread } from 'dou-ui'
import { computed, nextTick, ref, shallowRef, useTemplateRef, watch } from 'vue'
import { anchoredIds, anchorQuotes } from './comment-anchors'
import { commentAuthor, toCommentThread } from './comment-threads'
import { pendingDocumentBlock } from './pending-block'
import type { SharedArtifact } from './shared'

/**
 * A Markdown document in the block editor. The newest version is edited together through
 * `shared`, where the comments' marks travel with the text; an older one is shown
 * read-only from `content`, its comments marked again on the text they quote.
 */
const props = defineProps<{
  content: string
  readonly: boolean
  label?: string
  /** `undefined` turns comments off, as for an older version. */
  comments?: ArtifactCommentThreadView[]
  shared?: SharedArtifact
}>()
const emit = defineEmits<{
  comment: [action: CommentAction, quote: string]
}>()
const active = defineModel<string | null>('activeComment', { default: null })

const me = commentAuthor('user')
const extensions = props.shared
  ? [
      editorKit(),
      pendingDocumentBlock,
      collaboration({
        map: props.shared.doc.getMap('document'),
        awareness: props.shared.awareness,
        user: { name: me.name },
      }),
    ]
  : [editorKit(), pendingDocumentBlock]
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

// A thread started elsewhere (an agent's) is marked on the text it quotes.
watch(
  () => props.comments,
  (comments) => {
    if (props.shared || !comments) return
    const marked = anchoredIds(doc.value.blocks)
    const missing = comments.some((c) => !marked.has(c.id) && c.quote)
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
  emit('comment', action, editor.value?.getCommentAnchors()[action.thread.id]?.text ?? '')
}

/** The commented text as it reads now, by thread; saved with an edit so threads follow it. */
function quotes(): Record<string, string> {
  const anchors = editor.value?.getCommentAnchors() ?? {}
  return Object.fromEntries(
    Object.entries(anchors).flatMap(([id, anchor]) => (anchor ? [[id, anchor.text]] : [])),
  )
}

/** Ids of the threads whose text is in the document. */
function anchored(): Set<string> {
  return new Set(Object.keys(quotes()))
}

defineExpose({ quotes, anchored })
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

/* A readable column, about 40 characters of Chinese a line; paragraphs apart, list items close. */
.document-text {
  --dou-editor-line-height: 1.8;
  --dou-editor-reading-width: 40em;
  --dou-editor-block-gap: 10px;
  --dou-editor-list-gap: 4px;
}
</style>
