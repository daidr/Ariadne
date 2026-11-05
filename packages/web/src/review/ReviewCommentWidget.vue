<script setup lang="ts">
import { DouCommentThread, DouText, type CommentThread } from 'dou-ui'

/** A reviewer's comments on one line of the changes, each with what the author did, read-only. */
defineProps<{ threads: { thread: CommentThread; label: string }[] }>()
</script>

<template>
  <div class="review-comments">
    <DouCommentThread v-for="item in threads" :key="item.thread.id" :thread="item.thread" readonly>
      <template #header>
        <DouText size="sm" variant="tertiary">{{ item.label }}</DouText>
      </template>
    </DouCommentThread>
  </div>
</template>

<style scoped>
.review-comments {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-width: 560px;
  padding: 6px 12px 6px 0;
  font-family: var(--dou-font-family);
  white-space: normal;
}
</style>
