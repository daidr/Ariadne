<script setup lang="ts">
import { DouBadge, DouButton, DouEmpty, DouPopover, DouText } from 'dou-ui'
import { BellIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'
import type { NeedsMeItem, ThreadView } from '@ariadne/core'
import { openThread, state } from '../store'
import AskBlock from '../blocks/AskBlock.vue'
import ReportArtifacts from './ReportArtifacts.vue'

// The server builds the list; the badge is its length, so the two cannot disagree.
const items = computed(() => state.detail?.needsMe ?? [])
const count = computed(() => items.value.length)

/** Its stage, and what its last report asks of the user. */
function headline(thread: ThreadView): string {
  const stage = t(`thread.stage.${thread.stage}`)
  return thread.lastHeadline ? `${stage} · ${thread.lastHeadline}` : stage
}

/** Every thread waiting on a card, so the user sees whom one answer reaches. */
function askers(item: Extract<NeedsMeItem, { kind: 'attention' }>): string[] {
  return [...(item.thread ? [item.thread] : []), ...item.joined].map((thread) => thread.title)
}
</script>

<template>
  <DouPopover :title="t('attention.title')" :width="360" placement="bottom-end">
    <DouButton variant="ghost" size="sm">
      <template #leading><BellIcon /></template>
      {{ t('attention.title') }}
      <DouBadge v-if="count" :value="count" tone="warning" />
    </DouButton>
    <template #content>
      <div v-if="count" class="attention-list">
        <template v-for="item in items">
          <div v-if="item.kind === 'attention'" :key="item.attention.id" class="attention">
            <DouText v-if="askers(item).length" variant="secondary" size="sm" truncate>
              {{ askers(item).join(' · ') }}
            </DouText>
            <!-- A form is filled in where it was asked; the list only points there. -->
            <div v-if="item.attention.block.type === 'form'" class="attention-thread">
              <DouText weight="medium" truncate>{{ item.attention.block.title }}</DouText>
              <DouButton size="sm" variant="ghost" @click="openThread(item.attention.threadId)">
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
              <DouText weight="medium" truncate>{{ item.thread.title }}</DouText>
              <DouButton size="sm" variant="ghost" @click="openThread(item.thread.id)">
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

.attention-thread {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
</style>
