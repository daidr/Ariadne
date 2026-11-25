<script setup lang="ts">
import { DouBadge, DouButton, DouEmpty, DouLink, DouPopover, DouText } from 'dou-ui'
import { BellIcon, PlayIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'
import type { AttentionView, ThreadView } from '@ariadne/core'
import { threadLine } from '../stage'
import {
  endAside,
  openMain,
  openThread,
  openThreadFrom,
  retryTurn,
  state,
  threadsById,
} from '../store'
import AskBlock from '../blocks/AskBlock.vue'
import ReportArtifacts from './ReportArtifacts.vue'
import ReviewMark from '../review/ReviewMark.vue'

defineProps<{ compact?: boolean }>()

type ResolvedNeedsMeItem =
  | { kind: 'attention'; attention: AttentionView; askers: ThreadView[] }
  | { kind: 'thread'; thread: ThreadView }

// The server builds the list; the badge is its length, so the two cannot disagree.
const items = computed<ResolvedNeedsMeItem[]>(() => {
  const byId = threadsById.value
  const resolved: ResolvedNeedsMeItem[] = []
  for (const item of state.detail?.needsMe ?? []) {
    if (item.kind === 'attention') {
      const askers = [...(item.threadId ? [item.threadId] : []), ...item.joinedIds].flatMap(
        (id) => {
          const thread = byId.get(id)
          return thread ? [thread] : []
        },
      )
      resolved.push({ kind: 'attention', attention: item.attention, askers })
    } else {
      const thread = byId.get(item.threadId)
      if (thread) resolved.push({ kind: 'thread', thread })
    }
  }
  return resolved
})
const count = computed(() => state.detail?.needsMe.length ?? 0)

/** Go to where a form was asked: its thread, or the coordinator's conversation. */
function openAsker(threadId: string | null) {
  if (threadId) void openThread(threadId)
  else openMain({ kind: 'conversation' })
}

/** The line the thread list shows for it; its stage when its report said nothing. */
function headline(thread: ThreadView): string {
  if (thread.aside?.due) {
    return thread.aside.reason
      ? t('aside.dueReason', { reason: thread.aside.reason })
      : t('aside.due')
  }
  return threadLine(thread) ?? t(`thread.stage.${thread.stage}`)
}
</script>

<template>
  <DouPopover :title="t('attention.title')" :width="360" placement="bottom-end">
    <!-- With nothing waiting only the bell stays, so it takes no room from the header. -->
    <DouButton
      variant="ghost"
      size="sm"
      :aria-label="t('attention.title')"
      :title="count ? undefined : t('attention.title')"
    >
      <template #leading><BellIcon /></template>
      <template v-if="count" #default>
        <template v-if="!compact">{{ t('attention.title') }}</template>
        <DouBadge :value="count" tone="warning" />
      </template>
    </DouButton>
    <template #content>
      <div v-if="count" class="attention-list">
        <template v-for="item in items">
          <div v-if="item.kind === 'attention'" :key="item.attention.id" class="attention">
            <DouText v-if="item.askers.length" variant="secondary" size="sm" truncate>
              <template v-for="(thread, index) in item.askers" :key="thread.id">
                <template v-if="index"> · </template>
                <DouLink
                  underline="hover"
                  @click="(event: MouseEvent) => openThreadFrom(event, thread.id)"
                  >{{ thread.title }}</DouLink
                >
              </template>
            </DouText>
            <!-- The coordinator's own question says so, as a thread's carries its title. -->
            <DouText v-else variant="secondary" size="sm">{{ t('coordinator.title') }}</DouText>
            <!-- A form is filled in where it was asked; the list only points there. -->
            <div v-if="item.attention.block.type === 'form'" class="attention-thread">
              <DouText weight="medium" truncate>{{ item.attention.block.title }}</DouText>
              <DouButton size="sm" variant="ghost" @click="openAsker(item.attention.threadId)">
                {{ t('block.fill') }}
              </DouButton>
            </div>
            <AskBlock
              v-else
              :attention-id="item.attention.id"
              :block="item.attention.block"
              :answer="null"
            />
          </div>
          <div v-else :key="item.thread.id" class="attention">
            <div class="attention-thread">
              <!-- The title opens the thread, so a review can be looked at before it ends. -->
              <span class="attention-name">
                <DouLink
                  class="attention-title"
                  underline="hover"
                  :title="item.thread.title"
                  @click="(event: MouseEvent) => openThreadFrom(event, item.thread.id)"
                >
                  {{ item.thread.title }}
                </DouLink>
                <ReviewMark :thread="item.thread" />
              </span>
              <DouButton
                v-if="item.thread.stage === 'halted' || item.thread.stage === 'silent'"
                size="sm"
                variant="ghost"
                @click="retryTurn(item.thread.id)"
              >
                <template #leading><PlayIcon /></template>
                {{ t('conversation.continue') }}
              </DouButton>
              <!-- Back from being set aside: seen, and it goes. -->
              <DouButton
                v-else-if="item.thread.aside?.due"
                size="sm"
                variant="ghost"
                @click="endAside(item.thread.id)"
              >
                {{ t('aside.done') }}
              </DouButton>
              <DouButton
                v-else
                size="sm"
                variant="ghost"
                @click="(event: MouseEvent) => openThreadFrom(event, item.thread.id)"
              >
                {{ t('thread.open') }}
              </DouButton>
            </div>
            <DouText variant="secondary" size="sm" :truncate="2" :title="headline(item.thread)">
              {{ headline(item.thread) }}
            </DouText>
            <ReportArtifacts :artifacts="item.thread.lastArtifacts" />
          </div>
        </template>
      </div>
      <DouEmpty v-else size="sm" :description="t('attention.empty')" />
    </template>
  </DouPopover>
</template>

<style scoped>
.attention-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.attention {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.attention + .attention {
  padding-top: 12px;
  border-top: 1px solid var(--dou-color-border-subtle);
}

.attention-thread {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.attention-name {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.attention-title {
  min-width: 0;
  overflow: hidden;
  font-weight: 500;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
