<script setup lang="ts">
import type { LiveCommand } from '@ariadne/core'
import { DouCodeBlock, DouPopover, DouShimmer, DouStatusDot, DouText } from 'dou-ui'
import { computed, onBeforeUnmount, ref } from 'vue'
import { t } from '../i18n'
import { elapsedText, firstLine } from './command-text'

/**
 * The command a conversation is running, in its status line: what it is and for how long.
 * Opened, it shows the end of the output as it comes; the whole of it is in the trace once
 * the command ends.
 */
const props = defineProps<{ command: LiveCommand }>()

const now = ref(Date.now())
const timer = setInterval(() => (now.value = Date.now()), 1000)
onBeforeUnmount(() => clearInterval(timer))

const elapsed = computed(() => elapsedText(now.value - props.command.startedAt))
const shown = computed(() => firstLine(props.command.command))

const open = ref(false)
</script>

<template>
  <DouPopover v-model:open="open" trigger="click" placement="top-start" :width="560">
    <DouText
      variant="secondary"
      size="sm"
      class="live-trigger"
      role="button"
      tabindex="0"
      :aria-expanded="open"
      @keydown.enter.prevent="open = !open"
    >
      <DouStatusDot tone="primary" ripple />
      <DouShimmer class="live-text">{{ t('command.running', { command: shown }) }}</DouShimmer>
      <span class="live-time">{{ elapsed }}</span>
    </DouText>
    <template #content>
      <DouText size="sm" class="live-command" :title="command.command">
        {{ command.cwd === '.' ? '' : `${command.cwd} ` }}$ {{ command.command }}
      </DouText>
      <DouCodeBlock v-if="command.tail" :code="command.tail" wrap streaming :max-height="360" />
      <DouText v-else variant="tertiary" size="sm">{{ t('command.noOutput') }}</DouText>
    </template>
  </DouPopover>
</template>

<style scoped>
.live-trigger {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  cursor: pointer;
}

.live-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.live-time {
  flex: none;
  color: var(--dou-color-text-tertiary);
  font-variant-numeric: tabular-nums;
}

.live-command {
  display: block;
  margin-bottom: 8px;
  overflow: hidden;
  font-family: var(--dou-font-family-mono);
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
