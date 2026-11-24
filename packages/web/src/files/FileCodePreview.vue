<script setup lang="ts">
import { DouCodeEditor } from '@dou-ui/code'
import '@dou-ui/code/style.css'
import { useCodeLanguage } from '../highlighting/code-language'
import { codeEditorWorker } from '../highlighting/code-worker-pool'

const props = defineProps<{ name: string; text: string }>()

const { language } = useCodeLanguage(() => props.name)
</script>

<template>
  <DouCodeEditor
    :model-value="text"
    :language="language"
    :worker="codeEditorWorker()"
    :sync-highlight-lines="200"
    :aria-label="name"
    :bordered="false"
    :active-line="false"
    :keys="false"
    readonly
    line-numbers
    class="file-code"
  />
</template>

<style scoped>
.file-code {
  flex: 1;
  min-height: 0;
}
</style>
