<script setup lang="ts">
import { DouKbd, isApplePlatform } from 'dou-ui'
import { ref } from 'vue'
import { hotkeyOf } from '../shortcuts'

/**
 * One key binding: click it, press the new key. Leaving it (blur) keeps the old one. dou-ui has
 * no hotkey recorder yet; this follows its field look.
 */

const props = defineProps<{ modelValue: string; label: string }>()
const emit = defineEmits<{ 'update:modelValue': [key: string] }>()
const recording = ref(false)

function onKeydown(event: KeyboardEvent) {
  if (!recording.value) {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      recording.value = true
    }
    return
  }
  // The press is the new key; nothing else on the page should act on it.
  event.preventDefault()
  event.stopPropagation()
  const key = hotkeyOf(event, isApplePlatform())
  if (!key) return
  recording.value = false
  if (key !== props.modelValue) emit('update:modelValue', key)
}
</script>

<template>
  <button
    type="button"
    class="recorder"
    :class="{ 'recorder-on': recording }"
    :aria-label="label"
    :aria-pressed="recording"
    @click="recording = !recording"
    @keydown="onKeydown"
    @blur="recording = false"
  >
    <DouKbd v-if="!recording" size="sm" :keys="modelValue" />
    <span v-else class="recorder-wait" aria-hidden="true">…</span>
  </button>
</template>

<style scoped>
.recorder {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  box-sizing: border-box;
  min-width: 96px;
  height: var(--dou-control-height-sm);
  padding: 0 var(--dou-control-padding-x-sm);
  border: 1px solid var(--dou-color-border);
  border-radius: var(--dou-radius-control);
  background-color: var(--dou-color-bg-surface);
  box-shadow: var(--dou-shadow-sm);
  color: var(--dou-color-text);
  font: inherit;
  cursor: pointer;
  transition-property: border-color, box-shadow;
  transition-duration: var(--dou-motion-duration-fast);
  transition-timing-function: var(--dou-motion-easing);
}

.recorder:hover {
  border-color: var(--dou-color-border-strong);
}

.recorder:focus-visible,
.recorder-on {
  outline: none;
  border-color: color-mix(in oklab, var(--dou-color-focus) 70%, var(--dou-color-text));
  box-shadow:
    0 0 0 3px color-mix(in oklab, var(--dou-color-focus) 32%, transparent),
    var(--dou-shadow-sm);
}

.recorder-wait {
  color: var(--dou-color-text-tertiary);
}
</style>
