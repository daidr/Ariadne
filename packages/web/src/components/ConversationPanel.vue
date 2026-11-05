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
  DouShimmer,
  DouSpinner,
  DouStatusDot,
  DouTag,
  DouText,
  type ChatAttachment,
  type ChatInputOption,
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
  MessagesIcon,
} from '@dou-ui/icons'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useTemplateRef, watch } from 'vue'
import { api } from '../api'
import { clockText, count, t, tm } from '../i18n'
import GettingStarted from './GettingStarted.vue'
import PullRequestTag from './PullRequestTag.vue'
import { todoPlan } from './todo'
import { shownAttachments, useAttachments } from './attachments'
import {
  loadEarlier,
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
import ReviewActions from './ReviewActions.vue'
import ReviewMark from '../review/ReviewMark.vue'
import ReviewNotice from '../review/ReviewNotice.vue'
import { useSkills } from '../review/skills'
import LineComments from './LineComments.vue'
import LiveCommand from './LiveCommand.vue'
import BackgroundCommand from './BackgroundCommand.vue'
import WaitStatus from './WaitStatus.vue'
import TurnChanges from './TurnChanges.vue'
import { useMentions } from './mentions'
import StageLabel from './StageLabel.vue'
import ArtifactNotice from './ArtifactNotice.vue'
import AskBlock from '../blocks/AskBlock.vue'
import ShowBlock from '../blocks/ShowBlock.vue'
import AgentText from '../markdown/AgentText.vue'
import MessageImage from '../markdown/MessageImage.vue'
import MessageLink from '../markdown/MessageLink.vue'
import { isBare, sanitizeUrl } from '../markdown/links'
import { lastPanel, registerPanel } from '../shortcuts'
import { stepHistory, type Recall } from './input-history'

/** `thread` shows the open thread's conversation; otherwise the coordinator's. */
const props = defineProps<{ thread?: boolean }>()
const emit = defineEmits<{ openSettings: [tab?: 'input'] }>()

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
// `@` offers threads, artifacts and files; what was picked goes with the message.
const mentions = useMentions(
  draft,
  () => input.value?.textarea?.selectionStart ?? draft.value.length,
  () => conversationId.value,
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
/** For a reviewing thread: the thread whose changes it reviews. */
const reviewed = computed(() => {
  const id = thread.value?.reviewOf
  return id ? (state.detail?.threads.find((item) => item.id === id) ?? null) : null
})
/** The brief the autopilot opened this thread with: its very first message. */
const autoBrief = computed(() =>
  thread.value?.auto && !view.value.more ? (view.value.messages[0]?.id ?? null) : null,
)
const working = computed(() => !!view.value.conversation?.turn)
// The command it is running now, if any, shown in the status line.
const live = computed(() =>
  conversationId.value ? (state.live[conversationId.value] ?? null) : null,
)
// What the thread left running between turns, and what the conversation waits for.
const background = computed(() =>
  (conversationId.value ? (state.background[conversationId.value] ?? []) : []).filter(
    (command) => !command.result,
  ),
)
const wait = computed(() => (working.value ? null : (view.value.conversation?.wait ?? null)))
// The main model kept failing: this turn goes on with the role's fallback.
const fallback = computed(() => {
  const turn = view.value.conversation?.turn
  return turn?.fallback ? turn.model : null
})
// What the model said it is doing beside its latest tool calls, shown above the input.
const activity = computed(
  () => view.value.conversation?.turn?.activity ?? t('conversation.working'),
)
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
const todo = computed(() => todoPlan(view.value.todo))
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

/** How the host names the thread's pull request, e.g. `!12` on GitLab. */
function pullRequestRef(number: number): string {
  const pr = thread.value?.pullRequest
  return pr?.number === number && pr.reference ? pr.reference : `#${number}`
}

/** By the user's hand or the autopilot's, into the base or through the pull request. */
function mergedNotice(notice: Extract<NoticeView, { kind: 'merged' }>) {
  const base = notice.base
  if (notice.pullRequest) {
    return t('notice.mergedAutoPullRequest', {
      pullRequest: pullRequestRef(notice.pullRequest),
      base,
    })
  }
  const sha = notice.sha.slice(0, 7)
  if (notice.auto) {
    return t(notice.method === 'squash' ? 'notice.mergedAutoSquash' : 'notice.mergedAutoMerge', {
      base,
      sha,
    })
  }
  return t(notice.method === 'squash' ? 'notice.mergedSquash' : 'notice.mergedMerge', { base, sha })
}

function conflictNotice(notice: Extract<NoticeView, { kind: 'merge-conflict' }>) {
  if (notice.pullRequest) {
    return t('notice.mergeConflictPullRequest', {
      pullRequest: pullRequestRef(notice.pullRequest),
      base: notice.base,
    })
  }
  return t('notice.mergeConflict', { base: notice.base, files: notice.files.join('、') })
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
    ...(message.comments.length ? [{ type: 'comments' }] : []),
    ...message.blocks.map((_, index) => ({ type: 'block', index })),
    ...(message.ask ? [{ type: 'ask' }] : []),
  ]
}

function sender(message: MessageView) {
  return message.author === 'user' ? 'user' : 'assistant'
}

/** Agents write Markdown; the user's own words are shown as typed. */
const markdown = { linkTarget: '_blank', sanitizeUrl } as const

async function submit(value: string) {
  const id = conversationId.value
  if (!id || !canSubmit.value) return
  draft.value = ''
  const files = attachments.take()
  const sent = await sendMessage(id, value, {
    attachments: files.attachments,
    mentions: [...mentions.inText(value), ...skills.inText(value)],
  })
  files.settle(sent)
  if (sent) {
    mentions.reset()
    skills.reset()
  }
  // Not sent: the text comes back too, unless something new was typed meanwhile.
  if (!sent && !draft.value) draft.value = value
}

function stop() {
  if (conversationId.value) void stopTurn(conversationId.value)
}

// The keyboard reaches this conversation: `/` puts the caret in its input, Escape stops it.
const input = useTemplateRef<{ focus: () => void; textarea: HTMLTextAreaElement | null }>('input')
const kind = props.thread ? 'thread' : 'coordinator'
let unregister: (() => void) | undefined
onMounted(() => {
  unregister = registerPanel(kind, {
    focus: () => input.value?.focus(),
    stop: () => {
      if (!working.value) return false
      stop()
      return true
    },
  })
})
onBeforeUnmount(() => unregister?.())

// ↑ in an empty input brings back what the user sent here before, newest first.
const sent = computed(() =>
  view.value.messages
    .filter((message) => message.author === 'user' && message.content.trim())
    .map((message) => message.content)
    .toReversed(),
)
let recall: Recall | null = null
watch(conversationId, () => (recall = null))
function onInputKeydown(event: KeyboardEvent) {
  // The `/` menu moves through its entries with the arrows and has already taken them.
  if (event.defaultPrevented || event.isComposing) return
  if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return
  if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
  const next = stepHistory(sent.value, recall, draft.value, event.key === 'ArrowUp' ? 1 : -1)
  if (!next) return
  event.preventDefault()
  recall = next.recall
  draft.value = next.draft
  void caretToEnd()
}

async function caretToEnd() {
  await nextTick()
  const el = input.value?.textarea
  el?.setSelectionRange(el.value.length, el.value.length)
}

// `/` at the start offers the skills to work by, then the user's snippets; picking a
// snippet puts its text in the input, picking a skill names it there.
const MANAGE = ':manage'
const skills = useSkills(() => conversationId.value)
const commands = computed<ChatInputOption[]>(() => [
  ...skills.options.value,
  ...state.input.snippets.map((snippet) => ({
    value: snippet.id,
    label: snippet.name,
    description: snippet.text.split('\n', 1)[0],
  })),
  { value: MANAGE, label: t('snippets.manage') },
])
function useSnippet(option: ChatInputOption) {
  if (option.value === MANAGE) {
    emit('openSettings', 'input')
    return
  }
  const named = skills.pick(option)
  if (named) {
    draft.value = `${named} ${draft.value}`
    setTimeout(() => {
      const el = input.value?.textarea
      el?.setSelectionRange(named.length + 1, named.length + 1)
    })
    return
  }
  const snippet = state.input.snippets.find((item) => item.id === option.value)
  if (!snippet) return
  draft.value = snippet.text + draft.value
  // After the input has put its caret where the `/` was.
  setTimeout(() => {
    const el = input.value?.textarea
    el?.setSelectionRange(snippet.text.length, snippet.text.length)
  })
}

function retry() {
  if (conversationId.value) void retryTurn(conversationId.value)
}

// Scrolled to the top: the page before. A first page too short to scroll fetches the
// next one by itself, since there is no top to reach.
const list = useTemplateRef<{ viewport: HTMLElement | null }>('list')
function earlier() {
  if (conversationId.value && view.value.more) void loadEarlier(conversationId.value)
}
watch(
  () => [view.value.messages.length, view.value.more],
  async () => {
    if (!view.value.more || view.value.loadingEarlier) return
    await nextTick()
    const el = list.value?.viewport
    if (el && el.scrollHeight <= el.clientHeight) earlier()
  },
)

// A message the search found: scroll to it and mark it for a moment. A message from the
// coordinator to a thread is folded, so it opens.
const focused = ref<string | null>(null)
const openRelays = ref<string[]>([])
let unmark: ReturnType<typeof setTimeout> | undefined
watch(
  () => state.focus,
  async (focus) => {
    if (!focus || focus.conversationId !== conversationId.value) return
    state.focus = null
    const message = view.value.messages.find((item) => item.id === focus.messageId)
    if (message?.kind === 'relay' && !openRelays.value.includes(message.id)) {
      openRelays.value = [...openRelays.value, message.id]
    }
    await nextTick()
    const el = list.value?.viewport?.querySelector(`[data-message-id="${focus.messageId}"]`)
    el?.scrollIntoView({ block: 'center' })
    focused.value = focus.messageId
    clearTimeout(unmark)
    unmark = setTimeout(() => (focused.value = null), 2000)
  },
  { immediate: true },
)

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
  <div
    class="conversation"
    :data-panel="kind"
    @focusin="lastPanel = kind"
    @pointerdown="lastPanel = kind"
  >
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
        <DouShimmer v-if="thread.review?.reviewing" class="conversation-reviewing">
          {{ t('review.underway', { round: thread.review.rounds }) }}
        </DouShimmer>
        <StageLabel v-else :stage="thread.stage" />
        <ReviewActions v-if="thread.stage === 'review'" :thread-id="thread.id" />
        <DouTag
          v-if="thread.workspace?.branch"
          size="sm"
          :title="`${t('thread.workspace')}: ${thread.workspace.label}`"
        >
          <template #leading><GitBranchIcon /></template>
          {{ thread.workspace.branch }}
        </DouTag>
        <PullRequestTag :thread="thread" refreshable />
        <ReviewMark :thread="thread" />
        <DouButton
          v-if="thread.workspace?.branch && thread.workspace.base"
          variant="ghost"
          size="sm"
          @click="openMain({ kind: 'changes', threadId: thread.id })"
        >
          <template #leading><FileDiffIcon /></template>
          {{ t('changes.open') }}
        </DouButton>
        <!-- A reviewing thread reads its author's changes: they open from here, as does the author. -->
        <template v-if="reviewed">
          <DouButton variant="ghost" size="sm" @click="openThread(reviewed.id)">
            <template #leading><MessagesIcon /></template>
            {{ t('review.author') }}
          </DouButton>
          <DouButton
            v-if="reviewed.workspace?.branch && reviewed.workspace.base"
            variant="ghost"
            size="sm"
            @click="openMain({ kind: 'changes', threadId: reviewed.id })"
          >
            <template #leading><FileDiffIcon /></template>
            {{ t('changes.open') }}
          </DouButton>
        </template>
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

    <DouChatList
      v-if="timeline.length || todo.length || working"
      ref="list"
      class="conversation-list"
      @top-reached="earlier"
    >
      <div v-if="view.loadingEarlier" class="earlier" role="status">
        <DouSpinner size="sm" />
      </div>
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
          <DouText
            v-else-if="item.notice.kind === 'model-fallback'"
            variant="tertiary"
            size="sm"
            class="notice-text"
          >
            {{ t('notice.modelFallback', { from: item.notice.from, to: item.notice.to }) }}
          </DouText>
          <DouText
            v-else-if="item.notice.kind === 'woken'"
            variant="tertiary"
            size="sm"
            class="notice-text"
          >
            {{
              t(item.notice.how === 'now' ? 'notice.wokenByUser' : 'notice.wokenDue', {
                note: item.notice.note,
              })
            }}
          </DouText>
          <!-- A routine's prompt is there for whoever wants it, out of the way otherwise. -->
          <DouCollapse
            v-else-if="item.notice.kind === 'routine'"
            v-model="openRelays"
            class="relay routine"
          >
            <DouCollapseItem
              :value="item.notice.id"
              :title="t('notice.routine', { time: clockText(item.notice.dueAt) })"
            >
              <DouText size="sm" class="routine-prompt">{{ item.notice.prompt }}</DouText>
            </DouCollapseItem>
          </DouCollapse>
          <!-- What happened to its branch and worktree. -->
          <DouText
            v-else-if="item.notice.kind === 'merged'"
            variant="tertiary"
            size="sm"
            class="notice-text"
          >
            {{ mergedNotice(item.notice) }}
          </DouText>
          <DouText
            v-else-if="item.notice.kind === 'autopilot-held'"
            tone="warning"
            size="sm"
            class="notice-text"
          >
            {{ t(`notice.autopilotHeld.${item.notice.reason}`, { detail: item.notice.detail }) }}
          </DouText>
          <DouText
            v-else-if="item.notice.kind === 'merge-conflict'"
            tone="warning"
            size="sm"
            class="notice-text"
          >
            {{ conflictNotice(item.notice) }}
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
          <ArtifactNotice
            v-else-if="
              item.notice.kind === 'artifact-edited' || item.notice.kind === 'artifact-comment'
            "
            :notice="item.notice"
          />
          <TurnChanges v-else-if="item.notice.kind === 'turn-changes'" :notice="item.notice" />
          <ReviewNotice v-else-if="item.notice.kind === 'review'" :notice="item.notice" />
          <DouText
            v-else-if="item.notice.kind === 'compaction-failed'"
            tone="warning"
            size="sm"
            class="notice-text"
          >
            {{ t('notice.compactionFailed', { detail: item.notice.detail }) }}
          </DouText>
        </div>
        <template v-for="message in item.message ? [item.message] : []" v-else :key="message.id">
          <!-- What the coordinator told a thread: there for whoever wants it, out of the way otherwise. -->
          <DouCollapse
            v-if="message.kind === 'relay'"
            v-model="openRelays"
            :data-message-id="message.id"
            :class="['relay', { 'conversation-focused': focused === message.id }]"
          >
            <DouCollapseItem
              :value="message.id"
              :title="
                message.id === autoBrief ? t('message.fromAutopilot') : t('message.fromCoordinator')
              "
            >
              <AgentText :content="message.content" class="relay-content" />
            </DouCollapseItem>
          </DouCollapse>
          <DouChatMessage
            v-else
            :sender="sender(message)"
            :parts="partsOf(message)"
            :markdown="sender(message) === 'assistant' && markdown"
            :data-message-id="message.id"
            :class="{
              'conversation-edited': highlighted.has(message.id),
              'conversation-focused': focused === message.id,
            }"
          >
            <template #markdown-link="{ node, href, external, children }">
              <MessageLink
                :href="href"
                :external="external"
                :bare="isBare(node)"
                :children="children"
              />
            </template>
            <template #markdown-image="{ src, alt, title }">
              <MessageImage :src="src" :alt="alt" :title="title" />
            </template>
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
              <LineComments
                v-else-if="part.type === 'comments'"
                :comments="message.comments"
                :thread-id="message.conversationId"
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
                      <AgentText
                        v-if="message.author !== 'user'"
                        :content="message.originalContent"
                        class="edit-original edit-original-markdown"
                      />
                      <DouText v-else size="sm" class="edit-original">{{
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
      <DouChatPlan
        v-if="todo.length"
        :items="todo"
        :title="t('todo.title')"
        :collapsible="false"
        :live="working"
        class="todo"
      />
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
      <LiveCommand v-if="live" :command="live" />
      <DouText v-else variant="secondary" size="sm" class="conversation-activity">
        <DouStatusDot tone="primary" ripple />
        <DouShimmer class="conversation-activity-text">{{ activity }}</DouShimmer>
      </DouText>
      <div class="conversation-status-end">
        <DouText v-if="fallback" variant="tertiary" size="sm" class="conversation-fallback">
          {{ t('conversation.fallback', { model: fallback }) }}
        </DouText>
        <DouButton variant="ghost" size="sm" @click="stop">
          <template #leading><SquareStopIcon /></template>
          {{ t('conversation.stop') }}
        </DouButton>
      </div>
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

    <div v-if="background.length && conversationId" class="conversation-background">
      <BackgroundCommand
        v-for="command in background"
        :key="command.id"
        :thread-id="conversationId"
        :command="command"
      />
    </div>
    <WaitStatus
      v-if="wait && conversationId"
      :conversation-id="conversationId"
      :wait="wait"
      class="conversation-wait"
    />

    <DouImagePreview
      v-model:open="preview.open"
      v-model:index="preview.index"
      :images="preview.images"
    />
    <div class="conversation-input" data-message-input @keydown="onInputKeydown">
      <DouChatInput
        ref="input"
        v-model="draft"
        :submit-on="state.input.submitOn"
        :commands="commands"
        :mentions="mentions.options.value"
        @command="useSnippet"
        @mention="mentions.pick"
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
          <!-- Full at the line where older history is made room for. -->
          <DouChatContextMeter
            v-if="view.model && view.conversation?.contextTokens"
            :used="view.conversation.contextTokens"
            :total="view.model.compactAt"
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

.edit-original-markdown {
  font-size: var(--dou-font-size-sm);
  white-space: normal;
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

.conversation-status-end {
  display: flex;
  flex: none;
  align-items: center;
  gap: 8px;
}

.conversation-fallback {
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.conversation-background {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0 16px;
}

.conversation-wait {
  margin: 0 16px;
}

.routine-prompt {
  display: block;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.conversation-activity {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.conversation-activity-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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

.earlier {
  display: flex;
  justify-content: center;
  padding: 4px 0;
}

/* A message the search led to, marked until the eye has found it: its text, not the row. */
.relay.conversation-focused,
.conversation-focused :deep(.dou-chat-message__content) {
  outline: 1px solid var(--dou-color-primary);
  outline-offset: 4px;
  border-radius: var(--dou-radius-container);
}

.conversation-edited {
  outline: 1px solid var(--dou-color-warning);
  outline-offset: 4px;
  border-radius: var(--dou-radius-container);
}

/* Faded and small: the user can read it, but it does not compete with the conversation. */
.relay {
  --dou-collapse-border: transparent;
  --dou-collapse-header-padding-y: 6px;
  font-size: var(--dou-font-size-sm);
  opacity: 0.7;
}

.relay-content {
  font-size: var(--dou-font-size-sm);
}

.conversation-reviewing {
  font-size: var(--dou-font-size-sm);
}
</style>
