<script setup lang="ts">
import { DouCommentThread, type CommentAction, type CommentThread } from 'dou-ui'
import { commentAuthor } from '../artifacts/comment-threads'
import { t } from '../i18n'

/** A line's comments under it in the diff, or the box to write the first one. */
defineProps<{ thread: CommentThread | null }>()
const emit = defineEmits<{ action: [action: CommentAction]; cancel: [] }>()
const me = commentAuthor('user')
</script>

<template>
  <div class="line-comment">
    <DouCommentThread
      :thread="thread"
      :author="me"
      :placeholder="t('changes.commentPlaceholder')"
      :autofocus="!thread"
      :resolvable="false"
      @action="(action: CommentAction) => emit('action', action)"
      @cancel="emit('cancel')"
    />
  </div>
</template>

<style scoped>
.line-comment {
  max-width: 560px;
  padding: 6px 12px 6px 0;
  font-family: var(--dou-font-family);
  white-space: normal;
}
</style>
