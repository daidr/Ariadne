<script setup lang="ts">
import type { NoticeView } from '@ariadne/core'
import { DouLink, DouText } from 'dou-ui'
import { computed } from 'vue'
import { t } from '../i18n'
import { openMain, openThread } from '../store'

/**
 * A review round started or ended, in line with the author's and the reviewer's
 * messages: the author's line opens the reviewer, and a round with comments opens them.
 */
const props = defineProps<{ notice: Extract<NoticeView, { kind: 'review' }> }>()

const inAuthor = computed(() => props.notice.conversationId === props.notice.threadId)
const text = computed(() => {
  const { round, state, count } = props.notice
  if (state === 'started') return t('review.noticeStarted', { round })
  if (state === 'pass') return t('review.noticePass', { round })
  return t('review.noticeChanges', { round, count })
})
</script>

<template>
  <DouText variant="tertiary" size="sm" class="notice-text">
    {{ text }}
    <DouLink
      v-if="notice.state === 'changes'"
      @click="openMain({ kind: 'changes', threadId: notice.threadId })"
    >
      {{ t('review.openComments') }}
    </DouLink>
    <DouLink v-else-if="inAuthor" @click="openThread(notice.reviewerId)">
      {{ t('review.openReviewer') }}
    </DouLink>
  </DouText>
</template>
