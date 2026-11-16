<script setup lang="ts">
import { DouButton, DouTooltip } from 'dou-ui'
import { CheckIcon, PencilIcon } from '@dou-ui/icons'
import { t } from '../i18n'
import { writeToThread } from '../shortcuts'
import { completeThread } from '../store'

/**
 * The two ways out of a review: the user is through with it, or wants more done, which
 * opens the thread with the caret in its input. `compact`: icons only, named on hover,
 * where a row's title needs the room.
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
      <DouTooltip :content="t('thread.reviseAsk')">
        <DouButton
          size="xs"
          variant="ghost"
          :aria-label="t('thread.reviseAsk')"
          @click="writeToThread(props.threadId)"
        >
          <PencilIcon />
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
      <DouButton size="sm" variant="ghost" @click="writeToThread(props.threadId)">
        <template #leading><PencilIcon /></template>
        {{ t('thread.reviseAsk') }}
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
