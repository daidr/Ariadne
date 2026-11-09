<script setup lang="ts">
import { DouCommentThread, DouText, type CommentAction, type CommentThread } from 'dou-ui'
import { commentAuthor } from '../artifacts/comment-threads'
import { t } from '../i18n'

/**
 * A line's comments under it in the diff: those the thread was sent, read-only, then
 * those not sent yet, or the box to write the first one.
 */
defineProps<{
  sent: CommentThread | null
  /** The line no longer reads as when the sent comments were written. */
  moved: boolean
  thread: CommentThread | null
  /** The box to write a first comment is open. */
  writing: boolean
}>()
const emit = defineEmits<{ action: [action: CommentAction]; cancel: [] }>()
const me = commentAuthor('user')
</script>

<template>
  <div class="line-comment">
    <DouCommentThread v-if="sent" :thread="sent" readonly>
      <template #header>
        <DouText size="sm" variant="tertiary">
          {{
            moved
              ? `${t('changes.commentSent')} · ${t('changes.lineChanged')}`
              : t('changes.commentSent')
          }}
        </DouText>
      </template>
    </DouCommentThread>
    <DouCommentThread
      v-if="thread || writing"
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
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-width: 560px;
  padding: 6px 12px 6px 0;
  font-family: var(--dou-font-family);
  white-space: normal;
}
</style>
