<script setup lang="ts">
import { DouChatInput, DouChatList, DouChatMessage, DouEmpty, DouText } from 'dou-ui'
import type { MessageView } from '@ariadne/core'
import { ref, watch } from 'vue'
import { api } from '../api'
import { t } from '../i18n'
import { sendToCoordinator, state } from '../store'

const draft = ref('')

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
  () => state.currentId,
  () => (highlighted.value = new Set()),
)

function sender(message: MessageView) {
  return message.author === 'user' ? 'user' : 'assistant'
}

async function submit(value: string) {
  draft.value = ''
  await sendToCoordinator(value)
}

// Everything on screen counts as read; this also clears the "edited since read" highlight.
watch(
  () => state.messages.at(-1)?.id,
  (last) => {
    const conversation = state.detail?.project.coordinatorId
    if (last && conversation) void api.markRead(conversation, last)
  },
)
</script>

<template>
  <div class="chat">
    <DouChatList v-if="state.messages.length" class="chat-list">
      <DouChatMessage
        v-for="message in state.messages"
        :key="message.id"
        :sender="sender(message)"
        :content="message.content"
        :class="{ 'chat-edited': highlighted.has(message.id) }"
      >
        <template v-if="message.editedAt" #actions>
          <DouText variant="tertiary" size="sm" :title="message.editReason ?? undefined">
            {{ t('message.edited') }}
          </DouText>
        </template>
      </DouChatMessage>
    </DouChatList>
    <div v-else class="chat-empty">
      <DouEmpty :title="t('coordinator.title')" :description="t('coordinator.empty')" />
    </div>
    <div class="chat-input">
      <DouChatInput
        v-model="draft"
        :placeholder="t('coordinator.placeholder')"
        :disabled="!state.detail"
        @submit="submit"
      />
    </div>
  </div>
</template>

<style scoped>
.chat {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.chat-list {
  flex: 1;
  min-height: 0;
  padding: 16px;
}

.chat-empty {
  flex: 1;
  display: grid;
  place-items: center;
}

.chat-input {
  padding: 12px 16px 16px;
}

.chat-edited {
  outline: 1px solid var(--dou-color-warning);
  outline-offset: 4px;
  border-radius: var(--dou-radius-container);
}
</style>
