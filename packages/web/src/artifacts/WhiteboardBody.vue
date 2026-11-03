<script setup lang="ts">
import type { WhiteboardContent } from '@ariadne/core'
import { DouFlowControls, DouFlowEditor, type FlowDocument } from '@dou-ui/flow'
import '@dou-ui/flow/style.css'
import { nextTick, ref, shallowRef, watch } from 'vue'
import { api } from '../api'
import { report } from '../store'
import { whiteboardExtensions } from './whiteboard-extensions'

/** A whiteboard: the flow editor with the whiteboard kit, stored as its FlowDocument. */
const props = defineProps<{
  content: string
  projectId: string
  readonly: boolean
  label?: string
}>()
const emit = defineEmits<{ draft: [content: string | null] }>()

// Pasted and dropped images are uploaded as files; the board keeps their address.
// A failed upload leaves the image out; say why, since the editor only tells screen readers.
const extensions = whiteboardExtensions(async (file) => {
  try {
    return (await api.uploadFile(props.projectId, file)).file.url
  } catch (error) {
    report(error)
    throw error
  }
})
const board = shallowRef<FlowDocument>({ version: 1, nodes: [], edges: [] })
const generation = ref(0)
/** The board as loaded, so undoing back to it clears the draft. */
let baseline = ''

watch(
  () => props.content,
  (content) => {
    board.value = JSON.parse(content) as WhiteboardContent as FlowDocument
    baseline = JSON.stringify(board.value)
    generation.value++
  },
  { immediate: true },
)

// Only a real edit (a transaction) makes a draft, not the editor settling the document in.
function changed() {
  void nextTick(() => {
    const current = JSON.stringify(board.value)
    emit('draft', current === baseline ? null : current)
  })
}
</script>

<template>
  <div class="whiteboard">
    <DouFlowEditor
      :key="generation"
      v-model="board"
      :extensions="extensions"
      :readonly="readonly"
      :aria-label="label"
      fit-view
      class="whiteboard-editor"
      @transaction="changed"
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
