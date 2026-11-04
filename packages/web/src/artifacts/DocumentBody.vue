<script setup lang="ts">
import { DouEditor, editorKit, type EditorDocument, type PartialBlock } from '@dou-ui/editor'
import { blocksToMarkdown, markdownToBlocks } from '@dou-ui/editor/markdown'
import '@dou-ui/editor/style.css'
import { computed, ref, shallowRef, watch } from 'vue'

/** A Markdown document in the block editor. */
const props = defineProps<{ content: string; readonly: boolean; label?: string }>()
const emit = defineEmits<{ draft: [content: string | null] }>()

const extensions = [editorKit()]
const doc = shallowRef<EditorDocument<PartialBlock>>({ version: 1, blocks: [] })
/** Markdown of the document as loaded, written the way saving would write it. */
const baseline = ref('')
// Bumped to remount the editor when content is replaced from outside.
const generation = ref(0)

watch(
  () => props.content,
  (content) => {
    const blocks = markdownToBlocks(content, extensions)
    doc.value = { version: 1, blocks }
    baseline.value = blocksToMarkdown(blocks, extensions)
    generation.value++
  },
  { immediate: true },
)

const current = computed(() => blocksToMarkdown(doc.value.blocks as never, extensions))
watch(current, (markdown) => emit('draft', markdown === baseline.value ? null : markdown))
</script>

<template>
  <div class="document">
    <DouEditor
      :key="generation"
      v-model="doc"
      :readonly="readonly"
      :extensions="extensions"
      :aria-label="label"
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

/*
 * A readable column: about 40 characters of Chinese a line, centred, with room between
 * paragraphs. Editing adds the handle gutter on the left; the same space on the right keeps
 * the text where it was.
 */
.document :deep(.dou-editor) {
  --dou-editor-line-height: 1.8;
  --dou-editor-block-gap: 6px;
  max-width: calc(40em + 2 * var(--dou-editor-gutter));
  margin-inline: auto;
  padding-inline-end: var(--dou-editor-gutter);
}
</style>
