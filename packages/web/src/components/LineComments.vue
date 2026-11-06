<script setup lang="ts">
import type { LineComment } from '@ariadne/core'
import { DouLink, DouText } from 'dou-ui'
import { t } from '../i18n'
import { openMain } from '../store'

/** The line comments a message took to a thread, each with the line it is about. */
const props = defineProps<{ comments: LineComment[]; threadId: string }>()

const open = (path: string) => openMain({ kind: 'changes', threadId: props.threadId, path })
</script>

<template>
  <div class="line-comments">
    <DouText variant="tertiary" size="sm">
      {{ t('message.comments', { count: comments.length }) }}
    </DouText>
    <div v-for="(comment, index) in comments" :key="index" class="line-comment">
      <DouLink
        class="line-comment-place"
        role="link"
        tabindex="0"
        @click="open(comment.path)"
        @keydown.enter.prevent="open(comment.path)"
      >
        {{ comment.path }}:{{ comment.line }}
      </DouLink>
      <DouText v-if="comment.quote.trim()" size="sm" variant="secondary" class="line-comment-quote">
        {{ comment.quote.trim() }}
      </DouText>
      <DouText size="sm" class="line-comment-body">{{ comment.body }}</DouText>
    </div>
  </div>
</template>

<style scoped>
.line-comments {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.line-comment {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.line-comment-place {
  align-self: flex-start;
  font-size: var(--dou-font-size-sm);
  font-family: var(--dou-font-family-mono);
}

.line-comment-quote {
  font-family: var(--dou-font-family-mono);
  white-space: pre;
  overflow: hidden;
  text-overflow: ellipsis;
}

.line-comment-body {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
