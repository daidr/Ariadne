<script setup lang="ts">
import { DouImage } from 'dou-ui'
import { computed, ref, watch } from 'vue'
import ImagePreview from '../components/ImagePreview.vue'
import { douLocale } from '../i18n'

/** An image kept from a thread's workspace, in the message; clicking opens it large. */
const props = defineProps<{ src: string; alt: string; title?: string | undefined }>()

const loaded = ref(false)
const previewOpen = ref(false)

watch(
  () => props.src,
  () => {
    loaded.value = false
  },
)

const previewImages = computed(() => [{ src: props.src, alt: props.alt }])

function openPreview() {
  if (!loaded.value) return
  previewOpen.value = true
}

function onKeydown(event: KeyboardEvent) {
  if (!loaded.value || (event.key !== 'Enter' && event.key !== ' ')) return
  event.preventDefault()
  openPreview()
}
</script>

<template>
  <span
    class="message-image"
    :class="{ 'is-previewable': loaded }"
    :role="loaded ? 'button' : undefined"
    :tabindex="loaded ? 0 : undefined"
    :aria-label="loaded ? alt || douLocale.image.preview : undefined"
    :aria-haspopup="loaded ? 'dialog' : undefined"
    @click="openPreview"
    @keydown="onKeydown"
  >
    <DouImage
      :src="src"
      :alt="alt"
      :title="title"
      max-width="100%"
      :max-height="320"
      lazy
      @load="loaded = true"
      @error="loaded = false"
    />
  </span>
  <ImagePreview v-model:open="previewOpen" :images="previewImages" />
</template>

<style scoped>
.message-image {
  display: block;
  width: fit-content;
  max-width: 100%;
  margin-block: 4px;
  border-radius: var(--dou-radius-control);
}

.message-image.is-previewable {
  cursor: zoom-in;
}

.message-image.is-previewable:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: 2px;
}
</style>
