<script setup lang="ts">
import { defineAsyncComponent } from 'vue'
import type { OpenedView } from '../store'

// Each brings a large editor of its own: loaded once one is opened.
const ArtifactEditor = defineAsyncComponent(() => import('../artifacts/ArtifactEditor.vue'))
const ChangesView = defineAsyncComponent(() => import('../changes/ChangesView.vue'))

/** An artifact or changes, in a tab on the right side or full screen. */
defineProps<{ view: OpenedView; place: 'side' | 'full' }>()
</script>

<template>
  <ArtifactEditor v-if="view.kind === 'artifact'" :id="view.id" :place="place" />
  <ChangesView
    v-else
    :thread-id="view.threadId"
    :turn="view.turn"
    :path="view.path"
    :place="place"
  />
</template>
