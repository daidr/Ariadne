<script setup lang="ts">
import { DouCodeEditor } from '@dou-ui/code'
import '@dou-ui/code/style.css'
import { useCodeLanguage } from '../highlighting/code-language'

const props = defineProps<{ name: string; text: string }>()

const { language, isBuiltIn } = useCodeLanguage(() => props.name)
</script>

<template>
  <DouCodeEditor
    :model-value="text"
    :language="language"
    :worker="isBuiltIn && text.length > 100000"
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
