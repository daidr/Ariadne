<script setup lang="ts">
import { DouButton, DouTooltip } from 'dou-ui'
import { CheckIcon } from '@dou-ui/icons'
import { t } from '../i18n'
import { completeThread } from '../store'

/**
 * Complete a review. `compact` leaves more room for the thread's title in a list row.
 */
const props = defineProps<{ threadId: string; compact?: boolean }>()

function done() {
  void completeThread(props.threadId)
}
</script>

<template>
  <span class="review-actions">
    <template v-if="compact">
      <DouTooltip :content="t('thread.complete')">
        <DouButton size="xs" variant="ghost" :aria-label="t('thread.complete')" @click="done">
          <CheckIcon />
        </DouButton>
      </DouTooltip>
    </template>
    <template v-else>
      <DouButton size="sm" variant="ghost" @click="done">
        <template #leading>
          <CheckIcon />
        </template>
        {{ t('thread.complete') }}
      </DouButton>
    </template>
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
