<script setup lang="ts">
import { computed } from 'vue'
import { t } from '../i18n'
import { useServerNow } from '../server-clock'
import { messageTime } from './conversation-timeline'

const props = defineProps<{ at: number; unread?: boolean }>()
const now = useServerNow()
const label = computed(() => (props.unread ? t('message.new') : messageTime(props.at, now.value)))
</script>

<template>
  <div class="message-marker" :class="{ 'message-marker-unread': unread }">
    <span v-if="unread">{{ label }}</span>
    <time v-else :datetime="new Date(at).toISOString()">{{ label }}</time>
  </div>
</template>

<style scoped>
.message-marker {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  margin-block-end: 12px;
  color: color-mix(in oklab, var(--dou-color-text-tertiary) 77%, transparent);
  font-size: 12px;
  line-height: 1.5;
  white-space: nowrap;
}
.message-marker::before,
.message-marker::after {
  content: '';
  inline-size: min(48px, 18%);
  block-size: 6px;
  background-color: currentColor;
  mask: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 12 6'%3E%3Cpath d='M0 1.5C3 1.5 3 4.5 6 4.5S9 1.5 12 1.5' fill='none' stroke='black' stroke-width='1.25' stroke-linecap='round'/%3E%3C/svg%3E")
    left center / 12px 6px repeat-x;
  opacity: 0.48;
}
.message-marker-unread {
  color: var(--dou-color-primary-text);
}
.message-marker-unread::before,
.message-marker-unread::after {
  flex: 1;
}
</style>
