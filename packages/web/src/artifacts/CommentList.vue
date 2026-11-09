<script setup lang="ts">
import type { ArtifactCommentThreadView } from '@ariadne/core'
import { MessageIcon } from '@dou-ui/icons'
import { DouButton, DouCommentThread, DouPopover, DouText, type CommentAction } from 'dou-ui'
import { computed, ref } from 'vue'
import { t } from '../i18n'
import { authorName } from './author'
import { commentAuthor, toCommentThread } from './comment-threads'

/**
 * Every comment thread of a document. One whose text is in the document opens there;
 * one that was resolved, or whose text is gone, is answered here.
 */
const props = defineProps<{
  comments: ArtifactCommentThreadView[]
  /** Ids of the threads whose text is in the document now. */
  anchored: () => Set<string>
}>()
const emit = defineEmits<{ open: [id: string]; action: [action: CommentAction] }>()

const open = ref(false)
const me = commentAuthor('user')
const marked = ref(new Set<string>())

const count = computed(() => props.comments.filter((c) => !c.resolved).length)
// Open threads first, then resolved ones, each oldest first.
const sorted = computed(() =>
  props.comments.toSorted((a, b) => Number(!!a.resolved) - Number(!!b.resolved)),
)

function onOpen(value: boolean) {
  if (value) marked.value = props.anchored()
  open.value = value
}

function jump(id: string) {
  open.value = false
  emit('open', id)
}
</script>

<template>
  <DouPopover
    v-if="comments.length"
    :open="open"
    :title="t('artifact.comments')"
    :width="360"
    placement="bottom-end"
    @update:open="onOpen"
  >
    <DouButton variant="ghost" size="sm" :aria-label="t('artifact.comments')">
      <template #leading><MessageIcon /></template>
      <template v-if="count" #default>{{ count }}</template>
    </DouButton>
    <template #content>
      <div class="comment-list">
        <template v-for="thread in sorted" :key="thread.id">
          <button
            v-if="!thread.resolved && marked.has(thread.id)"
            type="button"
            class="comment-item"
            @click="jump(thread.id)"
          >
            <DouText size="sm" variant="secondary" class="comment-quote" truncate>
              {{ thread.quote }}
            </DouText>
            <DouText size="sm" truncate>
              {{
                t('artifact.commentBy', {
                  name: authorName(thread.comments[0]?.author ?? ''),
                  body: thread.comments[0]?.body ?? '',
                })
              }}
            </DouText>
            <DouText v-if="thread.comments.length > 1" size="sm" variant="tertiary">
              {{ t('artifact.commentReplies', { count: thread.comments.length - 1 }) }}
            </DouText>
          </button>
          <DouCommentThread
            v-else
            :thread="toCommentThread(thread)"
            :author="me"
            class="comment-card"
            @action="(action: CommentAction) => emit('action', action)"
          >
            <template #header>
              <DouText size="sm" variant="secondary" class="comment-quote" truncate>
                <template v-if="!thread.resolved && !marked.has(thread.id)">
                  {{ t('artifact.commentGone') }}
                </template>
                {{ thread.quote }}
              </DouText>
            </template>
          </DouCommentThread>
        </template>
      </div>
    </template>
  </DouPopover>
</template>

<style scoped>
.comment-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-height: 60vh;
  overflow-y: auto;
}

.comment-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-inline: -8px;
  padding: 6px 8px;
  border: none;
  border-radius: var(--dou-radius-control);
  background: none;
  color: inherit;
  font: inherit;
  text-align: start;
  cursor: pointer;
}

.comment-item:hover {
  background: var(--dou-color-bg-muted);
}

.comment-quote {
  padding-inline-start: 8px;
  border-inline-start: 2px solid var(--dou-color-warning);
}
</style>
