<script setup lang="ts">
import type { QuotedMessage } from '@ariadne/core'
import { DouText } from 'dou-ui'
import { t } from '../i18n'
import { openMessage } from '../store'

/** The earlier message an agent's message answers: a line above it that leads back there. */
defineProps<{ quote: QuotedMessage; conversationId: string }>()
</script>

<template>
  <button
    type="button"
    class="quote"
    :title="quote.text"
    @click="openMessage(conversationId, quote.messageId)"
  >
    <DouText size="sm" variant="secondary" truncate class="quote-text">
      <span class="quote-who">{{ t(`message.quote.${quote.author}`) }}</span>
      {{ quote.text }}
    </DouText>
  </button>
</template>

<style scoped>
.quote {
  display: flex;
  max-width: 100%;
  margin: 0;
  padding: 0 0 0 8px;
  font: inherit;
  text-align: start;
  color: inherit;
  cursor: pointer;
  background: none;
  border: 0;
  border-inline-start: 2px solid var(--dou-color-border-strong);
  border-radius: 0;
}

.quote:hover .quote-text {
  color: var(--dou-color-text);
}

.quote:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: 2px;
}

.quote-text {
  min-width: 0;
}

.quote-who {
  margin-inline-end: 4px;
  font-weight: 500;
}
</style>
