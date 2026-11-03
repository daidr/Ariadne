<script setup lang="ts">
import type { WhiteboardContent } from '@ariadne/core'
import { DouFlowControls, DouFlowEditor, flowKit, type FlowDocument } from '@dou-ui/flow'
import { whiteboardKit } from '@dou-ui/flow/whiteboard'
import '@dou-ui/flow/style.css'
import { nextTick, ref, shallowRef, watch } from 'vue'

/** A whiteboard: the flow editor with the whiteboard kit, stored as its FlowDocument. */
const props = defineProps<{ content: string; readonly: boolean; label?: string }>()
const emit = defineEmits<{ draft: [content: string | null] }>()

const extensions = [flowKit(), whiteboardKit()]
const board = shallowRef<FlowDocument>({ version: 1, nodes: [], edges: [] })
const generation = ref(0)

watch(
  () => props.content,
  (content) => {
    board.value = JSON.parse(content) as WhiteboardContent as FlowDocument
    generation.value++
  },
  { immediate: true },
)

// Only a real edit (a transaction) makes a draft, not the editor settling the document in.
function changed() {
  void nextTick(() => emit('draft', JSON.stringify(board.value)))
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
