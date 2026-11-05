<script setup lang="ts">
import { DouImage } from 'dou-ui'

/** An image kept from a thread's workspace, in the message; clicking opens it large. */
defineProps<{ src: string; alt: string; title?: string | undefined }>()
</script>

<template>
  <DouImage
    :src="src"
    :alt="alt"
    :title="title"
    :max-height="320"
    preview
    lazy
    class="message-image"
  />
</template>

<style scoped>
.message-image {
  display: block;
  margin-block: 4px;
}
</style>
