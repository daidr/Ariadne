<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouChatInput,
  DouChatList,
  DouChatMessage,
  DouCollapse,
  DouCollapseItem,
  DouEmpty,
  DouTag,
  DouText,
} from 'dou-ui'
import type { MessageView } from '@ariadne/core'
import { ChevronLeftIcon, GitBranchIcon, RotateCwIcon, SquareStopIcon } from '@dou-ui/icons'
import { computed, ref, watch } from 'vue'
import { api } from '../api'
import { t, tm } from '../i18n'
import { stageTone } from '../stage'
import {
  currentConversationId,
  openThread,
  retryTurn,
  sendMessage,
  state,
  stopTurn,
} from '../store'

const emit = defineEmits<{ openSettings: [] }>()

const draft = ref('')

const thread = computed(() =>
  state.threadId
    ? (state.detail?.threads.find((item) => item.id === state.threadId) ?? null)
    : null,
)
const working = computed(() => !!state.conversation?.turn)
// New input is waiting for a free slot.
const queued = computed(() => !working.value && (state.conversation?.pendingSeq ?? 0) > 0)
const failure = computed(() =>
  state.conversation?.lastOutcome === 'failed' && !working.value
    ? state.conversation.lastError
    : null,
)
const cancelled = computed(() => state.conversation?.lastOutcome === 'cancelled' && !working.value)

// Messages edited after the user read them stay highlighted for this visit, even though
// opening the conversation marks them read on the server.
const highlighted = ref(new Set<string>())
watch(
  () => state.messages,
  (messages) => {
    for (const message of messages) {
      if (message.editedSinceRead) highlighted.value.add(message.id)
    }
  },
  { immediate: true },
)
watch(
  () => [state.currentId, state.threadId],
  () => (highlighted.value = new Set()),
)

function sender(message: MessageView) {
  return message.author === 'user' ? 'user' : 'assistant'
}

async function submit(value: string) {
  draft.value = ''
  await sendMessage(value)
}

// Everything on screen counts as read; this also clears the "edited since read" highlight.
watch(
  () => state.messages.at(-1)?.id,
  (last) => {
    const conversation = currentConversationId()
    if (last && conversation) void api.markRead(conversation, last)
  },
)
</script>

<template>
  <div class="conversation">
    <div v-if="thread" class="conversation-header">
      <DouButton variant="ghost" size="sm" :title="t('thread.back')" @click="openThread(null)">
        <template #leading><ChevronLeftIcon /></template>
        {{ t('thread.back') }}
      </DouButton>
      <DouText weight="medium" truncate>{{ thread.title }}</DouText>
      <DouTag size="sm" :tone="stageTone[thread.stage]">
        {{ t(`thread.stage.${thread.stage}`) }}
      </DouTag>
      <DouTag
        v-if="thread.workspace?.branch"
        size="sm"
        :title="`${t('thread.workspace')}: ${thread.workspace.label}`"
      >
        <template #leading><GitBranchIcon /></template>
        {{ thread.workspace.branch }}
      </DouTag>
    </div>

    <DouChatList v-if="state.messages.length || working" class="conversation-list">
      <template v-for="message in state.messages" :key="message.id">
        <!-- What the coordinator told a thread: there for whoever wants it, out of the way otherwise. -->
        <DouCollapse v-if="message.kind === 'relay'" class="relay">
          <DouCollapseItem :value="message.id" :title="t('message.fromCoordinator')">
            <DouText size="sm" class="relay-content">{{ message.content }}</DouText>
          </DouCollapseItem>
        </DouCollapse>
        <DouChatMessage
          v-else
          :sender="sender(message)"
          :content="message.content"
          :class="{ 'conversation-edited': highlighted.has(message.id) }"
        >
          <template v-if="message.editedAt" #actions>
            <DouText variant="tertiary" size="sm" :title="message.editReason ?? undefined">
              {{ t('message.edited') }}
            </DouText>
          </template>
        </DouChatMessage>
      </template>
      <DouChatMessage v-if="working" sender="assistant" loading />
    </DouChatList>
    <div v-else class="conversation-empty">
      <DouEmpty v-if="thread" :title="thread.title" :description="t('thread.empty')" />
      <DouEmpty v-else :title="t('coordinator.title')" :description="t('coordinator.empty')" />
    </div>

    <div v-if="working" class="conversation-status" role="status">
      <DouText variant="secondary" size="sm" truncate>
        {{ t('conversation.working') }} · {{ state.conversation?.turn?.model }}
      </DouText>
      <DouButton variant="ghost" size="sm" @click="stopTurn">
        <template #leading><SquareStopIcon /></template>
        {{ t('conversation.stop') }}
      </DouButton>
    </div>
    <div v-else-if="queued" class="conversation-status" role="status">
      <DouText variant="secondary" size="sm">{{ t('conversation.queued') }}</DouText>
    </div>
    <DouAlert v-else-if="failure" tone="danger" class="conversation-alert">
      {{ tm(failure) }}
      <template #action>
        <DouButton v-if="failure.key === 'error.noModel'" size="sm" @click="emit('openSettings')">
          {{ t('conversation.openSettings') }}
        </DouButton>
        <DouButton size="sm" variant="outline" @click="retryTurn">
          <template #leading><RotateCwIcon /></template>
          {{ t('conversation.retry') }}
        </DouButton>
      </template>
    </DouAlert>
    <DouAlert v-else-if="cancelled" tone="neutral" class="conversation-alert">
      {{ t('conversation.cancelled') }}
      <template #action>
        <DouButton size="sm" variant="outline" @click="retryTurn">
          <template #leading><RotateCwIcon /></template>
          {{ t('conversation.retry') }}
        </DouButton>
      </template>
    </DouAlert>

    <div class="conversation-input">
      <DouChatInput
        v-model="draft"
        :placeholder="thread ? t('thread.placeholder') : t('coordinator.placeholder')"
        :disabled="!state.detail"
        @submit="submit"
      />
    </div>
  </div>
</template>

<style scoped>
.conversation {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.conversation-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 16px;
  border-bottom: 1px solid var(--dou-color-border);
}

.conversation-list {
  flex: 1;
  min-height: 0;
  padding: 16px;
}

.conversation-empty {
  flex: 1;
  display: grid;
  place-items: center;
}

.conversation-alert {
  margin: 0 16px;
}

.conversation-status {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin: 0 16px;
}

.conversation-input {
  padding: 12px 16px 16px;
}

.conversation-edited {
  outline: 1px solid var(--dou-color-warning);
  outline-offset: 4px;
  border-radius: var(--dou-radius-container);
}

/* Faded: the user can read it, but it does not compete with the conversation. */
.relay {
  opacity: 0.7;
}

.relay-content {
  white-space: pre-wrap;
}
</style>
