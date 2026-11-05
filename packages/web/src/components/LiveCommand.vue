<script setup lang="ts">
import type { LiveCommand } from '@ariadne/core'
import { DouCodeBlock, DouPopover, DouShimmer, DouStatusDot, DouText } from 'dou-ui'
import { computed, nextTick, onBeforeUnmount, ref, useTemplateRef, watch } from 'vue'
import { t } from '../i18n'

/**
 * The command a conversation is running, in its status line: what it is and for how long.
 * Opened, it shows the end of the output as it comes; the whole of it is in the trace once
 * the command ends.
 */
const props = defineProps<{ command: LiveCommand }>()

const now = ref(Date.now())
const timer = setInterval(() => (now.value = Date.now()), 1000)
onBeforeUnmount(() => clearInterval(timer))

/** `m:ss`, or `h:mm:ss` past an hour. */
const elapsed = computed(() => {
  const seconds = Math.max(0, Math.floor((now.value - props.command.startedAt) / 1000))
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = String(seconds % 60).padStart(2, '0')
  return h ? `${h}:${String(m).padStart(2, '0')}:${s}` : `${m}:${s}`
})
/** Its first line, cut short: the status line keeps room for the stop button. */
const MAX_SHOWN = 48
const firstLine = computed(() => {
  const line = props.command.command.split('\n', 1)[0]!.trim()
  return line.length > MAX_SHOWN ? `${line.slice(0, MAX_SHOWN)}…` : line
})

// The newest output stays in view, unless the user scrolled up to read.
const open = ref(false)
const box = useTemplateRef<HTMLElement>('box')
let pinned = true
function onScroll() {
  const el = box.value
  if (el) pinned = el.scrollHeight - el.scrollTop - el.clientHeight < 24
}
watch(
  () => [props.command.tail, open.value],
  async () => {
    await nextTick()
    const el = box.value
    if (el && pinned) el.scrollTop = el.scrollHeight
  },
)
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
      <DouShimmer class="live-text">{{ t('command.running', { command: firstLine }) }}</DouShimmer>
      <span class="live-time">{{ elapsed }}</span>
    </DouText>
    <template #content>
      <DouText size="sm" class="live-command" :title="command.command">
        {{ command.cwd === '.' ? '' : `${command.cwd} ` }}$ {{ command.command }}
      </DouText>
      <div ref="box" class="live-output" @scroll="onScroll">
        <DouCodeBlock :code="command.tail || t('command.noOutput')" wrap streaming />
      </div>
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

.live-output {
  max-height: 360px;
  overflow: auto;
}
</style>
