<script setup lang="ts">
import { DouImage } from 'dou-ui'

/** An image kept from a thread's workspace, in the message; clicking opens it large. */
defineProps<{ src: string; alt: string; title?: string | undefined }>()
</script>

<template>
  <DouImage :src="src" :alt="alt" :title="title" preview lazy fit="contain" class="message-image" />
</template>

<style scoped>
.message-image {
  display: block;
  max-width: 100%;
  max-height: 320px;
  margin-block: 4px;
}
.message-image :deep(img) {
  max-width: 100%;
  max-height: 320px;
}
</style>
