<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouChatAttachments,
  DouChatInput,
  DouChatContextMeter,
  DouChatPlan,
  DouChatList,
  DouChatMessage,
  DouCollapse,
  DouCollapseItem,
  DouDropdown,
  DouDropdownItem,
  DouDropdownRadioGroup,
  DouEmpty,
  DouImagePreview,
  DouInput,
  DouLink,
  DouPopover,
  DouTag,
  DouText,
  type ChatAttachment,
  type ChatMessageProps,
} from 'dou-ui'
import type { MessageView, NoticeView } from '@ariadne/core'
import {
  ChevronDownIcon,
  CpuIcon,
  FileDiffIcon,
  GitBranchIcon,
  ListTreeIcon,
  MoreHorizontalIcon,
  PaperclipIcon,
  PauseIcon,
  PlayIcon,
  RotateCwIcon,
  SquareStopIcon,
  CloseIcon,
} from '@dou-ui/icons'
import { computed, ref, useTemplateRef, watch } from 'vue'
import { api } from '../api'
import { count, t, tm } from '../i18n'
import GettingStarted from './GettingStarted.vue'
import PullRequestTag from './PullRequestTag.vue'
import { todoPlan } from './todo'
import { shownAttachments, useAttachments } from './attachments'
import {
  openMain,
  openThread,
  retryTurn,
  sendMessage,
  state,
  resumeThread,
  setThreadModel,
  stopThread,
  stopTurn,
  undoNotice,
} from '../store'
import StageLabel from './StageLabel.vue'
import AskBlock from '../blocks/AskBlock.vue'
import ShowBlock from '../blocks/ShowBlock.vue'

/** `thread` shows the open thread's conversation; otherwise the coordinator's. */
const props = defineProps<{ thread?: boolean }>()
const emit = defineEmits<{ openSettings: [] }>()

const draft = ref('')
const attachments = useAttachments(() => state.detail?.project.id ?? null)
const picker = useTemplateRef<HTMLInputElement>('picker')
function pick(event: Event) {
  const input = event.target as HTMLInputElement
  if (input.files) attachments.add(input.files)
  input.value = ''
}
const canSubmit = computed(
  () => (!!draft.value.trim() || attachments.files.value.length > 0) && attachments.ready(),
)

// A message's images open in a preview, its text files in a tab of their own.
const preview = ref<{ open: boolean; images: string[]; index: number }>({
  open: false,
  images: [],
  index: 0,
})
function openAttachment(message: MessageView, file: ChatAttachment) {
  if (!file.url) return
  if (!file.type?.startsWith('image/')) {
    window.open(file.url, '_blank', 'noopener')
    return
  }
  const images = shownAttachments(message.attachments).filter((item) =>
    item.type?.startsWith('image/'),
  )
  preview.value = {
    open: true,
    images: images.map((item) => item.url!),
    index: Math.max(
      0,
      images.findIndex((item) => item.uid === file.uid),
    ),
  }
}

const view = computed(() => (props.thread ? state.thread : state.coordinator))
const conversationId = computed(() =>
  props.thread ? state.threadId : (state.detail?.project.coordinatorId ?? null),
)
const thread = computed(() =>
  props.thread && state.threadId
    ? (state.detail?.threads.find((item) => item.id === state.threadId) ?? null)
    : null,
)
const working = computed(() => !!view.value.conversation?.turn)
// The model it uses now; a thread's picker also offers going back to the default.
const modelLabel = computed(() => {
  const id = view.value.model?.id
  return state.models.find((model) => model.id === id)?.label ?? null
})
const defaultLabel = computed(
  () => state.models.find((model) => model.id === state.threadDefault)?.label ?? '',
)
// A model since removed from the settings falls back to the default, so that is shown.
const pickedModel = computed(() => {
  const id = thread.value?.model
  return id && state.models.some((model) => model.id === id) ? id : ''
})
function pickModel(value: string | number | undefined) {
  if (thread.value) void setThreadModel(thread.value.id, value ? String(value) : null)
}
function openTrace() {
  if (conversationId.value) openMain({ kind: 'trace', conversationId: conversationId.value })
}
const todo = computed(() => todoPlan(view.value.todo, working.value))
// Set up enough to talk: a model, a project, and its working directory.
const started = computed(
  () => state.modelReady !== false && !!state.detail && !!state.detail.project.source,
)
// New input is waiting for a free slot.
const queued = computed(() => !working.value && (view.value.conversation?.pendingSeq ?? 0) > 0)
const failure = computed(() =>
  view.value.conversation?.lastOutcome === 'failed' && !working.value
    ? view.value.conversation.lastError
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
const cancelled = computed(
  () => view.value.conversation?.lastOutcome === 'cancelled' && !working.value,
)

// Messages edited after the user read them stay highlighted for this visit, even though
// opening the conversation marks them read on the server.
const highlighted = ref(new Set<string>())
watch(
  () => view.value.messages,
  (messages) => {
    for (const message of messages) {
      if (message.editedSinceRead) highlighted.value.add(message.id)
    }
  },
  { immediate: true },
)
watch(
  () => [state.currentId, conversationId.value],
  () => (highlighted.value = new Set()),
)

// Messages and notices in the order they happened.
const timeline = computed(() =>
  [
    ...view.value.messages.map((message) => ({
      id: message.id,
      at: message.createdAt,
      message,
      notice: null,
    })),
    ...view.value.notices.map((notice) => ({
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

/** The files sent with it, the text, then tables, charts and steps, then what it asks. */
function partsOf(message: MessageView): ChatMessageProps['parts'] {
  return [
    ...(message.attachments.length ? [{ type: 'attachments' }] : []),
    ...(message.content ? [{ type: 'text' as const, text: message.content }] : []),
    ...message.blocks.map((_, index) => ({ type: 'block', index })),
    ...(message.ask ? [{ type: 'ask' }] : []),
  ]
}

function sender(message: MessageView) {
  return message.author === 'user' ? 'user' : 'assistant'
}

async function submit(value: string) {
  const id = conversationId.value
  if (!id || !canSubmit.value) return
  draft.value = ''
  const files = attachments.take()
  const sent = await sendMessage(id, value, files.attachments)
  files.settle(sent)
  // Not sent: the text comes back too, unless something new was typed meanwhile.
  if (!sent && !draft.value) draft.value = value
}

function stop() {
  if (conversationId.value) void stopTurn(conversationId.value)
}

function retry() {
  if (conversationId.value) void retryTurn(conversationId.value)
}

// Everything on screen counts as read; this also clears the "edited since read" highlight.
watch(
  () => view.value.messages.at(-1)?.id,
  (last) => {
    const conversation = conversationId.value
    if (last && conversation) void api.markRead(conversation, last)
  },
)
</script>

<template>
  <div class="conversation">
    <div v-if="thread" class="conversation-header">
      <div class="conversation-title">
        <DouText weight="medium" truncate>{{ thread.title }}</DouText>
        <DouButton
          variant="ghost"
          size="sm"
          :aria-label="t('thread.close')"
          :title="t('thread.close')"
          @click="openThread(null)"
        >
          <CloseIcon />
        </DouButton>
      </div>
      <div class="conversation-meta">
        <StageLabel :stage="thread.stage" />
        <DouTag
          v-if="thread.workspace?.branch"
          size="sm"
          :title="`${t('thread.workspace')}: ${thread.workspace.label}`"
        >
          <template #leading><GitBranchIcon /></template>
          {{ thread.workspace.branch }}
        </DouTag>
        <PullRequestTag :thread="thread" refreshable />
        <DouButton
          v-if="thread.workspace?.branch && thread.workspace.base"
          variant="ghost"
          size="sm"
          @click="openMain({ kind: 'changes', threadId: thread.id })"
        >
          <template #leading><FileDiffIcon /></template>
          {{ t('changes.open') }}
        </DouButton>
        <div class="conversation-tools">
          <DouText
            v-if="thread.usage.input || thread.usage.output"
            variant="tertiary"
            size="sm"
            :title="t('usage.detail', { input: thread.usage.input, output: thread.usage.output })"
          >
            {{ t('usage.short', { value: count(thread.usage.input + thread.usage.output) }) }}
          </DouText>
          <DouPopover
            v-if="thread.stopped ? thread.stopped.by !== 'user' : thread.stage !== 'done'"
            v-model:open="stopOpen"
            trigger="click"
            :title="t(thread.stopped ? 'thread.takeOverTitle' : 'thread.stopTitle')"
            :width="320"
          >
            <DouButton variant="ghost" size="sm">
              <template #leading><PauseIcon /></template>
              {{ t(thread.stopped ? 'thread.takeOver' : 'thread.stop') }}
            </DouButton>
            <template #content>
              <div class="stop-form">
                <DouInput v-model="stopReason" :placeholder="t('thread.stopReasonPlaceholder')" />
                <DouButton
                  size="sm"
                  tone="primary"
                  @click="stopThreadNow(thread.id, !!thread.stopped)"
                >
                  {{ t('thread.stopConfirm') }}
                </DouButton>
              </div>
            </template>
          </DouPopover>
          <DouDropdown placement="bottom-end">
            <DouButton
              variant="ghost"
              size="sm"
              :aria-label="t('thread.more')"
              :title="t('thread.more')"
            >
              <MoreHorizontalIcon />
            </DouButton>
            <template #menu>
              <DouDropdownItem value="trace" @click="openTrace">
                <template #prefix><ListTreeIcon /></template>
                {{ t('trace.open') }}
              </DouDropdownItem>
            </template>
          </DouDropdown>
        </div>
      </div>
    </div>

    <DouChatList v-if="timeline.length || todo.length || working" class="conversation-list">
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
          <!-- Which work this conversation pointed at which issue. -->
          <DouText
            v-else-if="item.notice.kind === 'issue-linked'"
            variant="tertiary"
            size="sm"
            class="notice-text"
          >
            {{
              t(item.notice.linked ? 'notice.issueLinked' : 'notice.issueUnlinked', {
                issue: `${item.notice.tracker}#${item.notice.number}`,
                node: item.notice.nodeTitle,
              })
            }}
          </DouText>
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
          <!-- What happened to its branch and worktree. -->
          <DouText
            v-else-if="item.notice.kind === 'merged'"
            variant="tertiary"
            size="sm"
            class="notice-text"
          >
            {{
              t(item.notice.method === 'squash' ? 'notice.mergedSquash' : 'notice.mergedMerge', {
                base: item.notice.base,
                sha: item.notice.sha.slice(0, 7),
              })
            }}
          </DouText>
          <DouText
            v-else-if="item.notice.kind === 'merge-conflict'"
            tone="warning"
            size="sm"
            class="notice-text"
          >
            {{
              t('notice.mergeConflict', {
                base: item.notice.base,
                files: item.notice.files.join('、'),
              })
            }}
          </DouText>
          <DouText
            v-else-if="item.notice.kind === 'worktree-removed'"
            variant="tertiary"
            size="sm"
            class="notice-text"
          >
            {{
              t(
                item.notice.reason === 'merged'
                  ? 'notice.worktreeRemovedMerged'
                  : 'notice.worktreeRemovedUser',
              )
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
            :parts="partsOf(message)"
            :class="{ 'conversation-edited': highlighted.has(message.id) }"
          >
            <template #part="{ part }">
              <DouChatAttachments
                v-if="part.type === 'attachments'"
                :model-value="shownAttachments(message.attachments)"
                :removable="false"
                @open="(file) => openAttachment(message, file)"
              />
              <AskBlock
                v-else-if="part.type === 'ask' && message.ask"
                :attention-id="message.ask.attentionId"
                :block="message.ask.block"
                :answer="message.ask.answer"
                :withdrawn="message.ask.withdrawn"
              />
              <ShowBlock
                v-else-if="part.type === 'block'"
                :block="message.blocks[part.index as number]!"
              />
            </template>
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
      <!-- What it is doing, what comes next, what it waits on: in place of progress messages. -->
      <DouChatPlan v-if="todo.length" :items="todo" :title="t('todo.title')" class="todo" />
      <DouChatMessage v-if="working" sender="assistant" loading />
    </DouChatList>
    <div v-else class="conversation-empty">
      <DouEmpty v-if="props.thread" :title="thread?.title" :description="t('thread.empty')" />
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
        {{ t('conversation.working') }} · {{ view.conversation?.turn?.model }}
      </DouText>
      <DouButton variant="ghost" size="sm" @click="stop">
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
        <DouButton size="sm" variant="outline" @click="retry">
          <template #leading><RotateCwIcon /></template>
          {{ t('conversation.retry') }}
        </DouButton>
      </template>
    </DouAlert>
    <DouAlert v-else-if="cancelled" tone="neutral" class="conversation-alert">
      {{ t('conversation.cancelled') }}
      <template #action>
        <DouButton size="sm" variant="outline" @click="retry">
          <template #leading><RotateCwIcon /></template>
          {{ t('conversation.retry') }}
        </DouButton>
      </template>
    </DouAlert>

    <DouImagePreview
      v-model:open="preview.open"
      v-model:index="preview.index"
      :images="preview.images"
    />
    <div class="conversation-input">
      <DouChatInput
        v-model="draft"
        :placeholder="props.thread ? t('thread.placeholder') : t('coordinator.placeholder')"
        :disabled="!state.detail"
        :can-submit="canSubmit"
        @submit="submit"
        @attach="attachments.add"
      >
        <template v-if="attachments.files.value.length" #header>
          <DouChatAttachments v-model="attachments.files.value" @remove="attachments.release" />
        </template>
        <template #actions>
          <input ref="picker" type="file" multiple hidden @change="pick" />
          <DouButton
            variant="ghost"
            size="sm"
            :aria-label="t('composer.attach')"
            :title="t('composer.attach')"
            :disabled="!state.detail"
            @click="picker?.click()"
          >
            <PaperclipIcon />
          </DouButton>
          <!-- Which model answers here, and how full its context is. -->
          <DouDropdown v-if="thread && state.models.length > 1" placement="top-start">
            <DouButton variant="ghost" size="sm" :title="t('thread.model')">
              <template #leading><CpuIcon /></template>
              <span class="conversation-model">{{ modelLabel ?? t('thread.noModel') }}</span>
              <template #trailing><ChevronDownIcon /></template>
            </DouButton>
            <template #menu>
              <DouDropdownRadioGroup :model-value="pickedModel" @update:model-value="pickModel">
                <DouDropdownItem value="">
                  {{ t('thread.modelDefault', { label: defaultLabel }) }}
                </DouDropdownItem>
                <DouDropdownItem v-for="model in state.models" :key="model.id" :value="model.id">
                  {{ model.label }}
                </DouDropdownItem>
              </DouDropdownRadioGroup>
            </template>
          </DouDropdown>
          <DouText v-else-if="modelLabel" variant="tertiary" size="sm" class="conversation-model">
            {{ modelLabel }}
          </DouText>
          <DouChatContextMeter
            v-if="view.model && view.conversation?.contextTokens"
            :used="view.conversation.contextTokens"
            :total="view.model.contextTokens"
            :warn-at="0.5"
            :danger-at="0.6"
          />
        </template>
      </DouChatInput>
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
  flex-direction: column;
  gap: 4px;
  padding: 8px 12px 8px 16px;
  border-bottom: 1px solid var(--dou-color-border-subtle);
}

.conversation-title,
.conversation-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.conversation-title {
  justify-content: space-between;
}

.conversation-meta {
  flex-wrap: wrap;
}

.conversation-list {
  flex: 1;
  min-height: 0;
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

.conversation-tools {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-left: auto;
}

.conversation-model {
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
