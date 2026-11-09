<script setup lang="ts">
import type { BackgroundView } from '@ariadne/core'
import { DouButton, DouCodeBlock, DouLink, DouPopover, DouStatusDot, DouText } from 'dou-ui'
import { SquareStopIcon } from '@dou-ui/icons'
import { computed, nextTick, onBeforeUnmount, ref, useTemplateRef, watch } from 'vue'
import { api } from '../api'
import { t } from '../i18n'
import { report, stopBackground } from '../store'
import { elapsedText, firstLine } from './command-text'

/**
 * A command the thread left running in the background (a dev server, a watcher), in its
 * status line: what it is and for how long, and the address it serves at when it named one
 * (a dev server), to open the thread's work in the browser. Opened, it shows the end of its
 * output, read again every second while open, and can be stopped.
 */
const props = defineProps<{ threadId: string; command: BackgroundView }>()

const now = ref(Date.now())
const clock = setInterval(() => (now.value = Date.now()), 1000)
const elapsed = computed(() => elapsedText(now.value - props.command.startedAt))

// The address without its scheme and a bare trailing slash: `localhost:5173`.
const served = computed(() => props.command.url?.replace(/^https?:\/\//, '').replace(/\/$/, ''))

const open = ref(false)
const tail = ref('')
const stopping = ref(false)
let poll: ReturnType<typeof setInterval> | undefined
async function read() {
  try {
    tail.value = (await api.commandOutput(props.threadId, props.command.id)).tail
  } catch (error) {
    report(error)
    clearInterval(poll)
  }
}
watch(open, (isOpen) => {
  clearInterval(poll)
  if (!isOpen) return
  void read()
  poll = setInterval(read, 1000)
})
onBeforeUnmount(() => {
  clearInterval(clock)
  clearInterval(poll)
})

async function stop() {
  stopping.value = true
  await stopBackground(props.threadId, props.command.id)
  stopping.value = false
  open.value = false
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
  <span class="background">
    <DouPopover v-model:open="open" trigger="click" placement="top-start" :width="560">
      <DouText
        variant="secondary"
        size="sm"
        class="background-trigger"
        role="button"
        tabindex="0"
        :aria-expanded="open"
        @keydown.enter.prevent="open = !open"
      >
        <DouStatusDot tone="success" />
        <span class="background-text">
          {{ t('command.background', { command: firstLine(command.command) }) }}
        </span>
        <span class="background-time">{{ elapsed }}</span>
      </DouText>
      <template #content>
        <div class="background-head">
          <DouText size="sm" class="background-command" :title="command.command">
            $ {{ command.command }}
          </DouText>
          <DouButton variant="ghost" size="sm" :loading="stopping" @click="stop">
            <template #leading><SquareStopIcon /></template>
            {{ t('command.stop') }}
          </DouButton>
        </div>
        <div ref="box" class="background-output" @scroll="onScroll">
          <DouCodeBlock :code="tail || t('command.noOutput')" wrap streaming />
        </div>
      </template>
    </DouPopover>
    <DouLink v-if="command.url" :href="command.url" external class="background-url">
      {{ served }}
    </DouLink>
  </span>
</template>

<style scoped>
.background {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.background-url {
  flex: none;
  font-size: var(--dou-font-size-sm);
}

.background-trigger {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  cursor: pointer;
}

.background-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
  margin-bottom: 8px;
}

.background-command {
  min-width: 0;
  overflow: hidden;
  font-family: var(--dou-font-family-mono);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.background-output {
  max-height: 360px;
  overflow: auto;
}
</style>
