<script setup lang="ts">
import type { ThreadView } from '@ariadne/core'
import { DouText, DouTooltip } from 'dou-ui'
import { ScanSearchIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'

/** How many rounds the thread's latest delivery was reviewed, as an icon and a number. */
const props = defineProps<{ thread: ThreadView }>()

const rounds = computed(() =>
  props.thread.review && !props.thread.review.reviewing ? props.thread.review.rounds : 0,
)
const label = computed(() => t('review.rounds', { count: rounds.value }))
</script>

<template>
  <DouTooltip v-if="rounds" :content="label">
    <DouText size="sm" variant="tertiary" class="review-mark" role="img" :aria-label="label">
      <ScanSearchIcon aria-hidden="true" />
      <span class="review-mark-count">{{ rounds }}</span>
    </DouText>
  </DouTooltip>
</template>

<style scoped>
.review-mark {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 3px;
  white-space: nowrap;
}

/* Trimmed like the thread rows' other marks, so the icon and the number sit level. */
.review-mark-count {
  text-box: trim-both cap alphabetic;
}
</style>
