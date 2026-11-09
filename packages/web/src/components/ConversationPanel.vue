<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouChatAttachments,
  DouChatInput,
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
  DouText,
  type ChatAttachment,
  type ChatInputOption,
  type ChatMessageProps,
  type ChatListExpose,
} from 'dou-ui'
import type { MessageView, NoticeView } from '@ariadne/core'
import {
  ChevronDownIcon,
  CpuIcon,
  FileDiffIcon,
  ListTreeIcon,
  MoreHorizontalIcon,
  PaperclipIcon,
  PauseIcon,
  PlayIcon,
  RotateCwIcon,
  MessagesIcon,
} from '@dou-ui/icons'
import {
  computed,
  inject,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  useTemplateRef,
  watch,
} from 'vue'
import { openSettingsKey } from '../open-settings'
import { api } from '../api'
import { clockText, t, tm } from '../i18n'
import GettingStarted from './GettingStarted.vue'
import ThreadTools from './ThreadTools.vue'
import PullRequestTag from './PullRequestTag.vue'
import { todoPlan } from './todo'
import { shownAttachments, useAttachments } from './attachments'
import {
  composerFor,
  emptyConversation,
  loadEarlier,
  openMain,
  openView as openFrom,
  type OpenedView,
  openThread,
  openThreadFrom,
  retryTurn,
  sendMessage,
  state,
  resumeThread,
  setThreadModel,
  stopTurn,
  undoMerge,
  undoNotice,
} from '../store'
import WorkspaceTag from './WorkspaceTag.vue'
import { offerNotifications } from '../notify'
import ContextUsage from './ContextUsage.vue'
import ReviewActions from './ReviewActions.vue'
import ReviewMark from '../review/ReviewMark.vue'
import ReviewNotice from '../review/ReviewNotice.vue'
import { useSkills } from '../review/skills'
import LineComments from './LineComments.vue'
import LiveCommand from './LiveCommand.vue'
import BackgroundCommand from './BackgroundCommand.vue'
import WaitStatus from './WaitStatus.vue'
import ChangeTotals from './ChangeTotals.vue'
import { branchChanges, mergeOffered } from '../review/merge-offered'
import TurnChanges from './TurnChanges.vue'
import { useMentions } from './mentions'
import { elapsedText } from './command-text'
import ArtifactFeedbackNotice from './ArtifactFeedbackNotice.vue'
import ArtifactNotice from './ArtifactNotice.vue'
import RecapNotice from './RecapNotice.vue'
import PlanUndoButton from '../plan/PlanUndoButton.vue'
import AskBlock from '../blocks/AskBlock.vue'
import ShowBlock from '../blocks/ShowBlock.vue'
import AgentText from '../markdown/AgentText.vue'
import MessageImage from '../markdown/MessageImage.vue'
import MessageLink from '../markdown/MessageLink.vue'
import { isBare, sanitizeUrl } from '../markdown/links'
import { lastPanel, registerPanel } from '../shortcuts'
import { stepHistory, type Recall } from './input-history'
import { provideFromThread } from '../opened-from'

/** `threadId` shows that thread's conversation; otherwise the coordinator's. */
// `tabbed`: in a side tab group, whose own menu holds the thread's.
// `merging`: its changes are open beside it, with the ways out of the review on them.
const props = defineProps<{ threadId?: string; tabbed?: boolean; merging?: boolean }>()

// What opens from a thread shows full screen, with this thread beside it.
provideFromThread(() => props.threadId ?? null)
const openView = (view: OpenedView) => openFrom(view, { from: props.threadId ?? null })
const openSettings = inject(openSettingsKey, () => {})

// A thread's input is kept in the store, so moving its tab to another group keeps it.
const composer = props.threadId ? composerFor(props.threadId) : null
const draft = composer?.draft ?? ref('')
const attachments = useAttachments(() => state.detail?.project.id ?? null, composer?.files)
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
  composer?.mentions,
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

// A thread opening shows nothing until its conversation is in.
const blank = emptyConversation()
const view = computed(() =>
  props.threadId ? (state.threads[props.threadId] ?? blank) : state.coordinator,
)
const conversationId = computed(() =>
  props.threadId ? props.threadId : (state.detail?.project.coordinatorId ?? null),
)
const thread = computed(() =>
  props.threadId
    ? (state.detail?.threads.find((item) => item.id === props.threadId) ?? null)
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
// What it is doing, shown above the input: what the model said beside its latest tool
// calls, else the todo it marked as under way. A thread needs no name for it; the
// coordinator does, as its column sits beside threads that work too.
const activity = computed(() =>
  // Once the reply shows, the line before the last tool calls is no longer what it does.
  writing.value
    ? t('conversation.replying')
    : (view.value.conversation?.turn?.activity ??
      thread.value?.doing ??
      t(props.threadId ? 'conversation.working' : 'conversation.coordinatorWorking')),
)
const now = ref(Date.now())
const clock = setInterval(() => (now.value = Date.now()), 1000)
onBeforeUnmount(() => clearInterval(clock))
const elapsed = computed(() => {
  const startedAt = view.value.conversation?.turn?.startedAt
  return startedAt ? elapsedText(now.value - startedAt) : ''
})
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
const todo = computed(() => todoPlan(view.value.todo, working.value))
// A list all ticked folds to its title; one with steps left opens again.
const todoOpen = ref(true)
// Another conversation starts from its own list, not from how the last one was left.
watch(
  [
    () => todo.value.length > 0 && todo.value.every((item) => item.status === 'done'),
    conversationId,
  ],
  ([finished]) => (todoOpen.value = !finished),
  { immediate: true },
)
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
const cancelled = computed(
  () => view.value.conversation?.lastOutcome === 'cancelled' && !working.value,
)
// Its turn ended without a report, and it was told once already: the user decides.
const silent = computed(() => thread.value?.stage === 'silent')

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

// The reply being written now, shown where its message will be: it keeps the message's id,
// so the same bubble goes on once the message is posted and loaded.
const writing = computed<MessageView | null>(() => {
  const id = conversationId.value
  const reply = id ? state.writing[id] : undefined
  if (!id || !reply || view.value.messages.some((message) => message.id === reply.messageId)) {
    return null
  }
  return {
    id: reply.messageId,
    conversationId: id,
    author: props.threadId ? 'thread' : 'coordinator',
    kind: 'chat',
    content: reply.text,
    blocks: [],
    ask: null,
    attachments: [],
    mentions: [],
    comments: [],
    createdAt: reply.startedAt,
    editedAt: null,
    editReason: null,
    originalContent: null,
    editedSinceRead: false,
  }
})

const recap = computed(() => view.value.notices.findLast((notice) => notice.kind === 'recap')?.id)

// Messages and notices in the order they happened. A worktree removed after a merge goes
// unsaid: the merge's own line says what happened.
const timeline = computed(() =>
  [
    ...view.value.messages.map((message) => ({
      id: message.id,
      at: message.createdAt,
      message,
      notice: null,
    })),
    ...view.value.notices
      .filter((notice) => !(notice.kind === 'worktree-removed' && notice.reason === 'merged'))
      // A newer recap takes the place of the one before it.
      .filter((notice) => notice.kind !== 'recap' || notice.id === recap.value)
      .map((notice) => ({
        id: notice.id,
        at: notice.createdAt,
        message: null,
        notice,
      })),
    ...(writing.value
      ? [
          {
            id: writing.value.id,
            at: writing.value.createdAt,
            message: writing.value,
            notice: null,
          },
        ]
      : []),
  ].toSorted((a, b) => a.at - b.at),
)

// Its review offers "go merge", which opens the changes: no second way there beside it.
const mergeAsked = computed(() => thread.value?.stage === 'review' && mergeOffered(thread.value))

type TurnChangesNotice = Extract<NoticeView, { kind: 'turn-changes' }>
/**
 * What a turn changed goes under the reply it ended with, beside what it says; one with
 * no reply of its own stays a line of the log.
 */
const shown = computed(() => {
  const changes = new Map<string, TurnChangesNotice>()
  const items: typeof timeline.value = []
  let reply: { id: string; at: number } | null = null
  for (const item of timeline.value) {
    if (item.message) {
      const agent = item.message.author !== 'user' && item.message.kind !== 'relay'
      reply = agent ? { id: item.message.id, at: item.message.createdAt } : null
    } else if (
      item.notice?.kind === 'turn-changes' &&
      reply &&
      !changes.has(reply.id) &&
      // A reply of an earlier turn: this one ended without saying anything.
      reply.at >= (item.notice.startedAt ?? 0)
    ) {
      changes.set(reply.id, item.notice)
      continue
    }
    items.push(item)
  }
  return { items, changes }
})

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
const undoing = ref(false)
async function undoLastMerge() {
  if (!props.threadId) return
  undoing.value = true
  await undoMerge(props.threadId)
  undoing.value = false
}

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

function planNotice(notice: Extract<NoticeView, { kind: 'plan-changed' }>) {
  const text = t(notice.revert ? 'notice.planReverted' : 'notice.planChanged', {
    reason: notice.reason,
  })
  return notice.added ? `${text} · ${t('notice.planAdded', { count: notice.added })}` : text
}
const revisionOf = (id: string) => state.plan?.revisions.find((revision) => revision.id === id)
// Narrowed where they are called; the template cannot narrow inside a handler.
const threadIdOf = (notice: NoticeView) =>
  notice.kind === 'thread-auto-started' ? notice.threadId : ''
const titleOf = (notice: NoticeView) =>
  notice.kind === 'thread-auto-started'
    ? (state.detail?.threads.find((item) => item.id === notice.threadId)?.title ?? notice.title)
    : ''

function memoryNotice(notice: Extract<NoticeView, { kind: 'memory' }>) {
  if (notice.action === 'removed') return t('notice.memoryRemoved', { text: notice.text })
  if (notice.previous === null) return t('notice.memoryAdded', { text: notice.text })
  return t('notice.memoryChanged', { previous: notice.previous, text: notice.text })
}

/** The files sent with it, the text, then tables, charts and steps, then what it asks. */
function partsOf(message: MessageView): ChatMessageProps['parts'] {
  // Still being written while it is the draft, not yet a message of the log.
  const streaming =
    message.id === writing.value?.id && !state.writing[message.conversationId]?.posted
  return [
    ...(message.attachments.length ? [{ type: 'attachments' }] : []),
    ...(message.content ? [{ type: 'text' as const, text: message.content, streaming }] : []),
    ...(message.comments.length ? [{ type: 'comments' }] : []),
    ...message.blocks.map((_, index) => ({ type: 'block', index })),
    ...(message.ask ? [{ type: 'ask' }] : []),
    ...(shown.value.changes.has(message.id) ? [{ type: 'changes' }] : []),
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
  offerNotifications()
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
  escArmed.value = false
  if (conversationId.value) void stopTurn(conversationId.value)
}

// Escape is pressed out of habit to close things, so one press only arms the stop: a
// second one soon after stops the turn. Never while there is text in the input.
const ESC_WINDOW_MS = 1500
const escArmed = ref(false)
let disarm: ReturnType<typeof setTimeout> | undefined
function escape(): boolean {
  if (!working.value || draft.value.trim()) return false
  if (escArmed.value) {
    clearTimeout(disarm)
    stop()
    return true
  }
  escArmed.value = true
  clearTimeout(disarm)
  disarm = setTimeout(() => (escArmed.value = false), ESC_WINDOW_MS)
  return true
}
watch(working, (busy) => {
  if (!busy) escArmed.value = false
})
onBeforeUnmount(() => clearTimeout(disarm))

// The keyboard reaches this conversation: `/` puts the caret in its input, Escape stops it.
const input = useTemplateRef<{ focus: () => void; textarea: HTMLTextAreaElement | null }>('input')
const kind = props.threadId ?? 'coordinator'
let unregister: (() => void) | undefined
onMounted(() => {
  unregister = registerPanel(kind, {
    focus: () => input.value?.focus(),
    stop: escape,
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
const skills = useSkills(() => conversationId.value, composer?.skills)
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
    openSettings('input')
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
const list = useTemplateRef<ChatListExpose>('list')
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
    list.value?.scrollToMessage(el)
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
      <div class="conversation-meta">
        <div class="conversation-meta-main">
          <DouShimmer v-if="thread.review?.reviewing" class="conversation-reviewing">
            {{ t('review.underway', { round: thread.review.rounds }) }}
          </DouShimmer>
          <ReviewActions
            v-if="thread.stage === 'review' && !(merging && mergeOffered(thread))"
            :thread-id="thread.id"
          />
          <WorkspaceTag v-if="thread.workspace?.branch" :workspace="thread.workspace" />
          <PullRequestTag :thread="thread" refreshable />
          <ReviewMark :thread="thread" />
          <!-- Under review, "go merge" opens the same changes. -->
          <template v-if="thread.workspace?.branch && thread.workspace.base && !mergeAsked">
            <ChangeTotals
              v-if="branchChanges(thread)"
              :totals="branchChanges(thread)!"
              :thread-id="thread.id"
              button
            />
            <DouButton
              v-else
              variant="ghost"
              size="sm"
              @click="openView({ kind: 'changes', threadId: thread.id })"
            >
              <template #leading><FileDiffIcon /></template>
              {{ t('changes.open') }}
            </DouButton>
          </template>
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
              @click="openView({ kind: 'changes', threadId: reviewed.id })"
            >
              <template #leading><FileDiffIcon /></template>
              {{ t('changes.open') }}
            </DouButton>
          </template>
        </div>
        <div v-if="!tabbed" class="conversation-tools">
          <ThreadTools :thread="thread" :label="t('thread.more')" />
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
      <template v-for="item in shown.items" :key="item.id">
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
          <template v-else-if="item.notice.kind === 'merged'">
            <DouText variant="tertiary" size="sm" class="notice-text">
              {{ mergedNotice(item.notice) }}
            </DouText>
            <DouText v-if="item.notice.undone" variant="tertiary" size="sm">
              {{ t('notice.undone') }}
            </DouText>
            <!-- Only its last merge, made here, can be taken back. -->
            <DouButton
              v-else-if="
                thread && !item.notice.pullRequest && thread.merged?.sha === item.notice.sha
              "
              size="sm"
              variant="ghost"
              :loading="undoing"
              @click="undoLastMerge"
            >
              {{ t('notice.undo') }}
            </DouButton>
          </template>
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
          <ArtifactFeedbackNotice
            v-else-if="item.notice.kind === 'artifact-feedback'"
            :notice="item.notice"
          />
          <TurnChanges v-else-if="item.notice.kind === 'turn-changes'" :notice="item.notice" />
          <!-- A change the coordinator made to the plan without asking, to undo from here. -->
          <template v-else-if="item.notice.kind === 'plan-changed'">
            <DouText variant="tertiary" size="sm" class="notice-text">
              {{ planNotice(item.notice) }}
            </DouText>
            <PlanUndoButton
              v-if="revisionOf(item.notice.revisionId)"
              :revision="revisionOf(item.notice.revisionId)!"
            />
          </template>
          <DouText
            v-else-if="item.notice.kind === 'thread-auto-started'"
            variant="tertiary"
            size="sm"
            class="notice-text"
          >
            {{ t('notice.autoStarted') }}
            <DouLink
              underline="hover"
              @click="(event: MouseEvent) => openThreadFrom(event, threadIdOf(item.notice))"
            >
              {{ titleOf(item.notice) }}
            </DouLink>
          </DouText>
          <ReviewNotice v-else-if="item.notice.kind === 'review'" :notice="item.notice" />
          <RecapNotice v-else-if="item.notice.kind === 'recap'" :notice="item.notice" />
          <DouText
            v-else-if="item.notice.kind === 'routine-change'"
            variant="tertiary"
            size="sm"
            class="notice-text"
          >
            {{
              t(item.notice.action === 'added' ? 'notice.routineAdded' : 'notice.routineRemoved', {
                prompt: item.notice.prompt,
              })
            }}
          </DouText>
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
            :highlighted="focused === message.id"
            :class="{ 'conversation-edited': highlighted.has(message.id) }"
            actions-visible="hover"
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
              <!-- A workspace path becomes a stored file only once posted: none while written. -->
              <MessageImage
                v-if="message.id !== writing?.id || /^([a-z][a-z0-9+.-]*:|\/\/)/i.test(src ?? '')"
                :src="src"
                :alt="alt"
                :title="title"
              />
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
              <TurnChanges
                v-else-if="part.type === 'changes' && shown.changes.has(message.id)"
                :notice="shown.changes.get(message.id)!"
                class="turn-changes"
              />
            </template>
            <template v-if="message.editedAt" #trailing>
              <!-- Right after the text, as chat apps do: why it changed and what it said before. -->
              {{ ' ' }}
              <DouPopover trigger="click" :title="t('message.editedTitle')" :width="360">
                <DouLink class="edited-mark" underline="hover">{{ t('message.edited') }}</DouLink>
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
        v-model:open="todoOpen"
        :live="working"
        class="todo"
      />
    </DouChatList>
    <div v-else class="conversation-empty">
      <DouEmpty v-if="props.threadId" :title="thread?.title" :description="t('thread.empty')" />
      <GettingStarted v-else-if="!started" @open-settings="openSettings()" />
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
        <span class="conversation-elapsed">{{ elapsed }}</span>
      </DouText>
      <div class="conversation-status-end">
        <DouText v-if="escArmed" variant="tertiary" size="sm" role="status">
          {{ t('conversation.escAgain') }}
        </DouText>
        <DouText v-if="fallback" variant="tertiary" size="sm" class="conversation-fallback">
          {{ t('conversation.fallback', { model: fallback }) }}
        </DouText>
      </div>
    </div>
    <div v-else-if="queued" class="conversation-status" role="status">
      <DouText variant="secondary" size="sm">{{ t('conversation.queued') }}</DouText>
    </div>
    <DouAlert v-else-if="failure" tone="danger" class="conversation-alert">
      {{ tm(failure) }}
      <template #action>
        <DouButton v-if="failure.key === 'error.noModel'" size="sm" @click="openSettings()">
          {{ t('conversation.openSettings') }}
        </DouButton>
        <DouButton size="sm" variant="outline" @click="retry">
          <template #leading><RotateCwIcon /></template>
          {{ t('conversation.retry') }}
        </DouButton>
      </template>
    </DouAlert>
    <DouAlert v-else-if="cancelled || silent" tone="neutral" class="conversation-alert">
      {{ t(cancelled ? 'conversation.cancelled' : 'conversation.silent') }}
      <template #action>
        <DouButton size="sm" variant="outline" @click="retry">
          <template #leading><PlayIcon /></template>
          {{ t('conversation.continue') }}
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
    <div
      v-if="state.detail"
      class="conversation-input"
      data-message-input
      @keydown="onInputKeydown"
    >
      <DouChatInput
        ref="input"
        v-model="draft"
        :submit-on="state.input.submitOn"
        :commands="commands"
        :mentions="mentions.options.value"
        @command="useSnippet"
        @mention="mentions.pick"
        :placeholder="props.threadId ? t('thread.placeholder') : t('coordinator.placeholder')"
        :disabled="!state.detail"
        :can-submit="canSubmit"
        :loading="working"
        @submit="submit"
        @stop="stop"
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
          <ContextUsage
            v-if="view.model && view.conversation?.contextTokens"
            :used="view.conversation.contextTokens"
            :total="view.model.compactAt"
            :compactions="view.conversation.compactions"
            :usage="view.conversation.usage"
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

/* A recap with nothing left to tell draws nothing, and leaves no gap either. */
.notice:empty {
  display: none;
}

/* Under the reply's text, lined up with it: the button's own padding sits outside. */
.turn-changes {
  align-self: flex-start;
  margin-inline-start: -8px;
}

.notice-text {
  overflow-wrap: anywhere;
}

/* A quiet mark after the text; on touch screens it takes taps from 40px around it. */
.edited-mark {
  --dou-link-tone: var(--dou-color-text-tertiary);
  position: relative;
  font-size: var(--dou-font-size-sm);
  font-weight: inherit;
}

@media (pointer: coarse) {
  .edited-mark::after {
    position: absolute;
    inset: min(0px, calc(50% - 20px));
    content: '';
  }
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

/* The menu keeps its corner however far the rest wraps. */
.conversation-meta {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  min-width: 0;
}

/* With no branch, review or menu to show, the bar is not drawn at all. */
.conversation-header:has(.conversation-meta-main:empty):not(:has(.conversation-tools)) {
  display: none;
}

.conversation-meta-main {
  display: flex;
  flex: 1;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 8px;
  min-width: 0;
}

/* The first line is as tall as the menu beside it, so a lone chip centres on it. */
.conversation-meta-main::before {
  content: '';
  height: 28px;
  margin-inline-end: -8px;
}

.conversation-list {
  --dou-chat-list-gap: 8px;
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

.conversation-elapsed {
  flex: none;
  color: var(--dou-color-text-tertiary);
  font-variant-numeric: tabular-nums;
}

.conversation-input {
  padding: 12px 16px 16px;
}

.conversation-tools {
  display: flex;
  align-items: center;
  flex: none;
  gap: 4px;
  margin-left: auto;
}

.conversation-model {
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.earlier {
  display: flex;
  justify-content: center;
  padding: 4px 0;
}

/* A folded message from the coordinator the search led to, marked like a chat message. */
.relay.conversation-focused {
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
