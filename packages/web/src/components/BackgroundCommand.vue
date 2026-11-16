<script setup lang="ts">
import type { BackgroundView } from '@ariadne/core'
import { DouButton, DouCodeBlock, DouLink, DouText } from 'dou-ui'
import { SquareStopIcon } from '@dou-ui/icons'
import { computed, nextTick, onBeforeUnmount, ref, useTemplateRef, watch } from 'vue'
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
function onScroll() {
  const el = box.value
  if (el) pinned = el.scrollHeight - el.scrollTop - el.clientHeight < 24
}
watch(tail, async () => {
  await nextTick()
  const el = box.value
  if (el && pinned) el.scrollTop = el.scrollHeight
})
</script>

<template>
  <div class="background">
    <div class="background-head">
      <DouText size="sm" class="background-command" :title="command.command"
        >$ {{ command.command }}</DouText
      >
      <span class="background-time">{{ elapsed }}</span>
      <DouButton variant="ghost" size="sm" :loading="stopping" @click="stop">
        <template #leading><SquareStopIcon /></template>
        {{ t('command.stop') }}
      </DouButton>
    </div>
    <DouLink v-if="command.url" :href="command.url" external class="background-url">{{
      served
    }}</DouLink>
    <div ref="box" class="background-output" @scroll="onScroll">
      <DouCodeBlock :code="tail || t('command.noOutput')" wrap streaming />
    </div>
  </div>
</template>

<style scoped>
.background {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
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

.background-output {
  max-height: 240px;
  overflow: auto;
}
</style>
