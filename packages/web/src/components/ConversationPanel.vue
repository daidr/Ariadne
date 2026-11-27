<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouChatAttachments,
  DouChatInput,
  DouChatPlan,
  DouChatMessage,
  DouCollapse,
  DouCollapseItem,
  DouDropdown,
  DouDropdownItem,
  DouDropdownRadioGroup,
  DouDropdownSub,
  DouEmpty,
  DouImagePreview,
  DouInput,
  DouLink,
  DouPopover,
  DouText,
  type ChatAttachment,
  type ChatInputOption,
  type ChatMessageProps,
} from 'dou-ui'
import type { ChangeTotals, MessageView, NoticeView, ReplyDraftBlock } from '@ariadne/core'
import {
  CheckCircleIcon,
  ChevronDownIcon,
  CpuIcon,
  ListTreeIcon,
  MoreHorizontalIcon,
  PaperclipIcon,
  PauseIcon,
  PlayIcon,
  RotateCcwIcon,
  RotateCwIcon,
  ScanSearchIcon,
} from '@dou-ui/icons'
import {
  computed,
  inject,
  nextTick,
  onBeforeUnmount,
  provide,
  ref,
  useTemplateRef,
  watch,
  watchEffect,
} from 'vue'
import { openSettingsKey } from '../open-settings'
import { api, type Effort } from '../api'
import { effortsOf, effortWithin } from '../effort'
import { openFilePreview } from '../files/preview'
import { relayIdOf, shownAs } from '../relays'
import RelayedTo from './RelayedTo.vue'
import MessageQuote from './MessageQuote.vue'
import { commonEmojis, shownReactions } from './reactions'
import { clockText, t, tm } from '../i18n'
import { placeTodo, todoPlan } from './todo'
import { isInlineImage, shownAttachments, useAttachments } from './attachments'
import {
  composerFor,
  coordinatorComposerFor,
  emptyConversation,
  loadEarlier,
  openMain,
  openView as openFrom,
  type OpenedView,
  openThreadFrom,
  react,
  renderKeyOf,
  retryTurn,
  sendMessage,
  state,
  reopenThread,
  resumeThread,
  removeThreadReference,
  keepThreadReference,
  setThreadModel,
  stopTurn,
  threadsById,
  undoMerge,
  undoNotice,
} from '../store'
import { offerNotifications } from '../notify'
import ContextUsage from './ContextUsage.vue'
import ReviewNotice from '../review/ReviewNotice.vue'
import { useSkills } from '../review/skills'
import LineComments from './LineComments.vue'
import ConversationActivity from './ConversationActivity.vue'
import ConversationLog from './ConversationLog.vue'
import { askDraftsKey, type AskDraft } from '../blocks/ask-drafts'
import ConversationBar from './ConversationBar.vue'
import ConversationMarker from './ConversationMarker.vue'
import { messageTimes } from './conversation-timeline'
import { useCoordinatorUnread } from './coordinator-unread'
import { hasOpenBranch } from '../review/merge-offered'
import TurnChanges from './TurnChanges.vue'
import { useMentions } from './mentions'
import { elapsedText } from './command-text'
import ArtifactFeedbackNotice from './ArtifactFeedbackNotice.vue'
import ArtifactNotice from './ArtifactNotice.vue'
import RecapNotice from './RecapNotice.vue'
import AskBlock from '../blocks/AskBlock.vue'
import DraftBlock from '../blocks/DraftBlock.vue'
import ShowBlock from '../blocks/ShowBlock.vue'
import AgentText from '../markdown/AgentText.vue'
import MessageImage from '../markdown/MessageImage.vue'
import MessageLink from '../markdown/MessageLink.vue'
import { isBare, sanitizeUrl } from '../markdown/links'
import { lastPanel, registerPanel } from '../shortcuts'
import { stepHistory, type Recall } from './input-history'
import { provideFromThread } from '../opened-from'
import { reconcilePendingMessages } from '../optimistic-messages'

/** `threadId` shows that thread's conversation; otherwise the coordinator's. */
const props = withDefaults(defineProps<{ threadId?: string; active?: boolean }>(), {
  active: true,
})
const active = computed(() => props.active)

// What opens from a thread shows full screen, with this thread beside it.
provideFromThread(() => props.threadId ?? null)
const openView = (view: OpenedView) => openFrom(view, { from: props.threadId ?? null })
const openSettings = inject(openSettingsKey, () => {})

// Unsent input is kept in the store, so opening trace/plan/artifacts or moving tabs keeps it.
const projectId = state.currentId ?? state.detail?.project.id ?? null
const composer = props.threadId
  ? composerFor(props.threadId)
  : projectId
    ? coordinatorComposerFor(projectId)
    : null
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

// Images in the same message can be browsed together.
const preview = ref<{ open: boolean; images: string[]; index: number }>({
  open: false,
  images: [],
  index: 0,
})
function openAttachment(message: MessageView, file: ChatAttachment) {
  if (!file.uid) return
  if (!isInlineImage(file.type)) {
    openFilePreview({ id: String(file.uid), name: file.name, mime: file.type, size: file.size })
    return
  }
  if (!file.url) return
  const images = shownAttachments(message.attachments).filter(
    (item) => item.url && isInlineImage(item.type),
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
/** The source the user allowed this thread alone on the card of that ask, while it has it. */
const ownReference = (attentionId: string) =>
  thread.value?.references.find((reference) => reference.attentionId === attentionId) ?? null
/** What became of the source of that ask once the user took it from the thread. */
const endedReference = (attentionId: string) =>
  thread.value?.endedReferences.findLast((ended) => ended.attentionId === attentionId) ?? null
const thread = computed(() =>
  props.threadId ? (threadsById.value.get(props.threadId) ?? null) : null,
)
/** For a reviewing thread: the thread whose changes it reviews. */
const reviewed = computed(() => {
  const id = thread.value?.reviewOf
  return id ? (threadsById.value.get(id) ?? null) : null
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
const createdArtifacts = computed(() =>
  state.artifacts.filter((artifact) => artifact.createdBy === conversationId.value),
)
const backfilledChanges = ref<{ threadId: string; totals: ChangeTotals } | null>(null)
const attemptedBackfill = new Set<string>()
let backfillController: AbortController | null = null
let backfillKey: string | null = null
const abortBackfill = () => {
  if (!backfillController) return
  if (backfillKey !== null) {
    attemptedBackfill.delete(backfillKey)
    backfillKey = null
  }
  backfillController.abort()
  backfillController = null
}
onBeforeUnmount(abortBackfill)
watch(
  () => {
    const owner = reviewed.value ?? thread.value
    if (!hasOpenBranch(owner) || owner!.changes !== null) return null
    return `${owner!.id}\0${owner!.pullRequest?.headSha ?? ''}`
  },
  (key) => {
    abortBackfill()
    if (!key || attemptedBackfill.has(key)) return
    attemptedBackfill.add(key)
    const threadId = key.slice(0, key.indexOf('\0'))
    const controller = new AbortController()
    backfillController = controller
    backfillKey = key
    void api
      .getChanges(threadId, undefined, controller.signal)
      .then((result) => {
        if (controller.signal.aborted) return
        const repoList =
          result.repos && result.repos.length > 0
            ? result.repos.map((repo) => repo.changes)
            : result.changes
              ? [result.changes]
              : []
        if (!repoList.length) return
        const allFiles = repoList.flatMap((entry) => entry.files)
        if (allFiles.length === 0 && repoList.some((entry) => entry.uncommitted.length > 0)) return
        const totals = allFiles.reduce(
          (acc, file) => ({
            files: acc.files + 1,
            added: acc.added + (file.added ?? 0),
            removed: acc.removed + (file.removed ?? 0),
          }),
          { files: 0, added: 0, removed: 0 },
        )
        backfilledChanges.value = { threadId, totals }
      })
      .catch(() => {
        // Keep the pull-request fallback capsule if branch changes cannot be read right now.
      })
      .finally(() => {
        if (backfillController === controller) {
          backfillController = null
          backfillKey = null
        }
      })
  },
  { immediate: true },
)
const capsuleChanges = computed(() => {
  const owner = reviewed.value ?? thread.value
  if (!hasOpenBranch(owner)) return null
  const totals =
    owner!.changes ??
    (backfilledChanges.value?.threadId === owner!.id ? backfilledChanges.value.totals : null)
  if (totals) {
    return totals.files > 0 ? { threadId: owner!.id, totals } : null
  }
  return owner!.pullRequest ? { threadId: owner!.id, totals: null } : null
})
const showCapsules = computed(
  () =>
    !!capsuleChanges.value ||
    createdArtifacts.value.length > 0 ||
    background.value.length > 0 ||
    !!thread.value?.pullRequest,
)
const wait = computed(() => (working.value ? null : (view.value.conversation?.wait ?? null)))
const reviewing = computed(() => (thread.value?.review?.reviewing ? thread.value.review : null))
// What it is doing, shown after the last message: what the model said beside its latest tool
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
// The model it uses now; a thread's picker also offers choosing thinking effort and returning to default.
function formatModelEffort(label: string, effort: Effort | null | undefined): string {
  return effort ? `${label} · ${t(`settings.effort.${effort}`)}` : label
}
const modelChoices = computed(() =>
  state.models.map((model) => ({
    ...model,
    acceptedEfforts: effortsOf(model.efforts),
  })),
)
const canSwitchModel = computed(
  () =>
    !!thread.value &&
    (modelChoices.value.length > 1 ||
      (modelChoices.value.length === 1 && modelChoices.value[0]!.acceptedEfforts.length > 0)),
)
const modelLabel = computed(() => {
  const id = view.value.model?.id
  const model = state.models.find((item) => item.id === id)
  if (!model) return null
  return formatModelEffort(model.label, view.value.model?.effort ?? null)
})
const defaultLabel = computed(() => {
  const projectThread = state.detail?.project.settings?.models?.thread
  const projectModel = projectThread?.model
    ? state.models.find((item) => item.id === projectThread.model)
    : undefined
  const model = projectModel ?? state.models.find((item) => item.id === state.threadDefault)
  if (!model) return ''
  const rawEffort = projectModel
    ? (projectThread?.effort as Effort | null | undefined)
    : state.threadDefaultEffort
  const defaultEffort = rawEffort ? effortWithin(rawEffort, effortsOf(model.efforts)) : null
  return formatModelEffort(model.label, defaultEffort)
})
// A model since removed from the settings falls back to the default, so that is shown.
const pickedModelId = computed(() => {
  const id = thread.value?.model
  return id && state.models.some((model) => model.id === id) ? id : ''
})
const pickedEffort = computed<Effort | ''>(() => {
  if (!pickedModelId.value) return ''
  const model = modelChoices.value.find((item) => item.id === pickedModelId.value)
  const raw = thread.value?.effort as Effort | null | undefined
  if (!model || !raw) return ''
  return effortWithin(raw, model.acceptedEfforts) ?? ''
})
const pickedEffortLabel = computed(() =>
  pickedEffort.value ? t(`settings.effort.${pickedEffort.value}`) : t('settings.effort.default'),
)
function encodeModelChoice(modelId: string, effort: Effort | ''): string {
  return JSON.stringify([modelId, effort])
}
const pickedChoice = computed(() =>
  pickedModelId.value ? encodeModelChoice(pickedModelId.value, pickedEffort.value) : '',
)
function pickModel(value: string | number | undefined) {
  if (!thread.value) return
  if (!value) {
    void setThreadModel(thread.value.id, null, null)
    return
  }
  try {
    const [modelId, effort] = JSON.parse(String(value)) as [string, Effort | '']
    if (typeof modelId === 'string' && modelId) {
      void setThreadModel(thread.value.id, modelId, effort || null)
    }
  } catch {
    // Ignore malformed choice values.
  }
}
const todo = computed(() => todoPlan(view.value.todo, working.value))
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
// Completed discussions hide the composer until the user explicitly reopens them.
const resolved = computed(() => !!props.threadId && thread.value?.stage === 'done')
const canReopen = computed(() => resolved.value && !thread.value?.reviewOf)
const reopeningIds = ref(new Set<string>())
const reopening = computed(() => {
  const id = thread.value?.id
  return !!id && reopeningIds.value.has(id)
})
async function reopen() {
  const id = thread.value?.id
  if (!id || !canReopen.value || reopeningIds.value.has(id)) return
  const next = new Set(reopeningIds.value)
  next.add(id)
  reopeningIds.value = next
  try {
    const ok = await reopenThread(id)
    if (ok && thread.value?.id === id) {
      await nextTick()
      input.value?.focus()
    }
  } finally {
    const remaining = new Set(reopeningIds.value)
    remaining.delete(id)
    reopeningIds.value = remaining
  }
}

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
    cites: [],
    relayedTo: [],
    replyTo: null,
    reactions: [],
    createdAt: reply.startedAt,
    editedAt: null,
    editReason: null,
    originalContent: null,
    editedSinceRead: false,
  }
})

const recap = computed(() => view.value.notices.findLast((notice) => notice.kind === 'recap')?.id)
const pendingEntries = computed(() => {
  const id = conversationId.value
  return id ? (state.pendingMessages[id] ?? []) : []
})
const reconciledPending = computed(() =>
  reconcilePendingMessages(pendingEntries.value, view.value.messages),
)
function messageRenderKey(id: string): string {
  return reconciledPending.value.matchedKeys.get(id) ?? renderKeyOf(id)
}

// Messages and notices in the order they happened. A worktree removed after a merge goes
// unsaid: the merge's own line says what happened.
const timeline = computed(() =>
  [
    ...view.value.messages.flatMap(shownAs).map((message) => ({
      id: message.id,
      at: message.createdAt,
      message,
      notice: null,
      pending: false,
    })),
    ...reconciledPending.value.visible.map((entry) => ({
      id: entry.clientId,
      at: entry.message.createdAt,
      message: entry.message,
      notice: null,
      pending: true,
    })),
    ...view.value.notices
      // Only a removal the user made is worth a line: the others follow from what they see.
      .filter((notice) => notice.kind !== 'worktree-removed' || notice.reason === 'user')
      // Hide withdrawn artifact feedback notices once rolled back before consumption.
      .filter((notice) => notice.kind !== 'artifact-feedback' || !notice.withdrawn)
      // A newer recap takes the place of the one before it.
      .filter((notice) => notice.kind !== 'recap' || notice.id === recap.value)
      .map((notice) => ({
        id: notice.id,
        at: notice.createdAt,
        message: null,
        notice,
        pending: false,
      })),
    ...(writing.value
      ? [
          {
            id: writing.value.id,
            at: writing.value.createdAt,
            message: writing.value,
            notice: null,
            pending: false,
          },
        ]
      : []),
  ].toSorted((a, b) => a.at - b.at),
)

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
const entries = computed(() => [
  ...placeTodo(
    shown.value.items,
    view.value.todo,
    view.value.more ? view.value.messages[0] : undefined,
  ),
  { id: 'conversation-activity', activity: true as const },
])
const entryKey = (item: (typeof entries.value)[number]) =>
  'message' in item && item.message ? messageRenderKey(item.id) : item.id
const times = computed(() => messageTimes(shown.value.items))
const askDrafts = composer?.askDrafts ?? new Map<string, AskDraft>()
provide(askDraftsKey, askDrafts)
if (!composer) watch(conversationId, () => askDrafts.clear())

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
// Narrowed where they are called; the template cannot narrow inside a handler.
const threadIdOf = (notice: NoticeView) =>
  notice.kind === 'thread-auto-started' ? notice.threadId : ''
const titleOf = (notice: NoticeView) =>
  notice.kind === 'thread-auto-started'
    ? (threadsById.value.get(notice.threadId)?.title ?? notice.title)
    : ''

function memoryNotice(notice: Extract<NoticeView, { kind: 'memory' | 'memory-everywhere' }>) {
  if (notice.kind === 'memory-everywhere') {
    return t(notice.action === 'saved' ? 'notice.everywhereAdded' : 'notice.everywhereRemoved', {
      text: notice.text,
    })
  }
  if (notice.action === 'removed') return t('notice.memoryRemoved', { text: notice.text })
  if (notice.previous === null) return t('notice.memoryAdded', { text: notice.text })
  return t('notice.memoryChanged', { previous: notice.previous, text: notice.text })
}

/** The files sent with it, the text, then tables, charts and steps, then what it asks. */
function partsOf(message: MessageView): ChatMessageProps['parts'] {
  // Still being written while it is the draft, not yet a message of the log.
  const streaming =
    message.id === writing.value?.id && !state.writing[message.conversationId]?.posted
  // The user's files lead, as they were sent before the words; an agent's follow its text.
  const files = message.attachments.length ? [{ type: 'attachments' }] : []
  const own = message.author === 'user'
  return [
    ...(message.replyTo ? [{ type: 'quote' }] : []),
    ...(own ? files : []),
    ...(message.content ? [{ type: 'text' as const, text: message.content, streaming }] : []),
    ...(own ? [] : files),
    ...(message.comments.length ? [{ type: 'comments' }] : []),
    ...(message.id === writing.value?.id
      ? (state.writing[message.conversationId]?.blocks ?? []).map((block) => ({
          type: 'draft-block',
          block,
        }))
      : []),
    ...message.blocks.map((_, index) => ({ type: 'block', index })),
    ...(message.ask ? [{ type: 'ask' }] : []),
    ...(shown.value.changes.has(message.id) ? [{ type: 'changes' }] : []),
    ...(message.relayedTo.length ? [{ type: 'relayed' }] : []),
  ]
}

/**
 * Whether the user can put emojis on it: an agent's message of the log, not the one still
 * being written. Their own words (a relay's bubbles too) only show what agents put there.
 */
function reactable(message: MessageView): boolean {
  return message.author !== 'user' && message.kind !== 'relay' && message.id !== writing.value?.id
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
  const request = sendMessage(
    id,
    value,
    {
      attachments: files.attachments,
      mentions: [...mentions.inText(value), ...skills.inText(value)],
    },
    { attachments: files.previews },
  )
  void nextTick(() => list.value?.scrollToBottom())
  const sent = await request
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
watchEffect(
  (cleanup) => {
    if (!active.value) return
    cleanup(
      registerPanel(kind, {
        focus: () => input.value?.focus(),
        stop: escape,
      }),
    )
  },
  { flush: 'post' },
)

// ↑ in an empty input brings back what the user sent here before, newest first.
const sent = computed(() =>
  [...view.value.messages, ...reconciledPending.value.visible.map((entry) => entry.message)]
    .filter((message) => message.author === 'user' && message.content.trim())
    .map((message) => message.content)
    .toReversed(),
)
let recall: Recall | null = null
watch(conversationId, () => (recall = null))

function onInputKeydown(event: KeyboardEvent) {
  if (resolved.value) return
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
    void nextTick(() => {
      const el = input.value?.textarea
      el?.setSelectionRange(named.length + 1, named.length + 1)
    })
    return
  }
  const snippet = state.input.snippets.find((item) => item.id === option.value)
  if (!snippet) return
  draft.value = snippet.text + draft.value
  void nextTick(() => {
    const el = input.value?.textarea
    el?.setSelectionRange(snippet.text.length, snippet.text.length)
  })
}

function retry() {
  if (conversationId.value) void retryTurn(conversationId.value)
}

// Scrolled to the top: the page before. A first page too short to scroll fetches the
// next one by itself, since there is no top to reach.
const list = useTemplateRef<{
  viewport: HTMLElement | null
  scrollToBottom(behavior?: ScrollBehavior): void
  scrollToMessage(id: string): void
}>('list')
const unread = props.threadId
  ? undefined
  : useCoordinatorUnread(
      () => view.value,
      () => list.value?.viewport,
      () => active.value && !state.full && state.currentId === view.value.conversation?.projectId,
    )
const newMessagesAt = computed(() => unread?.boundary.value)
const footer = useTemplateRef<HTMLElement>('footer')
const footerHeight = ref(0)
watch(
  footer,
  (element, _, cleanup) => {
    if (!element) {
      footerHeight.value = 0
      return
    }
    const measure = () => (footerHeight.value = element.getBoundingClientRect().height)
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(measure)
    observer.observe(element, { box: 'border-box' })
    cleanup(() => observer.disconnect())
  },
  { flush: 'post' },
)
function earlier() {
  if (active.value && conversationId.value && view.value.more)
    void loadEarlier(conversationId.value)
}
watch(
  [list, active, () => view.value.more, () => view.value.messages.length],
  ([chat, visible, more], _, cleanup) => {
    const viewport = chat?.viewport
    if (!visible || !more || !viewport) return
    const loadIfShort = () => {
      if (
        !view.value.loadingEarlier &&
        viewport.clientHeight &&
        viewport.scrollHeight <= viewport.clientHeight
      ) {
        earlier()
      }
    }
    loadIfShort()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(loadIfShort)
    observer.observe(viewport)
    // The estimates can initially overflow even when the measured messages fit.
    if (viewport.firstElementChild) observer.observe(viewport.firstElementChild)
    cleanup(() => observer.disconnect())
  },
  { flush: 'post' },
)

// A message the search found: scroll to it and mark it for a moment. A message from the
// coordinator to a thread is folded, so it opens.
const focused = ref<string | null>(null)
const openRelays = ref<string[]>([])
let unmark: ReturnType<typeof setTimeout> | undefined
onBeforeUnmount(() => clearTimeout(unmark))
watch(
  [() => state.focus, active, list, conversationId],
  async ([focus, visible, chat, id]) => {
    if (!visible || !chat || !focus || focus.conversationId !== id) return
    state.focus = null
    const message = view.value.messages.find((item) => item.id === focus.messageId)
    if (message?.kind === 'relay' && !openRelays.value.includes(message.id)) {
      openRelays.value = [...openRelays.value, message.id]
    }
    await nextTick()
    if (list.value !== chat || conversationId.value !== id) return
    chat.scrollToMessage(messageRenderKey(focus.messageId))
    focused.value = focus.messageId
    clearTimeout(unmark)
    unmark = setTimeout(() => (focused.value = null), 2000)
  },
  { flush: 'post' },
)

// Threads keep their read behavior. The coordinator tracks the visible tail.
let lastRead = ''
watch([() => view.value.messages.at(-1)?.id, active], ([last, visible]) => {
  if (!visible || !last || !props.threadId || last === lastRead) return
  lastRead = last
  void api.markRead(props.threadId, last).catch(() => {
    if (lastRead === last) lastRead = ''
  })
})
</script>

<template>
  <div
    class="conversation"
    :style="{ '--conversation-footer-height': footerHeight ? `${footerHeight}px` : undefined }"
    :data-panel="kind"
    @focusin="lastPanel = kind"
    @pointerdown="lastPanel = kind"
  >
    <ConversationLog
      v-if="timeline.length || todo.length || working || queued || wait || reviewing"
      :key="conversationId ?? undefined"
      ref="list"
      class="conversation-list"
      :items="entries"
      :item-key="entryKey"
      :loading-earlier="view.loadingEarlier"
      :bottom-inset="footerHeight || undefined"
      :bottom-fade="Boolean(state.detail)"
      @bottom-change="unread?.onBottomChange($event)"
      @earlier="earlier"
    >
      <template #default="{ item }">
        <ConversationActivity
          v-if="'activity' in item"
          :conversation-id="conversationId"
          :working="working && !thread?.stopped"
          :queued="queued && !thread?.stopped"
          :live="live"
          :activity="activity"
          :elapsed="elapsed"
          :esc-armed="escArmed"
          :wait="wait"
          :reviewing="reviewing"
        />
        <DouChatPlan
          v-else-if="'todo' in item"
          :items="todo"
          :title="t('todo.title')"
          :show-header="false"
          :live="working"
        />
        <div v-else-if="item.notice" class="notice">
          <AskBlock
            v-if="item.notice.kind === 'approval'"
            :attention-id="item.notice.id"
            :block="item.notice.block"
            :answer="item.notice.answer"
            :withdrawn="item.notice.withdrawn"
          />
          <template
            v-else-if="item.notice.kind === 'memory' || item.notice.kind === 'memory-everywhere'"
          >
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
            {{ t('notice.worktreeRemovedUser') }}
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
          <DouText
            v-else-if="item.notice.kind === 'plan-changed'"
            variant="tertiary"
            size="sm"
            class="notice-text"
          >
            {{ planNotice(item.notice) }}
          </DouText>
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
        <div
          v-for="message in item.message ? [item.message] : []"
          v-else
          :key="messageRenderKey(message.id)"
        >
          <ConversationMarker
            v-if="message.id === newMessagesAt || times.has(message.id)"
            :at="message.createdAt"
            :unread="message.id === newMessagesAt"
          />
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
            :status="item.pending ? 'sending' : undefined"
            :data-message-id="message.id"
            :data-pending="item.pending ? '' : undefined"
            :highlighted="!!focused && focused === relayIdOf(message.id)"
            :reactions="shownReactions(message.reactions)"
            :reactable="reactable(message)"
            actions-visible="hover"
            @react="(emoji, item) => react(message.id, emoji, !!item?.reacted)"
          >
            <template v-if="reactable(message)" #reaction-picker="{ react: pick }">
              <div class="reaction-picker">
                <DouButton
                  v-for="emoji in commonEmojis"
                  :key="emoji"
                  variant="ghost"
                  size="sm"
                  :aria-label="t('message.reactWith', { emoji })"
                  @click="pick(emoji)"
                >
                  {{ emoji }}
                </DouButton>
              </div>
            </template>
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
              <MessageQuote
                v-if="part.type === 'quote' && message.replyTo"
                :quote="message.replyTo"
                :conversation-id="message.conversationId"
              />
              <DouChatAttachments
                v-else-if="part.type === 'attachments'"
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
              >
                <!-- A source allowed for this thread alone can be taken back or kept. -->
                <template v-if="ownReference(message.ask.attentionId)" #after>
                  <span class="own-reference">
                    <DouLink
                      underline="hover"
                      @click="
                        removeThreadReference(threadId!, ownReference(message.ask.attentionId)!.id)
                      "
                      >{{ t('reference.own.revoke') }}</DouLink
                    >
                    <DouLink
                      underline="hover"
                      @click="
                        keepThreadReference(threadId!, ownReference(message.ask.attentionId)!.id)
                      "
                      >{{ t('reference.own.keep') }}</DouLink
                    >
                  </span>
                </template>
                <template v-else-if="endedReference(message.ask.attentionId)" #after>
                  <DouText variant="tertiary" size="sm">{{
                    t(
                      endedReference(message.ask.attentionId)!.kept
                        ? 'reference.own.kept'
                        : 'reference.own.revoked',
                    )
                  }}</DouText>
                </template>
              </AskBlock>
              <LineComments
                v-else-if="part.type === 'comments'"
                :comments="message.comments"
                :thread-id="message.conversationId"
              />
              <ShowBlock
                v-else-if="part.type === 'block'"
                :block="message.blocks[part.index as number]!"
              />
              <DraftBlock
                v-else-if="part.type === 'draft-block'"
                :block="part.block as ReplyDraftBlock"
              />
              <RelayedTo v-else-if="part.type === 'relayed'" :relays="message.relayedTo" />
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
        </div>
      </template>
    </ConversationLog>
    <div v-else class="conversation-empty">
      <DouEmpty v-if="props.threadId" :title="thread?.title" :description="t('thread.empty')" />
      <DouEmpty v-else-if="state.modelReady === false" :description="t('error.noModel')">
        <DouButton size="sm" tone="primary" @click="openSettings()">
          {{ t('conversation.openSettings') }}
        </DouButton>
      </DouEmpty>
      <DouEmpty v-else :title="t('coordinator.title')" :description="t('coordinator.empty')" />
    </div>

    <DouImagePreview
      v-model:open="preview.open"
      v-model:index="preview.index"
      :images="preview.images"
    />
    <div
      v-if="state.detail || thread?.stopped || failure || cancelled || silent"
      ref="footer"
      class="conversation-footer"
    >
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

      <div
        v-if="state.detail"
        class="conversation-input"
        data-message-input
        @keydown="onInputKeydown"
      >
        <ConversationBar
          v-if="conversationId && showCapsules"
          :key="conversationId"
          :conversation-id="conversationId"
          :changes="capsuleChanges"
          :artifacts="createdArtifacts"
          :background="background"
          :thread="thread"
          class="conversation-capsules"
        />
        <div v-if="resolved" class="conversation-resolved" data-thread-resolved>
          <div class="conversation-resolved-status">
            <ScanSearchIcon
              v-if="thread?.reviewOf"
              class="conversation-resolved-icon is-review"
              aria-hidden="true"
            />
            <CheckCircleIcon v-else class="conversation-resolved-icon" aria-hidden="true" />
            <DouText variant="secondary" size="sm" class="conversation-resolved-text">
              {{ t(thread?.reviewOf ? 'review.ended' : 'thread.resolved') }}
            </DouText>
          </div>
          <DouButton
            v-if="canReopen"
            size="sm"
            variant="outline"
            class="conversation-resolved-action"
            :loading="reopening"
            @click="reopen"
          >
            <template #leading><RotateCcwIcon /></template>
            {{ t('thread.reopen') }}
          </DouButton>
        </div>
        <DouChatInput
          v-else
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
            <!-- Which model & effort answers here, and how full its context is. -->
            <DouDropdown v-if="canSwitchModel" placement="top-start">
              <DouButton variant="ghost" size="sm" :title="t('thread.model')">
                <template #leading><CpuIcon /></template>
                <span class="conversation-model">{{ modelLabel ?? t('thread.noModel') }}</span>
                <template #trailing><ChevronDownIcon /></template>
              </DouButton>
              <template #menu>
                <DouDropdownRadioGroup :model-value="pickedChoice" @update:model-value="pickModel">
                  <DouDropdownItem value="">
                    {{ t('thread.modelDefault', { label: defaultLabel }) }}
                  </DouDropdownItem>
                  <template v-for="model in modelChoices" :key="model.id">
                    <DouDropdownSub v-if="model.acceptedEfforts.length" :title="model.label">
                      <template #title>
                        <span class="model-option-title">
                          <span class="model-option-name">{{ model.label }}</span>
                          <span v-if="pickedModelId === model.id" class="model-option-effort">
                            {{ pickedEffortLabel }}
                          </span>
                        </span>
                      </template>
                      <DouDropdownItem :value="encodeModelChoice(model.id, '')">
                        {{ t('settings.effort.default') }}
                      </DouDropdownItem>
                      <DouDropdownItem
                        v-for="effort in model.acceptedEfforts"
                        :key="effort"
                        :value="encodeModelChoice(model.id, effort)"
                      >
                        {{ t(`settings.effort.${effort}`) }}
                      </DouDropdownItem>
                    </DouDropdownSub>
                    <DouDropdownItem v-else :value="encodeModelChoice(model.id, '')">
                      {{ model.label }}
                    </DouDropdownItem>
                  </template>
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
  user-select: text;
  -webkit-user-select: text;
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
  user-select: text;
  -webkit-user-select: text;
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

/* Shown while the pointer is on the card, like a message's own actions. */
.own-reference {
  display: inline-flex;
  gap: 12px;
  font-size: var(--dou-font-size-sm);
  opacity: 0;
  transition: opacity 0.15s;
}

.ask-block:hover .own-reference,
.own-reference:focus-within {
  opacity: 1;
}

@media (hover: none) {
  .own-reference {
    opacity: 1;
  }
}

.conversation {
  /* safe-area-inset-bottom (e.g. 34px on iOS) already clears the Home Indicator with
     built-in breathing room; adding 16px on top stacks two bottom gaps (50px). */
  --conversation-footer-padding: max(16px, var(--ariadne-safe-bottom));
  position: relative;
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.conversation-list :deep(.dou-chat-list) {
  --dou-chat-list-gap: 30px;
}

/*
 * Chat reading rhythm: wrapped lines < list items < paragraphs < heading top < consecutive messages.
 * Suppressing the top margin of the block right after a heading keeps headings bound to their body.
 */
.conversation-list :deep(.dou-markdown) {
  --dou-markdown-gap: 0.95em;
  line-height: 1.75;
}

.conversation-list :deep(.dou-markdown :where(h1, h2, h3, h4, h5, h6)) {
  margin-block: 0;
  line-height: 1.45;
}

.conversation-list :deep(.dou-markdown :where(h1, h2):not(:first-child)) {
  margin-block-start: 1.65em;
}

.conversation-list :deep(.dou-markdown :where(h1, h2):not(:last-child)) {
  margin-block-end: 0.45em;
}

.conversation-list :deep(.dou-markdown :where(h3):not(:first-child)) {
  margin-block-start: 1.5em;
}

.conversation-list :deep(.dou-markdown :where(h3):not(:last-child)) {
  margin-block-end: 0.35em;
}

.conversation-list :deep(.dou-markdown :where(h4, h5, h6):not(:first-child)) {
  margin-block-start: 1.35em;
}

.conversation-list :deep(.dou-markdown :where(h4, h5, h6):not(:last-child)) {
  margin-block-end: 0.3em;
}

.conversation-list
  :deep(
    .dou-markdown
      :where(h1, h2, h3, h4, h5, h6)
      + :where(
        p,
        ul,
        ol,
        blockquote,
        .dou-markdown__table,
        .dou-markdown__code,
        .dou-markdown__custom,
        .dou-markdown__callout,
        .dou-markdown__container,
        div.dou-markdown__trailing
      )
  ) {
  margin-block-start: 0;
}

.conversation-list :deep(.dou-markdown :where(li + li)) {
  margin-block-start: 0.5em;
}

.conversation-list :deep(.dou-markdown :where(li > ul, li > ol)) {
  margin-block: 0.45em;
}

.conversation-list :deep(.dou-markdown :where(li > p)) {
  margin-block: 0.55em;
}

.conversation-list :deep(.dou-markdown :where(code:not(.dou-code-block__code))) {
  font-size: 0.875em;
  padding: 0.1em 0.36em;
}

.conversation-list :deep(.dou-markdown .dou-link--soft) {
  font-size: 0.93em;
  padding-block: 0.05em;
  padding-inline: 0.35em;
}

.reaction-picker {
  display: flex;
  gap: 2px;
  margin: -4px;
}

.conversation-empty {
  flex: 1;
  display: grid;
  place-items: center;
  padding-block-end: var(--conversation-footer-height, 0px);
}

.routine-prompt {
  display: block;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  user-select: text;
  -webkit-user-select: text;
}

.conversation-footer {
  position: absolute;
  inset-inline: 0;
  inset-block-end: 0;
  z-index: 3;
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 0 16px var(--conversation-footer-padding);
  pointer-events: none;
}

.conversation-input {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.conversation-alert,
.conversation-capsules :deep(button),
.conversation-capsules :deep(a),
.conversation-resolved,
.conversation-input :deep(.dou-chat-input) {
  pointer-events: auto;
}

.conversation-resolved {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-width: 0;
  min-height: 48px;
  padding: 9px 10px 9px 14px;
  border: 1px solid var(--dou-color-border);
  border-radius: 16px;
  background-color: var(--dou-color-bg-surface);
  color: var(--dou-color-text-secondary);
}

.conversation-resolved-status {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.conversation-resolved-icon {
  flex: none;
  width: 16px;
  height: 16px;
  font-size: 16px;
  color: var(--dou-color-success);
}

.conversation-resolved-icon.is-review {
  color: var(--dou-color-text-tertiary);
}

.conversation-resolved-text {
  min-width: 0;
  overflow-wrap: anywhere;
}

.conversation-resolved-action {
  flex: none;
}

.conversation-input :deep(.dou-chat-input:not(:focus-within)) {
  box-shadow: none;
}

.conversation-model {
  max-width: min(240px, 44vw);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.model-option-title {
  display: inline-flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
  min-width: 0;
}

.model-option-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.model-option-effort {
  flex: none;
  font-size: 11px;
  color: var(--dou-color-primary);
  font-weight: var(--dou-font-weight-medium);
}

/* A folded message from the coordinator the search led to, marked like a chat message. */
.relay.conversation-focused {
  outline: 1px solid var(--dou-color-primary);
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
</style>
