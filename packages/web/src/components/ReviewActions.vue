<script setup lang="ts">
import { DouButton } from 'dou-ui'
import { CheckIcon, PencilIcon } from '@dou-ui/icons'
import { t } from '../i18n'
import { writeToThread } from '../shortcuts'
import { reviewThread } from '../store'

/**
 * The two ways out of a review: the user is through with it, or wants more done, which
 * opens the thread with the caret in its input.
 */
const props = defineProps<{ threadId: string }>()
</script>

<template>
  <span class="review-actions">
    <DouButton size="sm" variant="ghost" @click="reviewThread(props.threadId)">
      <template #leading><CheckIcon /></template>
      {{ t('thread.reviewed') }}
    </DouButton>
    <DouButton size="sm" variant="ghost" @click="writeToThread(props.threadId)">
      <template #leading><PencilIcon /></template>
      {{ t('thread.reviseAsk') }}
    </DouButton>
  </span>
</template>

<style scoped>
.review-actions {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 2px;
}
</style>
