<script setup lang="ts">
import type { NoticeView, ThreadView } from '@ariadne/core'
import { DouButton, DouMenu, DouText, type MenuOption } from 'dou-ui'
import { ChevronDownIcon, ChevronRightIcon, HelpCircleIcon } from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { t } from '../i18n'
import { openMain, openMessage, openThread, state } from '../store'
import ThreadRow from './ThreadRow.vue'

/**
 * What came of the work while the user was away, folded into one line where they talk to
 * the coordinator: threads that finished, were merged, or wait on them. Rows are the
 * threads as they stand now, so what was dealt with since drops out of "waiting".
 */
const props = defineProps<{ notice: Extract<NoticeView, { kind: 'recap' }> }>()

const open = ref(false)

const byId = computed(() => new Map((state.detail?.threads ?? []).map((item) => [item.id, item])))
const threadsOf = (ids: string[]) => ids.flatMap((id) => byId.value.get(id) ?? [])

// Still waiting now: the thread is on the "needs me" list, alone or behind a card.
const needing = computed(() => {
  const ids = new Set<string>()
  for (const item of state.detail?.needsMe ?? []) {
    if (item.kind === 'thread') ids.add(item.thread.id)
    else for (const thread of [item.thread, ...item.joined]) if (thread) ids.add(thread.id)
  }
  return ids
})
const waiting = computed(() =>
  threadsOf(props.notice.waiting).filter((thread) => needing.value.has(thread.id)),
)
const asks = computed(() =>
  (state.detail?.needsMe ?? []).flatMap((item) =>
    item.kind === 'attention' && props.notice.asks.includes(item.attention.id)
      ? [item.attention]
      : [],
  ),
)
const merged = computed(() => threadsOf(props.notice.merged))
const done = computed(() => threadsOf(props.notice.done))

function span(ms: number): string {
  const minutes = Math.round(ms / 60_000)
  if (minutes < 60) return t('recap.minutes', { count: minutes })
  const hours = Math.round(minutes / 60)
  if (hours < 48) return t('recap.hours', { count: hours })
  return t('recap.days', { count: Math.round(hours / 24) })
}

const title = computed(() => {
  const parts = [
    ...(done.value.length ? [t('recap.done', { count: done.value.length })] : []),
    ...(merged.value.length ? [t('recap.merged', { count: merged.value.length })] : []),
    ...(waiting.value.length + asks.value.length
      ? [t('recap.waiting', { count: waiting.value.length + asks.value.length })]
      : []),
  ]
  if (!parts.length) return null
  return t('recap.title', {
    time: span(props.notice.createdAt - props.notice.since),
    parts: parts.join(t('recap.join')),
  })
})

const ASK = 'ask:'
const items = computed<MenuOption[]>(() => [
  ...waiting.value.map((thread) => ({ key: thread.id, label: thread.title })),
  ...asks.value.map((ask) => ({
    key: `${ASK}${ask.id}`,
    label: ask.block.type === 'choice' ? ask.block.question : ask.block.title,
  })),
  ...[...merged.value, ...done.value].map((thread: ThreadView) => ({
    key: thread.id,
    label: thread.title,
  })),
])

function select(key: string | number) {
  const id = String(key)
  if (!id.startsWith(ASK)) return void openThread(id)
  const ask = asks.value.find((item) => item.id === id.slice(ASK.length))
  // The coordinator's question is in its message; the system's own are about the plan.
  if (ask?.messageId && ask.conversationId) void openMessage(ask.conversationId, ask.messageId)
  else openMain({ kind: 'plan' })
}
</script>

<template>
  <div v-if="title" class="recap">
    <DouButton
      size="sm"
      variant="ghost"
      class="recap-toggle"
      :aria-expanded="open"
      @click="open = !open"
    >
      {{ title }}
      <template #trailing>
        <ChevronDownIcon v-if="open" />
        <ChevronRightIcon v-else />
      </template>
    </DouButton>
    <DouMenu v-if="open" class="recap-menu" :aria-label="title" :items="items" @select="select">
      <template #label="{ item }">
        <ThreadRow v-if="byId.get(String(item.key))" :thread="byId.get(String(item.key))!" />
        <span v-else class="recap-ask">
          <DouText tone="warning" class="recap-ask-icon">
            <HelpCircleIcon aria-hidden="true" />
          </DouText>
          <DouText truncate class="recap-ask-title">{{ item.label }}</DouText>
        </span>
      </template>
    </DouMenu>
  </div>
</template>

<style scoped>
.recap {
  display: flex;
  flex: 1;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
  min-width: 0;
}

/* One quiet line the size of its text, as the thread list names its groups. */
.recap-toggle {
  --dou-button-fg: var(--dou-color-text-tertiary);
  margin-inline-start: -8px;
}

/* Rows grow for the activity line. */
.recap-menu {
  align-self: stretch;
  --dou-menu-item-height: auto;
}

.recap-ask {
  display: flex;
  flex: 1;
  align-items: center;
  gap: 8px;
  min-width: 0;
  padding: 8px 0;
  line-height: normal;
}

.recap-ask-icon {
  display: inline-flex;
  flex: none;
  font-size: 16px;
}

.recap-ask-title {
  flex: 1;
  min-width: 0;
  text-box: trim-both cap alphabetic;
}
</style>
