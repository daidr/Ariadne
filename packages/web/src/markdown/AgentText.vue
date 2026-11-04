<script setup lang="ts">
import { DouMarkdown } from 'dou-ui'
import MessageImage from './MessageImage.vue'
import MessageLink from './MessageLink.vue'
import { isBare, sanitizeUrl } from './links'

/** An agent's words outside a chat message (a relay, an edited message's original), as Markdown. */
defineProps<{ content: string }>()
</script>

<template>
  <DouMarkdown :content="content" link-target="_blank" :sanitize-url="sanitizeUrl">
    <template #link="{ node, href, external, children }">
      <MessageLink :href="href" :external="external" :bare="isBare(node)" :children="children" />
    </template>
    <template #image="{ src, alt, title }">
      <MessageImage :src="src" :alt="alt" :title="title" />
    </template>
  </DouMarkdown>
</template>
