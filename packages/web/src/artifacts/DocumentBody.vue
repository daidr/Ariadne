<script setup lang="ts">
import type { ArtifactCommentThreadView } from '@ariadne/core'
import { DouEditor, editorKit, type EditorDocument, type PartialBlock } from '@dou-ui/editor'
import { blocksToMarkdown, markdownToBlocks } from '@dou-ui/editor/markdown'
import '@dou-ui/editor/style.css'
import type { CommentAction, CommentThread } from 'dou-ui'
import { computed, nextTick, ref, shallowRef, useTemplateRef, watch } from 'vue'
import { anchoredIds, anchorQuotes } from './comment-anchors'
import { commentAuthor, toCommentThread } from './comment-threads'

/**
 * A Markdown document in the block editor. Comments are kept apart from the Markdown, by
 * the text they quote, and marked on that text again whenever the document is loaded.
 */
const props = defineProps<{
  content: string
  readonly: boolean
  label?: string
  /** `undefined` turns comments off, as for an older version. */
  comments?: ArtifactCommentThreadView[]
}>()
const emit = defineEmits<{
  draft: [content: string | null]
  comment: [action: CommentAction, quote: string]
}>()
const active = defineModel<string | null>('activeComment', { default: null })

const extensions = [editorKit()]
const doc = shallowRef<EditorDocument<PartialBlock>>({ version: 1, blocks: [] })
/** Markdown of the document as loaded, written the way saving would write it. */
const baseline = ref('')
// Bumped to remount the editor when content is replaced from outside.
const generation = ref(0)
const editor = useTemplateRef<InstanceType<typeof DouEditor>>('editor')

const me = commentAuthor('user')

/** The threads as the editor shows them; changes here show at once, before the server. */
const threads = ref<CommentThread[]>([])
watch(
  () => props.comments,
  (comments) => (threads.value = (comments ?? []).map(toCommentThread)),
  { immediate: true },
)

function load() {
  const read = markdownToBlocks(props.content, extensions) as PartialBlock[]
  baseline.value = blocksToMarkdown(read as never, extensions)
  doc.value = { version: 1, blocks: anchorQuotes(read, props.comments ?? []).blocks }
  generation.value++
}
watch(() => props.content, load, { immediate: true })

const current = computed(() => blocksToMarkdown(doc.value.blocks as never, extensions))
const dirty = computed(() => current.value !== baseline.value)
watch(current, (markdown) => emit('draft', markdown === baseline.value ? null : markdown))

// A thread started elsewhere (an agent's) is marked once nothing unsaved would be lost.
watch(
  () => props.comments,
  (comments) => {
    if (dirty.value || !comments) return
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
  padding: 32px 24px 96px;
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
