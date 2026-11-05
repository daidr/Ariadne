<script setup lang="ts">
import { DouButton, DouStatusDot, DouText } from 'dou-ui'
import { AlarmClockIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { clockText, t } from '../i18n'
import { endWait } from '../store'

/**
 * The conversation waits to look at something later, in its status line: until when and
 * for what, with waking it now or calling the wait off.
 */
const props = defineProps<{
  conversationId: string
  wait: { id: string; until: number; note: string }
}>()

const until = computed(() => clockText(props.wait.until))
</script>

<template>
  <div class="wait">
    <DouText variant="secondary" size="sm" class="wait-text" :title="wait.note">
      <AlarmClockIcon class="wait-icon" />
      <span class="wait-until">{{ t('wait.until', { time: until }) }}</span>
      <span class="wait-note">{{ wait.note }}</span>
    </DouText>
    <div class="wait-actions">
      <DouButton variant="ghost" size="sm" @click="endWait(conversationId, 'now')">
        {{ t('wait.now') }}
      </DouButton>
      <DouButton variant="ghost" size="sm" @click="endWait(conversationId, 'cancel')">
        {{ t('wait.cancel') }}
      </DouButton>
    </div>
  </div>
</template>

<style scoped>
.wait {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
}

.wait-text {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.wait-icon {
  flex: none;
}

.wait-until {
  flex: none;
}

.wait-note {
  min-width: 0;
  overflow: hidden;
  color: var(--dou-color-text-tertiary);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.wait-actions {
  display: flex;
  flex: none;
  gap: 4px;
}
</style>
