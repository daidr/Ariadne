<script setup lang="ts">
import type { BackgroundView } from '@ariadne/core'
import { DouButton, DouCodeBlock, DouLink, DouText } from 'dou-ui'
import { StopIcon } from '@dou-ui/icons'
import { computed, onBeforeUnmount, ref, useTemplateRef, watch } from 'vue'
import { api } from '../api'
import { t } from '../i18n'
import { report, stopBackground } from '../store'
import { elapsedText } from './command-text'

/**
 * A command the thread left running in the background (a dev server, a watcher), in its
 * task popover: what it is and for how long, and the address it serves at when it named one
 * (a dev server), to open the thread's work in the browser. Opened, it shows the end of its
 * output, read again every second while open, and can be stopped.
 */
const props = defineProps<{ threadId: string; command: BackgroundView }>()

const now = ref(Date.now())
const clock = setInterval(() => (now.value = Date.now()), 1000)
const elapsed = computed(() => elapsedText(now.value - props.command.startedAt))

// The address without its scheme and a bare trailing slash: `localhost:5173`.
const served = computed(() => props.command.url?.replace(/^https?:\/\//, '').replace(/\/$/, ''))

const tail = ref('')
const stopping = ref(false)
let poll: ReturnType<typeof setInterval> | undefined
const controller = new AbortController()
async function read() {
  try {
    const result = await api.commandOutput(props.threadId, props.command.id, controller.signal)
    if (!controller.signal.aborted) tail.value = result.tail
  } catch (error) {
    if (!controller.signal.aborted) report(error)
    return
  }
  if (!controller.signal.aborted) poll = setTimeout(read, 1000)
}
void read()
onBeforeUnmount(() => {
  clearInterval(clock)
  clearTimeout(poll)
  controller.abort()
})

async function stop() {
  stopping.value = true
  try {
    await stopBackground(props.threadId, props.command.id)
  } finally {
    stopping.value = false
  }
}

// The newest output stays in view, unless the user scrolled up to read.
const box = useTemplateRef<HTMLElement>('box')
let pinned = true
function onScroll(event: Event) {
  const el = event.target
  if (el instanceof HTMLElement && el.matches('.dou-code-block__body')) {
    pinned = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  }
}
watch(
  tail,
  () => {
    const el = box.value?.querySelector<HTMLElement>('.dou-code-block__body')
    if (el && pinned) el.scrollTop = el.scrollHeight
  },
  { flush: 'post' },
)
</script>

<template>
  <div class="background">
    <div class="background-head">
      <DouText size="sm" class="background-command" :title="command.command"
        >$ {{ command.command }}</DouText
      >
      <span class="background-time">{{ elapsed }}</span>
      <DouButton
        variant="ghost"
        tone="danger"
        size="sm"
        :aria-label="t('command.stop')"
        :title="t('command.stop')"
        :loading="stopping"
        @click="stop"
      >
        <StopIcon />
      </DouButton>
    </div>
    <DouLink v-if="command.url" :href="command.url" external class="background-url">{{
      served
    }}</DouLink>
    <div ref="box" class="background-output" @scroll.capture="onScroll">
      <DouCodeBlock v-if="tail" :code="tail" wrap streaming />
      <DouText v-else variant="tertiary" size="sm" class="background-empty">{{
        t('command.noOutput')
      }}</DouText>
    </div>
  </div>
</template>

<style scoped>
.background {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
  min-height: 0;
}

.background-url {
  flex: none;
  font-size: var(--dou-font-size-sm);
}

.background-time {
  flex: none;
  color: var(--dou-color-text-tertiary);
  font-variant-numeric: tabular-nums;
}

.background-head {
  display: flex;
  flex: none;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.background-command {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  font-family: var(--dou-font-family-mono);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.background-output,
.background-output :deep(.dou-code-block) {
  display: flex;
  flex-direction: column;
  min-height: 0;
}

.background-output :deep(.dou-code-block__body) {
  box-sizing: border-box;
  max-height: 240px;
  overflow-y: auto;
  overscroll-behavior-y: contain;
  scrollbar-width: thin;
}

.background-output :deep(.dou-code-block__copy) {
  right: var(--dou-code-block-padding-x);
}

.background-empty {
  display: block;
  padding: 12px 0;
}
</style>
