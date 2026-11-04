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
  DouInput,
  DouLink,
  DouPopover,
  DouTag,
  DouText,
} from 'dou-ui'
import type { MessageView, NoticeView } from '@ariadne/core'
import {
  ChevronLeftIcon,
  GitBranchIcon,
  PauseIcon,
  PlayIcon,
  RotateCwIcon,
  SquareStopIcon,
} from '@dou-ui/icons'
import { computed, ref, watch } from 'vue'
import { api } from '../api'
import { t, tm } from '../i18n'
import { stageTone } from '../stage'
import GettingStarted from './GettingStarted.vue'
import PullRequestTag from './PullRequestTag.vue'
import {
  currentConversationId,
  openThread,
  retryTurn,
  sendMessage,
  state,
  resumeThread,
  stopThread,
  stopTurn,
  undoNotice,
} from '../store'

const emit = defineEmits<{ openSettings: [] }>()

const draft = ref('')

const thread = computed(() =>
  state.threadId
    ? (state.detail?.threads.find((item) => item.id === state.threadId) ?? null)
    : null,
)
const working = computed(() => !!state.conversation?.turn)
// Set up enough to talk: a model, a project, and its working directory.
const started = computed(
  () => state.modelReady !== false && !!state.detail && !!state.detail.project.source,
)
// New input is waiting for a free slot.
const queued = computed(() => !working.value && (state.conversation?.pendingSeq ?? 0) > 0)
const failure = computed(() =>
  state.conversation?.lastOutcome === 'failed' && !working.value
    ? state.conversation.lastError
    : null,
)
// The user stops a thread with a reason, the same way the coordinator does.
const stopOpen = ref(false)
const stopReason = ref('')
async function stopThreadNow(threadId: string, takeover: boolean) {
  // Taking over without a reason keeps the coordinator's, which says why it stopped.
  const reason = stopReason.value.trim() || (takeover ? '' : t('thread.stopDefaultReason'))
  await stopThread(threadId, reason)
  stopOpen.value = false
  stopReason.value = ''
}
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

// Messages and notices in the order they happened.
const timeline = computed(() =>
  [
    ...state.messages.map((message) => ({
      id: message.id,
      at: message.createdAt,
      message,
      notice: null,
    })),
    ...state.notices.map((notice) => ({
      id: notice.id,
      at: notice.createdAt,
      message: null,
      notice,
    })),
  ].toSorted((a, b) => a.at - b.at),
)

function issueNotice(notice: Extract<NoticeView, { kind: 'issue-written' }>) {
  const issue = `${notice.tracker}#${notice.number}`
  if (notice.action === 'comment') return t('notice.issueComment', { issue })
  if (notice.action === 'reopen') return t('notice.issueReopen', { issue })
  return t('notice.issueClose', {
    issue,
    reason: t(
      notice.text === 'not_planned' ? 'tracker.reason.not_planned' : 'tracker.reason.completed',
    ),
  })
}

function memoryNotice(notice: Extract<NoticeView, { kind: 'memory' }>) {
  if (notice.action === 'removed') return t('notice.memoryRemoved', { text: notice.text })
  if (notice.previous === null) return t('notice.memoryAdded', { text: notice.text })
  return t('notice.memoryChanged', { previous: notice.previous, text: notice.text })
}

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
      <PullRequestTag :thread="thread" refreshable />
      <DouPopover
        v-if="thread.stopped ? thread.stopped.by !== 'user' : thread.stage !== 'done'"
        v-model:open="stopOpen"
        trigger="click"
        :title="t(thread.stopped ? 'thread.takeOverTitle' : 'thread.stopTitle')"
        :width="320"
      >
        <DouButton variant="ghost" size="sm" class="conversation-stop-thread">
          <template #leading><PauseIcon /></template>
          {{ t(thread.stopped ? 'thread.takeOver' : 'thread.stop') }}
        </DouButton>
        <template #content>
          <div class="stop-form">
            <DouInput v-model="stopReason" :placeholder="t('thread.stopReasonPlaceholder')" />
            <DouButton size="sm" tone="primary" @click="stopThreadNow(thread.id, !!thread.stopped)">
              {{ t('thread.stopConfirm') }}
            </DouButton>
          </div>
        </template>
      </DouPopover>
    </div>

    <DouChatList v-if="timeline.length || working" class="conversation-list">
      <template v-for="item in timeline" :key="item.id">
        <div v-if="item.notice" class="notice">
          <template v-if="item.notice.kind === 'memory'">
            <DouText variant="tertiary" size="sm" class="notice-text">
              {{ memoryNotice(item.notice) }}
            </DouText>
            <DouText v-if="item.notice.undone" variant="tertiary" size="sm">
              {{ t('notice.undone') }}
            </DouText>
            <DouButton v-else size="sm" variant="ghost" @click="undoNotice(item.notice.id)">
              {{ t('notice.undo') }}
            </DouButton>
          </template>
          <DouCollapse v-else-if="item.notice.kind === 'compacted'" class="relay">
            <DouCollapseItem :value="item.notice.id" :title="t('notice.compacted')">
              <DouText size="sm" class="relay-content">{{ item.notice.summary }}</DouText>
            </DouCollapseItem>
          </DouCollapse>
          <!-- What the thread follows from the repository, and when it moves. -->
          <DouText
            v-else-if="item.notice.kind === 'instructions' && item.notice.content === null"
            variant="tertiary"
            size="sm"
            class="notice-text"
          >
            {{ t('notice.instructionsRemoved', { path: item.notice.path }) }}
          </DouText>
          <DouCollapse v-else-if="item.notice.kind === 'instructions'" class="relay">
            <DouCollapseItem
              :value="item.notice.id"
              :title="
                t(item.notice.changed ? 'notice.instructionsChanged' : 'notice.instructions', {
                  path: item.notice.path,
                }) + (item.notice.uncommitted ? t('notice.instructionsUncommitted') : '')
              "
            >
              <DouText size="sm" class="relay-content">{{ item.notice.content }}</DouText>
            </DouCollapseItem>
          </DouCollapse>
          <!-- A change made to an issue in the user's name, with a link to see it there. -->
          <DouText
            v-else-if="item.notice.kind === 'issue-written'"
            variant="tertiary"
            size="sm"
            class="notice-text"
          >
            {{ issueNotice(item.notice) }}
            <DouLink v-if="item.notice.url" :href="item.notice.url" external>
              {{ t('notice.issueOpen') }}
            </DouLink>
          </DouText>
          <DouText
            v-else-if="item.notice.kind === 'model-retry'"
            variant="tertiary"
            size="sm"
            class="notice-text"
          >
            {{
              t('notice.modelRetry', {
                attempt: item.notice.attempt,
                seconds: Math.max(1, Math.round(item.notice.delayMs / 1000)),
                detail: item.notice.detail,
              })
            }}
          </DouText>
          <DouText v-else tone="warning" size="sm" class="notice-text">
            {{ t('notice.compactionFailed', { detail: item.notice.detail }) }}
          </DouText>
        </div>
        <template v-for="message in item.message ? [item.message] : []" v-else :key="message.id">
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
              <!-- Why it changed and what it said before, for whoever wants to check. -->
              <DouPopover trigger="click" :title="t('message.editedTitle')" :width="360">
                <DouButton variant="ghost" size="sm">{{ t('message.edited') }}</DouButton>
                <template #content>
                  <div class="edit-details">
                    <DouText v-if="message.editReason" size="sm">
                      {{ t('message.editReason') }}{{ message.editReason }}
                    </DouText>
                    <template v-if="message.originalContent !== null">
                      <DouText variant="tertiary" size="sm">{{ t('message.original') }}</DouText>
                      <DouText size="sm" class="edit-original">{{
                        message.originalContent
                      }}</DouText>
                    </template>
                  </div>
                </template>
              </DouPopover>
            </template>
          </DouChatMessage>
        </template>
      </template>
      <DouChatMessage v-if="working" sender="assistant" loading />
    </DouChatList>
    <div v-else class="conversation-empty">
      <DouEmpty v-if="thread" :title="thread.title" :description="t('thread.empty')" />
      <GettingStarted v-else-if="!started" @open-settings="emit('openSettings')" />
      <DouEmpty v-else :title="t('coordinator.title')" :description="t('coordinator.empty')" />
    </div>

    <!-- A stopped thread is not scheduled, so its reason and 继续 win over "queued". -->
    <DouAlert v-if="thread?.stopped" tone="warning" class="conversation-alert">
      {{
        t(thread.stopped.by === 'user' ? 'thread.stoppedByUser' : 'thread.stoppedByCoordinator', {
          reason: thread.stopped.reason,
        })
      }}
      <template #action>
        <DouButton size="sm" variant="outline" @click="resumeThread(thread.id)">
          <template #leading><PlayIcon /></template>
          {{ t('thread.resume') }}
        </DouButton>
      </template>
    </DouAlert>
    <div v-else-if="working" class="conversation-status" role="status">
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
.notice {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 0;
}

.notice-text {
  overflow-wrap: anywhere;
}

.edit-details {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.edit-original {
  max-height: 240px;
  overflow: auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

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

.conversation-stop-thread {
  margin-left: auto;
}

.stop-form {
  display: flex;
  flex-direction: column;
  gap: 8px;
  align-items: flex-end;
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
