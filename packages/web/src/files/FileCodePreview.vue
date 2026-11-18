<script setup lang="ts">
import {
  css,
  DouCodeEditor,
  html,
  javascript,
  json,
  jsx,
  languageFor,
  markdown,
  python,
  scss,
  shell,
  sql,
  tsx,
  typescript,
  vue,
  yaml,
  type CodeLanguage,
} from '@dou-ui/code'
import '@dou-ui/code/style.css'
import { computed, shallowRef, watch } from 'vue'
import { codeLanguage } from './preview'

const props = defineProps<{ name: string; text: string }>()

const builtIn = [
  javascript,
  typescript,
  jsx,
  tsx,
  css,
  scss,
  html,
  vue,
  json,
  yaml,
  python,
  shell,
  sql,
  markdown,
]
const extension = computed(() => codeLanguage(props.name))
const language = computed(() => languageFor(builtIn, extension.value))
const extra = shallowRef<CodeLanguage | null>(null)

watch(
  [extension, language],
  async ([name, builtin], _previous, cleanup) => {
    extra.value = null
    if (!name || builtin) return
    let active = true
    cleanup(() => (active = false))
    try {
      const { extraCodeLanguage } = await import('./preview-highlighting')
      if (!active) return
      const grammar = await extraCodeLanguage(name)
      if (active) extra.value = grammar
    } catch {
      // The file can still be read if the optional tokenizer fails to load.
    }
  },
  { immediate: true },
)
</script>

<template>
  <DouCodeEditor
    :model-value="text"
    :language="language ?? extra"
    :worker="!!language && text.length > 100000"
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
