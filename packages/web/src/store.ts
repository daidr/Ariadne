import type {
  AnyStoredEvent,
  ArtifactKind,
  ArtifactSummary,
  BlockAnswer,
  ConversationView,
  LineComment,
  LiveCommand,
  LiveUpdate,
  MemoryView,
  MessageView,
  NoticeView,
  PlanOp,
  PlanView,
  ProjectView,
  TodoView,
  TurnFile,
} from '@ariadne/core'
import { reactive } from 'vue'
import {
  api,
  ApiError,
  DEFAULT_KEYS,
  followEvents,
  type InputSettings,
  type MessageExtras,
  type ModelSettings,
  type ProjectDetail,
  type SourceInput,
  type StreamStatus,
} from './api'
import { t, tm } from './i18n'

/** What the middle column shows. */
export type MainView =
  | { kind: 'conversation' }
  | { kind: 'plan' }
  | { kind: 'artifacts' }
  | { kind: 'artifact'; id: string }
  /**
   * What a thread's branch changed, with merging it; or with `turn`, what one turn changed
   * in its workspace. `path` opens that file first.
   */
  | { kind: 'changes'; threadId: string; turn?: TurnChangesRef; path?: string }
  /** What a conversation's agent did, turn by turn, for finding out what went wrong. */
  | { kind: 'trace'; conversationId: string }

/** A turn's changes, as its notice names them. */
export interface TurnChangesRef {
  turnId: string
  from: string
  to: string
  files: TurnFile[]
  total: { files: number; added: number; removed: number }
}

/** One conversation as shown on screen: the coordinator's, or the open thread's. */
export interface ConversationState {
  conversation: ConversationView | null
  messages: MessageView[]
  /** What happened in the conversation besides messages, shown in line with them. */
  notices: NoticeView[]
  todo: TodoView | null
  /** The model it uses now, with how much its context holds. */
  model: { id: string; contextTokens: number } | null
  /** Earlier messages exist than the first one loaded. */
  more: boolean
  /**
   * The first message loaded; refreshes fetch everything from it on, so pages loaded
   * earlier stay. `null` until the first page is in.
   */
  from: string | null
  /** An earlier page is on its way. */
  loadingEarlier: boolean
  /**
   * The newest event what is shown holds; later refreshes fetch only what changed after it.
   * `null` until the first page is in.
   */
  seq: number | null
}

const emptyConversation = (): ConversationState => ({
  conversation: null,
  messages: [],
  notices: [],
  todo: null,
  model: null,
  more: false,
  from: null,
  loadingEarlier: false,
  seq: null,
})

/** A message to bring into view and mark for a moment, e.g. one the search found. */
export interface MessageFocus {
  conversationId: string
  messageId: string
  /** Changes on every jump, so jumping to the same message again still scrolls. */
  nonce: number
}

/** A model the user set up, as the page names it. */
export interface ModelOption {
  id: string
  label: string
}

interface State {
  stream: StreamStatus
  projects: ProjectView[]
  currentId: string | null
  detail: ProjectDetail | null
  /** The coordinator's conversation, always on screen. */
  coordinator: ConversationState
  /** The thread open beside the coordinator; `null` shows the project panel there. */
  threadId: string | null
  thread: ConversationState
  plan: PlanView | null
  artifacts: ArtifactSummary[]
  /** The project's memory: standing preferences every conversation sees. */
  memories: MemoryView[]
  main: MainView
  /** Whether the coordinator has a model; `null` until known. */
  modelReady: boolean | null
  /** The models set up, to pick one for a thread. */
  models: ModelOption[]
  /** The model threads use unless given another. */
  threadDefault: string | null
  /** The new-project form is open. */
  creatingProject: boolean
  /** The project source popover in the header is open; the first-run steps open it too. */
  sourceOpen: boolean
  /** The message to scroll to; the conversation showing it clears it once there. */
  focus: MessageFocus | null
  /** The project search is open. */
  searchOpen: boolean
  /** The open project's settings dialog is open. */
  projectSettingsOpen: boolean
  /** Which key sends, and the snippets offered after `/`. */
  input: InputSettings
  /** Commands conversations are running now, by conversation; not part of the log. */
  live: Record<string, LiveCommand>
  /**
   * Line comments the user wrote on threads' changes and has not sent yet, by thread;
   * they go to the thread together.
   */
  lineComments: Record<string, LineComment[]>
  error: string | null
}

export const state = reactive<State>({
  stream: 'connecting',
  projects: [],
  currentId: null,
  detail: null,
  coordinator: emptyConversation(),
  threadId: null,
  thread: emptyConversation(),
  plan: null,
  artifacts: [],
  memories: [],
  main: { kind: 'conversation' },
  modelReady: null,
  models: [],
  threadDefault: null,
  creatingProject: false,
  sourceOpen: false,
  focus: null,
  searchOpen: false,
  projectSettingsOpen: false,
  input: { submitOn: 'enter', snippets: [], keys: { ...DEFAULT_KEYS } },
  live: {},
  lineComments: {},
  error: null,
})

export function report(error: unknown) {
  state.error = error instanceof ApiError ? tm(error.localized) : String(error)
}

async function refreshProjects() {
  const { projects, lastSeq } = await api.listProjects()
  state.projects = projects
  return lastSeq
}

/** The parts of the open project the page loads, each refreshed when an event touches it. */
type Part = 'detail' | 'conversations' | 'plan' | 'artifacts' | 'memories'
const ALL_PARTS: readonly Part[] = ['detail', 'conversations', 'plan', 'artifacts', 'memories']

/**
 * Load the open project's `parts`. Conversations already shown fetch only what changed
 * since they were last loaded. Refreshes run one after another, so a slow answer never
 * lands on top of a newer one.
 */
let refreshing: Promise<void> = Promise.resolve()
function refreshCurrent(parts: Iterable<Part> = ALL_PARTS): Promise<void> {
  const wanted = new Set(parts)
  const run = refreshing.then(() => loadParts(wanted))
  refreshing = run.catch(() => {})
  return run
}

async function loadParts(parts: ReadonlySet<Part>) {
  const id = state.currentId
  const threadId = state.threadId
  if (!id) return
  const detail = parts.has('detail') || !state.detail ? await api.getProject(id) : state.detail
  const conversations = parts.has('conversations')
  const [coordinator, thread, plan, artifacts, memory] = await Promise.all([
    conversations ? fetchConversation(detail.project.coordinatorId, state.coordinator) : null,
    conversations && threadId ? fetchConversation(threadId, state.thread) : null,
    parts.has('plan') ? api.getPlan(id) : null,
    parts.has('artifacts') ? api.listArtifacts(id) : null,
    parts.has('memories') ? api.listMemories(id) : null,
  ])
  // The user may have opened another project while this was loading: a late answer is
  // stale. Another thread only makes the thread's own part stale; opening it loads that.
  if (state.currentId !== id) return
  state.detail = detail
  if (coordinator) state.coordinator = coordinator(state.coordinator)
  if (conversations && state.threadId === threadId) {
    state.thread = thread ? thread(state.thread) : emptyConversation()
  }
  if (plan) state.plan = plan.plan
  if (artifacts) state.artifacts = artifacts.artifacts
  if (memory) state.memories = memory.memories
}

/**
 * Fetch a conversation: whole from where it starts on screen the first time, then only
 * what changed. Gives what to make of the state on screen once the answer is in.
 */
async function fetchConversation(
  conversationId: string,
  current: ConversationState,
): Promise<(now: ConversationState) => ConversationState> {
  const { from, seq } = current
  if (seq === null || current.conversation?.id !== conversationId) {
    const page = await api.getConversation(conversationId, cursorFrom(from))
    return (now) => take(now, page, from)
  }
  const page = await api.getConversation(conversationId, { since: seq, ...cursorFrom(from) })
  return (now) => merge(now, page)
}

type ConversationPage = Awaited<ReturnType<typeof api.getConversation>>

const cursorFrom = (from: string | null) => (from ? { from } : {})

/**
 * The conversation as just fetched from `from`. If an earlier page or a jump moved the
 * start meanwhile, what is on screen before `from` stays and the fetched rest follows it.
 */
function take(
  current: ConversationState,
  page: ConversationPage,
  from: string | null,
): ConversationState {
  const { conversation, todo, model } = page
  if (current.from !== from) {
    if (!from) return { ...current, conversation, todo, model }
    const fetched = new Set(page.notices.map((notice) => notice.id))
    const startAt = page.messages[0]?.createdAt ?? Infinity
    return {
      ...current,
      conversation,
      todo,
      model,
      messages: [...current.messages.filter((message) => message.id < from), ...page.messages],
      notices: [
        ...current.notices.filter(
          (notice) => !fetched.has(notice.id) && notice.createdAt < startAt,
        ),
        ...page.notices,
      ],
    }
  }
  return {
    conversation,
    todo,
    model,
    messages: page.messages,
    notices: page.notices,
    more: page.more,
    // Hold on to where it starts, so the next refresh brings the same messages and more.
    from: page.messages[0]?.id ?? from,
    loadingEarlier: current.loadingEarlier,
    seq: page.lastSeq,
  }
}

/** The conversation on screen with what changed in it: new messages added, changed ones replaced. */
export function merge(current: ConversationState, page: ConversationPage): ConversationState {
  const messages = [...current.messages]
  for (const message of page.messages) {
    const at = messages.findIndex((shown) => shown.id === message.id)
    if (at !== -1) messages[at] = message
    else if (!current.from || message.id >= current.from) messages.push(message)
  }
  messages.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
  // A notice before the first message loaded comes with the earlier page instead.
  const firstAt = current.more ? (messages[0]?.createdAt ?? Infinity) : -Infinity
  const notices = [...current.notices]
  for (const notice of page.notices) {
    const at = notices.findIndex((shown) => shown.id === notice.id)
    if (at !== -1) notices[at] = notice
    else if (notice.createdAt >= firstAt) notices.push(notice)
  }
  notices.sort((a, b) => a.createdAt - b.createdAt || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
  return {
    ...current,
    conversation: page.conversation,
    todo: page.todo,
    model: page.model,
    messages,
    notices,
    from: current.from ?? messages[0]?.id ?? null,
    seq: Math.max(current.seq ?? 0, page.lastSeq),
  }
}

const slotOf = (conversationId: string): 'coordinator' | 'thread' | null =>
  state.detail?.project.coordinatorId === conversationId
    ? 'coordinator'
    : state.threadId === conversationId
      ? 'thread'
      : null

/** Load the page of messages before the first one shown; called when scrolled to the top. */
export async function loadEarlier(conversationId: string) {
  const slot = slotOf(conversationId)
  if (!slot) return
  const current = state[slot]
  const first = current.messages[0]
  if (!current.more || current.loadingEarlier || !first) return
  current.loadingEarlier = true
  try {
    const page = await api.getConversation(conversationId, { before: first.id })
    // Still the same conversation, starting where it did when this was asked for.
    if (slotOf(conversationId) !== slot || state[slot].messages[0]?.id !== first.id) return
    state[slot] = {
      ...state[slot],
      messages: [...page.messages, ...state[slot].messages],
      notices: [...page.notices, ...state[slot].notices],
      more: page.more,
      from: page.messages[0]?.id ?? first.id,
    }
  } catch (error) {
    report(error)
  } finally {
    if (slotOf(conversationId) === slot) state[slot].loadingEarlier = false
  }
}

/**
 * Show a message wherever it was said: the coordinator's conversation, or its thread
 * opened beside it, loaded back far enough to hold it, then scrolled to.
 */
export async function openMessage(conversationId: string, messageId: string) {
  const project = state.detail?.project
  if (!project) return
  if (conversationId === project.coordinatorId) {
    if (state.main.kind !== 'conversation') state.main = { kind: 'conversation' }
  } else {
    await openThread(conversationId)
  }
  const slot = slotOf(conversationId)
  if (!slot) return
  if (!state[slot].messages.some((message) => message.id === messageId)) {
    try {
      const page = await api.getConversation(conversationId, { from: messageId })
      if (slotOf(conversationId) !== slot) return
      state[slot] = {
        ...state[slot],
        conversation: page.conversation,
        todo: page.todo,
        model: page.model,
        messages: page.messages,
        notices: page.notices,
        more: page.more,
        from: page.messages[0]?.id ?? null,
        seq: page.lastSeq,
      }
    } catch (error) {
      report(error)
      return
    }
  }
  state.focus = { conversationId, messageId, nonce: (state.focus?.nonce ?? 0) + 1 }
}

export async function openProject(id: string) {
  state.currentId = id
  state.threadId = null
  state.detail = null
  state.plan = null
  state.artifacts = []
  state.memories = []
  state.main = { kind: 'conversation' }
  state.coordinator = emptyConversation()
  state.thread = emptyConversation()
  await refreshCurrent().catch(report)
}

/**
 * Open a thread's conversation beside the coordinator's, or close it with `null`. The
 * coordinator stays where it is.
 */
export async function openThread(threadId: string | null) {
  if (state.threadId === threadId) return
  state.threadId = threadId
  state.thread = emptyConversation()
  await refreshCurrent(['conversations']).catch(report)
}

export async function createProject(name: string) {
  try {
    const { project } = await api.createProject(name)
    await refreshProjects()
    await openProject(project.id)
  } catch (error) {
    report(error)
  }
}

/** Delete a project; the dialog asking first stays open on a failure, which is reported. */
export async function deleteProject(id: string) {
  try {
    await api.deleteProject(id)
  } catch (error) {
    report(error)
    throw error
  }
  forgetProject(id)
}

/** Let go of a project that is gone; if it was open, the next one opens. */
function forgetProject(id: string) {
  state.projects = state.projects.filter((project) => project.id !== id)
  if (state.currentId !== id) return
  const next = state.projects[0]?.id
  if (next) {
    void openProject(next)
    return
  }
  state.currentId = null
  state.threadId = null
  state.detail = null
  state.plan = null
  state.artifacts = []
  state.memories = []
  state.main = { kind: 'conversation' }
  state.coordinator = emptyConversation()
  state.thread = emptyConversation()
}

/** Whether it was sent; a failure is reported, and the draft is kept for another try. */
export async function sendMessage(
  conversationId: string,
  content: string,
  extras: MessageExtras = {},
): Promise<boolean> {
  const project = state.detail?.project
  if (!project) return false
  try {
    await api.postMessage(project.id, conversationId, content, extras)
    return true
  } catch (error) {
    report(error)
    return false
  }
}

export async function stopTurn(conversationId: string) {
  await api.stopTurn(conversationId).catch(report)
}

export async function retryTurn(conversationId: string) {
  await api.retryTurn(conversationId).catch(report)
}

/** Show the plan graph large, or an artifact, in the middle column. */
export function openMain(view: MainView) {
  state.main = view
}

/** Start an empty artifact of the user's own and open it. */
export async function createArtifact(kind: ArtifactKind) {
  const id = state.currentId
  if (!id) return
  try {
    const { artifact } = await api.createArtifact(id, {
      kind,
      title: t(`artifact.untitled.${kind}`),
    })
    await refreshCurrent()
    openMain({ kind: 'artifact', id: artifact.id })
  } catch (error) {
    report(error)
  }
}

/** Save the open project's settings; returns why they were refused, or `null`. */
export async function saveProjectSettings(
  input: Parameters<typeof api.saveProjectSettings>[1],
  slots: number | null,
  mcp: string[],
): Promise<string | null> {
  const project = state.detail?.project
  if (!project) return null
  try {
    await api.saveProjectSettings(project.id, input)
    if (slots !== project.slots) await api.setProjectSlots(project.id, slots)
    if (JSON.stringify(mcp) !== JSON.stringify(project.mcp)) {
      await api.setProjectMcp(project.id, mcp)
    }
    await refreshCurrent(['detail'])
    return null
  } catch (error) {
    if (error instanceof ApiError) return tm(error.localized)
    report(error)
    return null
  }
}

/** Download everything the project's log holds as a file. */
export async function exportProject(id: string) {
  try {
    const { blob, name } = await api.exportProject(id)
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = name
    link.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  } catch (error) {
    report(error)
  }
}

/** Save the project's working directory; returns why it was refused, or `null`. */
export async function setSource(source: SourceInput | null): Promise<string | null> {
  const project = state.detail?.project
  if (!project) return null
  try {
    await api.setSource(project.id, source)
    await refreshCurrent()
    return null
  } catch (error) {
    if (error instanceof ApiError) return tm(error.localized)
    report(error)
    return null
  }
}

export async function dismissProposal(proposalId: string) {
  const project = state.detail?.project
  if (!project) return
  try {
    state.plan = (await api.dismissProposal(project.id, proposalId)).plan
  } catch (error) {
    report(error)
  }
}

/** Ask the repository for the thread's pull request now; the event stream refreshes the view. */
export async function refreshPullRequest(threadId: string) {
  await api.refreshPullRequest(threadId).catch(report)
}

/** Apply the user's own edit of the plan; returns whether it took effect. */
/**
 * Change the plan as the user. `conflict` means someone changed a field the edit expected
 * unchanged; the caller shows that next to the edit instead of as an error.
 */
export async function editPlan(
  reason: string,
  ops: PlanOp[],
): Promise<'ok' | 'conflict' | 'failed'> {
  const project = state.detail?.project
  if (!project || !ops.length) return 'failed'
  try {
    const { plan } = await api.revisePlan(project.id, { reason, ops })
    // The user may have opened another project while this was on its way.
    if (state.detail?.project.id === plan.projectId) state.plan = plan
    return 'ok'
  } catch (error) {
    if (error instanceof ApiError && error.localized.key === 'error.planConflict') {
      // The plan here may lag behind (the event stream dropped for a moment): fetch it, so
      // the edit shows what changed.
      try {
        const { plan } = await api.getPlan(project.id)
        if (state.detail?.project.id === plan.projectId) state.plan = plan
      } catch {
        // The next event brings it.
      }
      return 'conflict'
    }
    report(error)
    return 'failed'
  }
}

/** Link an issue to a plan node, or unlink it; returns why it was refused, or `null`. */
export async function setNodeIssue(
  nodeId: string,
  number: number,
  linked: boolean,
): Promise<string | null> {
  const project = state.detail?.project
  if (!project) return null
  try {
    const { plan } = linked
      ? await api.linkIssue(project.id, nodeId, number)
      : await api.unlinkIssue(project.id, nodeId, number)
    if (state.detail?.project.id === plan.projectId) state.plan = plan
    return null
  } catch (error) {
    if (error instanceof ApiError) return tm(error.localized)
    report(error)
    return null
  }
}

export async function revertPlan(revisionId: string, reason: string) {
  const project = state.detail?.project
  if (!project) return
  try {
    const { plan } = await api.revertPlan(project.id, revisionId, reason)
    if (state.detail?.project.id === plan.projectId) state.plan = plan
  } catch (error) {
    report(error)
  }
}

/** Whether the answer was taken; a refusal is reported to the user. */
export async function answerAttention(id: string, answer: BlockAnswer): Promise<boolean> {
  return api.answerAttention(id, answer).then(
    () => true,
    (error: unknown) => {
      report(error)
      return false
    },
  )
}

// Coalesce bursts of events into one refresh per frame, remembering every project touched
// and which parts of the open one.
let pending = false
const touched = new Set<string>()
const parts = new Set<Part>()
const listeners = new Set<(event: AnyStoredEvent) => void>()

/** Hear every event as it arrives, e.g. to notify the user. */
export function onLiveEvent(listener: (event: AnyStoredEvent) => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

/**
 * What an event changes of the open project's parts, by the group its type starts with.
 * A group not listed refreshes every part: a new event is never missed, only fetched more
 * than it needs.
 */
export function partsOf(type: AnyStoredEvent['type']): readonly Part[] {
  switch (type.split('.')[0]) {
    case 'prompt':
      return []
    case 'memory':
      return ['memories', 'conversations']
    case 'artifact':
      return ['artifacts', 'conversations']
    // What a turn does while it runs; its threads' stages on the plan stay as they are.
    case 'tool':
    case 'research':
    case 'todo':
    case 'conversation':
    case 'notice':
    case 'project':
      return ['detail', 'conversations']
    case 'turn':
      return type === 'turn.step'
        ? ['detail', 'conversations']
        : ['detail', 'conversations', 'plan']
    // Anything that may move a thread's stage, which the plan's nodes show.
    case 'plan':
    case 'thread':
    case 'message':
    case 'attention':
    case 'workspace':
    case 'branch':
    case 'pull_request':
    case 'checks':
    case 'issue':
    case 'mcp':
      return ['detail', 'conversations', 'plan']
    default:
      return ALL_PARTS
  }
}

function onEvent(event: AnyStoredEvent) {
  for (const listener of listeners) listener(event)
  if (event.type === 'project.deleted') {
    touched.delete(event.projectId)
    forgetProject(event.projectId)
    return
  }
  // A piece of model input stored once changes nothing on screen.
  if (event.type === 'prompt.recorded') return
  touched.add(event.projectId)
  if (event.projectId === state.currentId) for (const part of partsOf(event.type)) parts.add(part)
  if (pending) return
  pending = true
  requestAnimationFrame(() => {
    pending = false
    const current = state.currentId !== null && touched.has(state.currentId)
    const changed = [...parts]
    touched.clear()
    parts.clear()
    void refreshProjects().catch(report)
    if (current && changed.length) void refreshCurrent(changed).catch(report)
  })
}

/** Take in the model settings as saved: whether the coordinator has one, and the choices. */
export function applyModelSettings(settings: ModelSettings) {
  state.modelReady = !!settings.roles.thread.model
  state.threadDefault = settings.roles.thread.model
  state.models = settings.models.map((model) => ({
    id: model.id,
    label: `${settings.providers.find((p) => p.id === model.provider)?.name ?? model.provider} / ${model.model}`,
  }))
}

/** Give a thread another model, or back the default with `null`. */
export async function setThreadModel(threadId: string, model: string | null) {
  try {
    await api.setThreadModel(threadId, model)
    await refreshCurrent()
  } catch (error) {
    report(error)
  }
}

async function load(): Promise<number | null> {
  api.getModelSettings().then(applyModelSettings, () => {})
  api.getInputSettings().then(
    (input) => (state.input = input),
    () => {},
  )
  try {
    const lastSeq = await refreshProjects()
    const target = state.currentId ?? state.projects[0]?.id
    if (target) await openProject(target)
    return lastSeq
  } catch (error) {
    report(error)
    return null
  }
}

export async function start() {
  const lastSeq = await load()
  // Follow from the state just loaded. If loading failed (say the page opened before the
  // server), follow from the server's newest event and load once the stream is up.
  let loaded = lastSeq !== null
  followEvents(
    lastSeq,
    onEvent,
    (status) => (state.stream = status),
    () => {
      // The server tells again what is running now.
      state.live = {}
      if (loaded) return
      loaded = true
      void load()
    },
    onLive,
  )
}

function onLive(update: LiveUpdate) {
  if (update.command) state.live[update.conversationId] = update.command
  else delete state.live[update.conversationId]
}

/**
 * Send a thread the line comments written on its changes, all in one message; the drafts
 * go once it is sent.
 */
export async function sendLineComments(
  threadId: string,
  comments: LineComment[],
  ids: readonly string[],
): Promise<boolean> {
  if (!comments.length) return false
  const sent = await sendMessage(threadId, '', { comments })
  if (sent) {
    // Only those sent: one written while they went stays a draft.
    const left = (state.lineComments[threadId] ?? []).filter(
      (draft) => !ids.includes((draft as { id?: string }).id ?? ''),
    )
    if (left.length) state.lineComments[threadId] = left
    else delete state.lineComments[threadId]
  }
  return sent
}

/** Add a memory entry, change one (`id`), or remove it (`text` null); `null` when it worked. */
export async function saveMemory(id: string | null, text: string | null): Promise<string | null> {
  const projectId = state.currentId
  if (!projectId) return null
  try {
    if (text === null) {
      if (id) await api.removeMemory(projectId, id)
    } else if (id) await api.saveMemory(projectId, id, text)
    else await api.addMemory(projectId, text)
    state.memories = (await api.listMemories(projectId)).memories
    return null
  } catch (error) {
    return error instanceof ApiError ? tm(error.localized) : String(error)
  }
}

/** Take back an agent's change to the project memory. */
export async function undoNotice(noticeId: string) {
  const projectId = state.currentId
  if (!projectId) return
  await api.undoNotice(projectId, noticeId).catch(report)
}

/** Let a thread the coordinator stopped go on. */
export async function stopThread(threadId: string, reason: string) {
  await api.stopThread(threadId, reason).catch(report)
}

export async function resumeThread(threadId: string) {
  await api.resumeThread(threadId).catch(report)
}

/** The user is through with what the thread put up for review. */
export async function reviewThread(threadId: string) {
  await api.reviewThread(threadId).catch(report)
}
