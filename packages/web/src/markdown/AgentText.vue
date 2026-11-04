<script setup lang="ts">
import { DouMarkdown } from 'dou-ui'
import MessageLink from './MessageLink.vue'
import { sanitizeUrl } from './links'

/** An agent's words outside a chat message (a relay, an edited message's original), as Markdown. */
defineProps<{ content: string }>()
</script>

<template>
  <DouMarkdown :content="content" link-target="_blank" :sanitize-url="sanitizeUrl">
    <template #link="{ href, external, children }">
      <MessageLink :href="href" :external="external" :children="children" />
    </template>
  </DouMarkdown>
</template>
