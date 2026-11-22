import type {
  AnyStoredEvent,
  ArtifactKind,
  ArtifactSummary,
  BlockAnswer,
  ConversationView,
  LineComment,
  LiveCommand,
  LiveUpdate,
  DraftUpdate,
  ReplyDraft,
  BackgroundUpdate,
  BackgroundView,
  MemoryView,
  MessageView,
  NoticeView,
  PlanView,
  ProjectView,
  TodoView,
  ThreadView,
  TurnFile,
} from '@ariadne/core'
import { toast } from 'dou-ui'
import { computed, reactive, ref, toRaw, type Ref } from 'vue'
import type { AskDraft } from './blocks/ask-drafts'
import type { Draft } from './components/attachments'
import { reuse } from './reuse'
import { coalescedRefresh } from './coalesced-refresh'
import * as side from './side/layout'
import type { OpenOptions, PaneKey, SideLayout, SplitDirection } from './side/layout'
import {
  api,
  ApiError,
  DEFAULT_KEYS,
  followEvents,
  type Effort,
  type InputSettings,
  type MentionInput,
  type MessageExtras,
  type ModelSettings,
  type ProjectDetail,
  type StreamStatus,
  type UpdateStatus,
} from './api'
import { t, tm } from './i18n'
import { modelLabels } from './model-label'
import { clearBoardThumbnails } from './artifacts/board-thumbnail'
import { clearLinkCaches } from './markdown/link-summaries'
import type { WorkspaceLocation, WorkspaceSnapshot } from './workspace-route'

/** What the middle column shows. */
export type MainView =
  | { kind: 'conversation' }
  | { kind: 'plan' }
  | { kind: 'artifacts' }
  /** What a conversation's agent did, turn by turn, for finding out what went wrong. */
  | { kind: 'trace'; conversationId: string }

export type WorkspaceLevel = 'projects' | 'coordinator' | 'side'

/**
 * Something read beside the conversations, in a tab on the right side or full screen: an
 * artifact; or what a thread's branch changed, with merging it, or with `turn`, what one
 * turn changed in its workspace. `path` opens that file first.
 */
export type OpenedView =
  | { kind: 'artifact'; id: string }
  | { kind: 'changes'; threadId: string; turn?: TurnChangesRef; path?: string }

/** A view shown full screen, with the thread it was opened from beside it. */
export interface FullView {
  view: OpenedView
  threadId: string | null
  /** It was a tab on the right side, and goes back there when closed. */
  fromSide: boolean
}

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
  readThrough?: string
  /** What happened in the conversation besides messages, shown in line with them. */
  notices: NoticeView[]
  todo: TodoView | null
  /** The model it uses now, with how much its context holds. */
  model: { id: string; contextTokens: number; compactAt: number; effort?: Effort | null } | null
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

export const emptyConversation = (): ConversationState => ({
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

export type ProjectSettingsTab = 'source' | 'instructions' | 'workspace' | 'autopilot' | 'usage'

/** Open the open project's settings, on `tab`; the first-run steps open them on the source. */
export function openProjectSettings(tab: ProjectSettingsTab = 'source') {
  state.projectSettingsTab = tab
  state.projectSettingsOpen = true
}

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
  /** The efforts it accepts, least first; empty when none, omitted when unknown. */
  efforts?: Effort[]
}

interface State {
  stream: StreamStatus
  projects: ProjectView[]
  currentId: string | null
  detail: ProjectDetail | null
  /** The coordinator's conversation, always on screen. */
  coordinator: ConversationState
  /** What the right side shows, beside the coordinator: groups of tabs, split as the user likes. */
  side: SideLayout
  /** The conversations of the threads open on the right side, by thread. */
  threads: Record<string, ConversationState>
  plan: PlanView | null
  artifacts: ArtifactSummary[]
  /** The project's memory: standing preferences every conversation sees. */
  memories: MemoryView[]
  main: MainView
  /** The part of the workspace in front on narrow screens. */
  workspaceLevel: WorkspaceLevel
  /** What a changes tab compares and opens first; not remembered with the layout. */
  paneParams: Record<string, { turn?: TurnChangesRef; path?: string }>
  /** The view shown full screen, if one is. */
  full: FullView | null
  /** The plan is shown large across the side too, until something opens there. */
  planWide: boolean
  /** Whether the coordinator has a model; `null` until known. */
  modelReady: boolean | null
  /** The models set up, to pick one for a thread. */
  models: ModelOption[]
  /** The model threads use unless given another. */
  threadDefault: string | null
  /** The default thinking effort threads use unless overridden. */
  threadDefaultEffort: Effort | null
  /** Usage shows the estimated cost too. */
  showCost: boolean
  /** The user is making a new project: its form shows in place of the open one. */
  starting: boolean
  /** The message to scroll to; the conversation showing it clears it once there. */
  focus: MessageFocus | null
  /** The project search is open. */
  searchOpen: boolean
  /** The open project's settings dialog is open, and the tab it opens on. */
  projectSettingsOpen: boolean
  projectSettingsTab: ProjectSettingsTab
  /** Which key sends, and the snippets offered after `/`. */
  input: InputSettings
  /** Commands conversations are running now, by conversation; not part of the log. */
  live: Record<string, LiveCommand>
  /**
   * Replies being written now, by conversation; not part of the log. A posted one stays
   * until its message is loaded, so it goes on showing in the same place.
   */
  writing: Record<string, ReplyDraft>
  /** Threads' background commands by conversation, as the server last told them. */
  background: Record<string, BackgroundView[]>
  /**
   * Line comments the user wrote on threads' changes and has not sent yet, by thread;
   * they go to the thread together.
   */
  lineComments: Record<string, LineComment[]>
}

export const state = reactive<State>({
  stream: 'connecting',
  projects: [],
  currentId: null,
  detail: null,
  coordinator: emptyConversation(),
  side: side.defaultLayout(),
  threads: {},
  plan: null,
  artifacts: [],
  memories: [],
  main: { kind: 'conversation' },
  workspaceLevel: 'projects',
  paneParams: {},
  full: null,
  planWide: false,
  modelReady: null,
  models: [],
  threadDefault: null,
  threadDefaultEffort: null,
  showCost: false,
  starting: false,
  focus: null,
  searchOpen: false,
  projectSettingsOpen: false,
  projectSettingsTab: 'source',
  input: { submitOn: 'enter', snippets: [], keys: { ...DEFAULT_KEYS } },
  live: {},
  writing: {},
  background: {},
  lineComments: {},
})

let stopStream: (() => void) | null = null
let streamSession: symbol | null = null

export function isUnauthorizedError(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401
}

function markUnauthorized() {
  streamSession = null
  stopStream?.()
  stopStream = null
  state.stream = 'unauthorized'
}

export function report(error: unknown) {
  if (isUnauthorizedError(error)) {
    markUnauthorized()
    return
  }
  const message = error instanceof ApiError ? tm(error.localized) : String(error)
  toast.danger(message, { duration: 7000, closable: true })
}

const PROJECT_ORDER_KEY = 'ariadne.projectOrder'
let projectOrder: string[] = []
try {
  const saved: unknown = JSON.parse(localStorage.getItem(PROJECT_ORDER_KEY) ?? 'null')
  if (Array.isArray(saved)) projectOrder = saved.filter((id) => typeof id === 'string')
} catch {
  // The default creation order still works when storage is unavailable.
}

function orderProjects(projects: readonly ProjectView[]): ProjectView[] {
  const remaining = new Map(projects.map((project) => [project.id, project]))
  const ordered: ProjectView[] = []
  for (const id of projectOrder) {
    const project = remaining.get(id)
    if (!project) continue
    ordered.push(project)
    remaining.delete(id)
  }
  const added = [...remaining.values()].toSorted(
    (a, b) => a.createdAt - b.createdAt || a.id.localeCompare(b.id),
  )
  return [...ordered, ...added]
}

export function reorderProjects(ids: string[]) {
  projectOrder = ids
  state.projects = orderProjects(state.projects)
  try {
    localStorage.setItem(PROJECT_ORDER_KEY, JSON.stringify(projectOrder))
  } catch {
    // The order still applies while the page is open.
  }
}

let deletedDuringRefresh: Set<string> | undefined
const refreshProjectList = coalescedRefresh<string | null, number>(async (keys) => {
  const ids = keys.has(null) || keys.size > 128 ? [] : [...keys].filter((id) => id !== null)
  const deleted = new Set<string>()
  deletedDuringRefresh = deleted
  try {
    const { projects, lastSeq } = await api.listProjects(ids)
    const current = toRaw(state.projects)
    const updated = projects.filter((project) => !deleted.has(project.id))
    if (ids.length) {
      const requested = new Set(ids)
      updated.push(...current.filter((project) => !requested.has(project.id)))
    }
    state.projects = reuse(current, orderProjects(updated))
    return lastSeq
  } finally {
    deletedDuringRefresh = undefined
  }
})

function refreshProjects(ids: Iterable<string | null> = [null]) {
  return refreshProjectList(ids)
}

/** The parts of the open project the page loads, each refreshed when an event touches it. */
type Part = 'detail' | 'conversations' | 'plan' | 'artifacts' | 'memories'
type RefreshKey = Part | `conversation:${string}`
const ALL_PARTS: readonly Part[] = ['detail', 'conversations', 'plan', 'artifacts', 'memories']

/**
 * Load the open project's `parts`. Conversations already shown fetch only what changed
 * since they were last loaded. Refreshes run one after another, so a slow answer never
 * lands on top of a newer one.
 */
const refreshParts = coalescedRefresh(loadParts)
function refreshCurrent(parts: Iterable<RefreshKey> = ALL_PARTS): Promise<void> {
  return refreshParts(parts)
}

/** Where the open project's threads were last synced to, to ask only for what changed. */
let synced: { projectId: string; cursor: string } | null = null

/**
 * `current` brought up to date by a partial answer: the threads it brings take the place
 * of the ones shown, and `ids`, when the set of threads changed, says which stay and in
 * what order. `null` when a thread it names is in neither, so everything must be asked for.
 */
export function patchThreads(
  current: readonly ThreadView[],
  changed: readonly ThreadView[],
  ids: readonly string[] | undefined,
): ThreadView[] | null {
  const byId = new Map(current.map((thread) => [thread.id, thread]))
  for (const thread of changed) {
    if (!ids && !byId.has(thread.id)) return null
    byId.set(thread.id, thread)
  }
  const merged: ThreadView[] = []
  for (const id of ids ?? current.map((thread) => thread.id)) {
    const thread = byId.get(id)
    if (!thread) return null
    merged.push(thread)
  }
  return merged
}

/** The project's detail, and the cursor to keep once it is the one shown. */
async function fetchDetail(id: string): Promise<{ detail: ProjectDetail; cursor: string }> {
  const shown = state.detail?.project.id === id ? toRaw(state.detail) : null
  const since = shown && synced?.projectId === id ? synced.cursor : undefined
  let { sync, ...detail } = await api.getProject(id, since)
  if (sync.partial) {
    const threads = shown && patchThreads(shown.threads, detail.threads, sync.ids)
    if (threads) detail.threads = threads
    // What was shown is gone or does not fit: start over from everything.
    else ({ sync, ...detail } = await api.getProject(id))
  }
  return { detail, cursor: sync.cursor }
}

function syncProjectRow(project: ProjectView) {
  const currentProjects = toRaw(state.projects)
  const index = currentProjects.findIndex((item) => item.id === project.id)
  if (index < 0) return
  const next = [...currentProjects]
  next[index] = project
  state.projects = reuse(currentProjects, orderProjects(next))
}

async function loadParts(parts: ReadonlySet<RefreshKey>) {
  const id = state.currentId
  if (!id) return
  const fetched = parts.has('detail') || !state.detail ? await fetchDetail(id) : null
  // Keep the sidebar row up to date even if the user switched projects or a sibling fetch fails.
  if (fetched) syncProjectRow(fetched.detail.project)
  if (state.currentId !== id) return
  const detail = fetched?.detail ?? state.detail!
  const allConversations = parts.has('conversations')
  const targetConversations = new Set<string>()
  if (!allConversations) {
    for (const key of parts) {
      if (key.startsWith('conversation:'))
        targetConversations.add(key.slice('conversation:'.length))
    }
  }
  const needCoordinator = allConversations || targetConversations.has(detail.project.coordinatorId)
  // A thread that no longer exists closes; its tab would show nothing.
  const known = new Set(detail.threads.map((thread) => thread.id))
  const threadIds = shownThreads().filter(
    (threadId) => known.has(threadId) && (allConversations || targetConversations.has(threadId)),
  )
  const [coordinator, threads, plan, artifacts, memory] = await Promise.all([
    needCoordinator ? fetchConversation(detail.project.coordinatorId, state.coordinator) : null,
    threadIds.length
      ? Promise.all(
          threadIds.map(async (threadId) => {
            const current = state.threads[threadId] ?? emptyConversation()
            return [threadId, await fetchConversation(threadId, current)] as const
          }),
        )
      : null,
    parts.has('plan') ? api.getPlan(id) : null,
    parts.has('artifacts') ? api.listArtifacts(id) : null,
    parts.has('memories') ? api.listMemories(id) : null,
  ])
  // The user may have opened another project while this was loading: a late answer is
  // stale. Another thread only makes the thread's own part stale; opening it loads that.
  if (state.currentId !== id) return
  // What did not change stays the object it was, so nothing showing it draws again.
  state.detail = reuse(toRaw(state.detail), detail)
  // Only now: an answer dropped above must be asked for again from where this one began.
  if (fetched) synced = { projectId: id, cursor: fetched.cursor }
  const artifactIds = artifacts ? new Set(artifacts.artifacts.map((a) => a.id)) : null
  validateWorkspace(detail, known, artifactIds)
  if (coordinator) state.coordinator = coordinator(state.coordinator)
  // A thread closed meanwhile stays closed.
  for (const [threadId, apply] of threads ?? []) {
    if (!shownThreads().includes(threadId)) continue
    state.threads[threadId] = apply(state.threads[threadId] ?? emptyConversation())
  }
  if (plan) state.plan = reuse(toRaw(state.plan), plan.plan)
  settleSide()
  if (artifacts) state.artifacts = reuse(toRaw(state.artifacts), artifacts.artifacts)
  if (memory) state.memories = reuse(toRaw(state.memories), memory.memories)
}

/**
 * Whether a tab still has something to show: its thread, or its artifact. Artifacts are
 * only checked when they were loaded; `artifactIds` is `null` otherwise.
 */
function stillThere(key: PaneKey, threads: Set<string>, artifactIds: Set<string> | null) {
  const threadId = side.threadOf(key)
  if (threadId) return threads.has(threadId)
  const view = side.viewOf(key)
  if (!view) return true
  if (view.kind === 'artifact') return !artifactIds || artifactIds.has(view.id)
  return threads.has(view.threadId) && (view.kind === 'changes' || !!state.paneParams[key]?.turn)
}

function fullStillThere(view: OpenedView, threads: Set<string>, artifactIds: Set<string> | null) {
  return view.kind === 'artifact'
    ? !artifactIds || artifactIds.has(view.id)
    : threads.has(view.threadId)
}

function validateWorkspace(
  detail: ProjectDetail,
  threads: Set<string>,
  artifacts: Set<string> | null,
) {
  setSide(side.prune(state.side, (key) => stillThere(key, threads, artifacts)))
  if (state.full && !fullStillThere(state.full.view, threads, artifacts)) setFull(null)
  if (
    state.main.kind === 'trace' &&
    state.main.conversationId !== detail.project.coordinatorId &&
    !threads.has(state.main.conversationId)
  )
    resetMain()
  pruneThreadComposers()
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
    readThrough: page.readThrough ?? current.readThrough,
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
  // Nothing new leaves the lists the ones they were: what shows them has nothing to redo.
  const messages = page.messages.length ? [...current.messages] : current.messages
  for (const message of page.messages) {
    const at = messages.findIndex((shown) => shown.id === message.id)
    if (at !== -1) messages[at] = message
    else if (!current.from || message.id >= current.from) messages.push(message)
  }
  if (page.messages.length) messages.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
  // A notice before the first message loaded comes with the earlier page instead.
  const firstAt = current.more ? (messages[0]?.createdAt ?? Infinity) : -Infinity
  const notices = page.notices.length ? [...current.notices] : current.notices
  for (const notice of page.notices) {
    const at = notices.findIndex((shown) => shown.id === notice.id)
    if (at !== -1) notices[at] = notice
    else if (notice.createdAt >= firstAt) notices.push(notice)
  }
  if (page.notices.length)
    notices.sort((a, b) => a.createdAt - b.createdAt || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
  return {
    ...current,
    conversation: reuse(toRaw(current.conversation), page.conversation),
    todo: reuse(toRaw(current.todo), page.todo),
    model: reuse(toRaw(current.model), page.model),
    messages,
    notices,
    from: current.from ?? messages[0]?.id ?? null,
    seq: Math.max(current.seq ?? 0, page.lastSeq),
  }
}

/** The conversation on screen with this id: the coordinator's or an open thread's. */
export function conversationState(conversationId: string): ConversationState | null {
  if (state.detail?.project.coordinatorId === conversationId) return state.coordinator
  return state.threads[conversationId] ?? null
}

function setConversation(conversationId: string, value: ConversationState) {
  if (state.detail?.project.coordinatorId === conversationId) state.coordinator = value
  else if (state.threads[conversationId]) state.threads[conversationId] = value
}

/** Load the page of messages before the first one shown; called when scrolled to the top. */
export async function loadEarlier(conversationId: string) {
  const current = conversationState(conversationId)
  const first = current?.messages[0]
  if (!current || !current.more || current.loadingEarlier || !first) return
  current.loadingEarlier = true
  try {
    const page = await api.getConversation(conversationId, { before: first.id })
    // Still on screen, starting where it did when this was asked for.
    const now = conversationState(conversationId)
    if (!now || now.messages[0]?.id !== first.id) return
    setConversation(conversationId, {
      ...now,
      messages: [...page.messages, ...now.messages],
      notices: [...page.notices, ...now.notices],
      more: page.more,
      from: page.messages[0]?.id ?? first.id,
    })
  } catch (error) {
    report(error)
  } finally {
    const now = conversationState(conversationId)
    if (now) now.loadingEarlier = false
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
    openMain({ kind: 'conversation' })
  } else {
    await openThread(conversationId)
  }
  const shown = conversationState(conversationId)
  if (!shown) return
  if (!shown.messages.some((message) => message.id === messageId)) {
    try {
      const page = await api.getConversation(conversationId, { from: messageId })
      const now = conversationState(conversationId)
      if (!now) return
      setConversation(conversationId, {
        ...now,
        conversation: page.conversation,
        todo: page.todo,
        model: page.model,
        messages: page.messages,
        notices: page.notices,
        more: page.more,
        from: page.messages[0]?.id ?? null,
        seq: page.lastSeq,
      })
    } catch (error) {
      report(error)
      return
    }
  }
  state.focus = { conversationId, messageId, nonce: (state.focus?.nonce ?? 0) + 1 }
}

function prepareProject(id: string | null) {
  parts.clear()
  clearBoardThumbnails()
  clearLinkCaches()
  state.starting = false
  state.currentId = id
  if (!id) sideUntouched = true
  showSide(id ? readSide(id) : side.defaultLayout())
  state.detail = null
  state.plan = null
  state.artifacts = []
  state.memories = []
  resetMain()
  state.focus = null
  state.coordinator = emptyConversation()
}

export async function openProject(id: string, { reveal = true } = {}) {
  prepareProject(id)
  if (reveal) showWorkspace('coordinator')
  await refreshCurrent().catch(report)
}

const SIDE_KEY = (projectId: string) => `ariadne.side.${projectId}`

/** How the user left the project's right side, in this browser. */
function readSide(projectId: string): SideLayout {
  let stored: SideLayout | null = null
  try {
    const raw = localStorage.getItem(SIDE_KEY(projectId))
    stored = (raw && side.parseLayout(JSON.parse(raw))) || null
  } catch {
    // Not readable: the project opens with its lists.
  }
  sideUntouched = !stored
  return stored ?? side.defaultLayout()
}

/** Whether the project's right side is still as it came, never arranged by the user. */
let sideUntouched = true

/**
 * Until the user arranges the right side, the tab in front is the one with something on
 * it: the plan, or the threads while there are some and no plan yet.
 */
function settleSide() {
  if (!sideUntouched || !state.plan || !state.detail) return
  const front = !state.plan.nodes.length && state.detail.threads.length ? 'threads' : 'plan'
  if (side.activeOf(side.focusedGroup(state.side)) !== front) state.side = side.defaultLayout(front)
}

/** Show another project's right side: its loaded thread conversations start fresh. */
function showSide(layout: SideLayout) {
  state.side = layout
  state.paneParams = {}
  state.full = null
  state.threads = {}
}

/** Show the right side as `next`, remembered for the project; closed threads let go. */
export function setSide(next: SideLayout) {
  if (next === state.side) return
  sideUntouched = false
  state.side = next
  syncThreads()
  const keys = new Set(side.groupsOf(next.root).flatMap(side.tabsOf) as string[])
  for (const key of Object.keys(state.paneParams)) if (!keys.has(key)) delete state.paneParams[key]
  const projectId = state.currentId
  if (!projectId) return
  try {
    localStorage.setItem(SIDE_KEY(projectId), JSON.stringify(next))
  } catch {
    // Not remembered; the project opens with its lists next time.
  }
}

/** The threads whose conversations are on screen: in tabs, or beside a full-screen view. */
function shownThreads(): string[] {
  const shown = side.openThreads(state.side)
  const beside = state.full?.threadId
  return beside && !shown.includes(beside) ? [...shown, beside] : shown
}

let openedThreads = Promise.resolve()
/** Load the conversations newly on screen, and let go of those gone from it. */
function syncThreads() {
  const open = new Set(shownThreads())
  for (const threadId of Object.keys(state.threads)) {
    if (!open.has(threadId)) delete state.threads[threadId]
  }
  pruneThreadComposers(open)
  // A thread dragged in from the thread list has nothing loaded yet.
  const opened = [...open].filter((threadId) => !state.threads[threadId])
  for (const threadId of opened) state.threads[threadId] = emptyConversation()
  if (opened.length) openedThreads = refreshCurrent(['conversations']).catch(report)
}

/** Show a view full screen, or none; the thread beside it loads. */
function setFull(full: FullView | null) {
  state.full = full
  syncThreads()
}

/**
 * What the user is writing to a conversation and has not sent: kept here rather than in
 * its panel, so opening trace/plan/artifacts or moving a thread's tab to another group,
 * which builds the panel anew, keeps it. Closing a thread tab or deleting a project lets it go.
 */
export interface Composer {
  draft: Ref<string>
  files: Ref<Draft[]>
  mentions: Map<string, MentionInput>
  skills: Set<string>
  askDrafts: Map<string, AskDraft>
}
const composers = new Map<string, { projectId: string | null; composer: Composer }>()
const coordinatorComposers = new Map<string, Composer>()

function makeComposer(): Composer {
  return {
    draft: ref(''),
    files: ref([]),
    mentions: new Map(),
    skills: new Set(),
    askDrafts: new Map(),
  }
}

function releaseComposerFiles(composer: Composer | undefined) {
  for (const file of composer?.files.value ?? []) {
    if (file.url?.startsWith('blob:')) URL.revokeObjectURL(file.url)
  }
}

export function composerFor(threadId: string): Composer {
  const projectId = state.currentId ?? state.detail?.project.id ?? null
  let entry = composers.get(threadId)
  if (!entry) {
    entry = { projectId, composer: makeComposer() }
    composers.set(threadId, entry)
  } else if (entry.projectId === null && projectId !== null) {
    entry.projectId = projectId
  }
  return entry.composer
}

export function coordinatorComposerFor(projectId: string): Composer {
  let composer = coordinatorComposers.get(projectId)
  if (!composer) {
    composer = makeComposer()
    coordinatorComposers.set(projectId, composer)
  }
  return composer
}

function dropComposer(threadId: string) {
  releaseComposerFiles(composers.get(threadId)?.composer)
  composers.delete(threadId)
}

function pruneThreadComposers(open = new Set(shownThreads())) {
  for (const [threadId, entry] of composers) {
    if (entry.projectId === state.currentId && !open.has(threadId)) dropComposer(threadId)
  }
}

function dropProjectComposers(projectId: string) {
  releaseComposerFiles(coordinatorComposers.get(projectId))
  coordinatorComposers.delete(projectId)
  for (const [threadId, entry] of composers) {
    if (entry.projectId === projectId) dropComposer(threadId)
  }
}

/** The thread in front of the group on the right the user last used. */
export const activeThreadId = computed(() => side.focusedThread(state.side))

/**
 * Open something on the right side, beside the coordinator, which stays where it is: where
 * it already is, else in the group last used, or in a new group beside it.
 */
export async function openPane(key: PaneKey, options: OpenOptions = {}) {
  showWorkspace('side')
  const threadId = side.threadOf(key)
  const loading = !!threadId && !state.threads[threadId]
  setSide(side.open(state.side, key, options))
  if (loading) await openedThreads
}

/**
 * Open a thread's conversation on the right side; see `openPane`. Over a full-screen view
 * it shows beside that view instead, where the user can see it, not behind it.
 */
export async function openThread(threadId: string, options: OpenOptions = {}) {
  const full = state.full
  if (!full) return openPane(side.threadKey(threadId), options)
  if (full.threadId === threadId) return
  const loading = !state.threads[threadId]
  setFull({ ...full, threadId })
  if (loading) await openedThreads
}

/**
 * Open a thread from a click or key press: with Alt held it goes into a group of its own
 * beside the one last used, the same from every place a thread opens.
 */
export function openThreadFrom(event: MouseEvent | KeyboardEvent | undefined, threadId: string) {
  return openThread(threadId, event?.altKey ? { beside: 'horizontal' } : {})
}

export function closePane(key: PaneKey, acrossGroups = false) {
  setSide(side.close(state.side, key, acrossGroups))
}

export function splitPane(key: PaneKey, direction: SplitDirection) {
  setSide(side.split(state.side, key, direction))
}

/**
 * Create a project and open it. The refusal's reason when it was refused (a source that
 * cannot be reached), to show beside what was typed; `null` once it is open.
 */
export async function startProject(
  input: Parameters<typeof api.createProject>[0],
): Promise<string | null> {
  try {
    const { project } = await api.createProject(input)
    await refreshProjects()
    await openProject(project.id)
    return null
  } catch (error) {
    if (error instanceof ApiError) return tm(error.localized)
    report(error)
    return String(error)
  }
}

export async function renameProject(id: string, name: string) {
  await api.renameProject(id, name)
  await refreshProjects()
  if (state.currentId === id) await refreshCurrent(['detail'])
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
  deletedDuringRefresh?.add(id)
  dropProjectComposers(id)
  clearBoardThumbnails()
  clearLinkCaches()
  state.projects = state.projects.filter((project) => project.id !== id)
  if (state.currentId !== id) return
  const next = state.projects[0]?.id
  if (next) {
    void openProject(next, { reveal: state.workspaceLevel !== 'projects' })
    return
  }
  prepareProject(null)
  showWorkspace('projects')
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

/** The user's emoji on a message, or theirs taken back. */
export async function react(messageId: string, emoji: string, remove: boolean) {
  await (remove ? api.removeReaction(messageId, emoji) : api.addReaction(messageId, emoji)).catch(
    report,
  )
}

export async function retryTurn(conversationId: string) {
  await api.retryTurn(conversationId).catch(report)
}

/**
 * Where "back" leads from the view in the middle column: the views opened on the way there,
 * so an artifact opened from a message goes back to the message, not to the artifact list.
 */
let mainTrail: { view: MainView; level: WorkspaceLevel }[] = []
const sameView = (a: MainView, b: MainView) => JSON.stringify(a) === JSON.stringify(b)

export function showWorkspace(level: WorkspaceLevel) {
  if (level !== 'coordinator') state.starting = false
  if (level === 'side') state.planWide = false
  state.workspaceLevel = level
}

export function startNewProject() {
  state.starting = true
  showWorkspace('coordinator')
  if (state.main.kind === 'trace') resetMain()
}

function resetMain() {
  mainTrail = []
  state.main = { kind: 'conversation' }
  state.planWide = false
}

/** Show the plan graph large, or an artifact, in the middle column. */
export function openMain(view: MainView) {
  if (view.kind === 'conversation') mainTrail = []
  else if (!sameView(view, state.main)) {
    mainTrail = mainTrail.filter((seen) => !sameView(seen.view, view))
    mainTrail.push({ view: state.main, level: state.workspaceLevel })
  }
  state.main = view
  state.planWide = view.kind === 'plan'
  state.starting = false
  showWorkspace('coordinator')
}

/** Go back to the view this one was opened from; the coordinator when there is none. */
export function backMain() {
  const { view, level } = mainTrail.pop() ?? {
    view: { kind: 'conversation' },
    level: 'coordinator',
  }
  state.main = view
  state.planWide = view.kind === 'plan'
  showWorkspace(level)
}

export function captureWorkspace(): { location: WorkspaceLocation; snapshot: WorkspaceSnapshot } {
  return {
    location: {
      projectId: state.currentId,
      level: state.workspaceLevel,
      main: state.main,
      starting: state.starting,
      tab: state.currentId ? side.activeOf(side.focusedGroup(state.side)) : null,
      modal: state.full ? paneKeyOf(state.full.view) : null,
      from: state.full?.threadId ?? null,
    },
    snapshot: {
      side: state.side,
      paneParams: state.paneParams,
      full: state.full,
      planWide: state.planWide,
      mainTrail,
    },
  }
}

/** Apply a URL before loading its project, so all its open conversations load together. */
export async function restoreWorkspace(location: WorkspaceLocation, saved?: WorkspaceSnapshot) {
  const exists = (id: string | null) => state.projects.some((project) => project.id === id)
  const projectId = exists(location.projectId)
    ? location.projectId
    : exists(state.currentId)
      ? state.currentId
      : (state.projects[0]?.id ?? null)
  const useProject = projectId === location.projectId
  const changed = projectId !== state.currentId || (!!projectId && !state.detail)
  if (changed) prepareProject(projectId)

  const stored = useProject && saved ? side.parseLayout(saved.side, true) : null
  if (stored && saved) {
    state.side = stored
    state.paneParams = saved.paneParams
    sideUntouched = false
  } else if (useProject && projectId && location.tab) {
    const tab = location.tab
    if (!tab.startsWith('turn:') || state.paneParams[tab]?.turn) {
      state.side = side.open(
        state.side,
        side.groupWith(state.side, tab) ? tab : (side.listOf(tab) ?? tab),
      )
      sideUntouched = false
    }
  }

  const modal = useProject && projectId && location.modal ? openedViewOf(location.modal) : null
  state.full =
    stored && saved
      ? saved.full
      : modal
        ? { view: modal, threadId: location.from, fromSide: false }
        : null
  state.starting = location.starting && (useProject || !location.projectId)
  state.workspaceLevel = state.starting ? 'coordinator' : useProject ? location.level : 'projects'
  state.main = useProject && projectId ? location.main : { kind: 'conversation' }
  state.planWide =
    stored && saved ? saved.planWide : state.main.kind === 'plan' && state.workspaceLevel !== 'side'
  mainTrail = stored && saved ? saved.mainTrail : []

  if (changed) {
    await refreshCurrent().catch(report)
  } else if (state.detail) {
    validateWorkspace(
      state.detail,
      new Set(state.detail.threads.map((thread) => thread.id)),
      new Set(state.artifacts.map((artifact) => artifact.id)),
    )
    syncThreads()
  }
}

/** The tab a view opens in on the right side. */
export function paneKeyOf(view: OpenedView): PaneKey {
  if (view.kind === 'artifact') return side.artifactKey(view.id)
  return view.turn ? side.turnKey(view.threadId, view.turn.turnId) : side.changesKey(view.threadId)
}

/** The view a tab on the right side shows, if it shows one. */
export function openedViewOf(key: PaneKey): OpenedView | null {
  const view = side.viewOf(key)
  if (!view) return null
  if (view.kind === 'artifact') return view
  const params = state.paneParams[key] ?? {}
  if (view.kind === 'changes') return { kind: 'changes', threadId: view.threadId, ...params }
  return params.turn ? { kind: 'changes', threadId: view.threadId, ...params } : null
}

export interface OpenViewOptions {
  /** The thread it was opened from: it shows full screen, with that thread beside it. */
  from?: string | null
  /** Show it full screen even when not opened from a thread. */
  full?: boolean
}

/**
 * Open an artifact or changes: in a tab on the right side, beside the coordinator; full
 * screen when opened from a thread, with the thread beside it so the user can read and
 * talk to it at once.
 */
export async function openView(view: OpenedView, options: OpenViewOptions = {}) {
  const from = options.from ?? null
  if (from || options.full) {
    // One taken from the right side goes back there rather than being lost.
    const before = state.full
    if (before?.fromSide && !sameOpened(before.view, view)) await openInPane(before.view)
    setFull({ view, threadId: from, fromSide: false })
    return
  }
  await openInPane(view)
}

function openInPane(view: OpenedView) {
  const key = paneKeyOf(view)
  if (view.kind === 'changes') state.paneParams[key] = { turn: view.turn, path: view.path }
  return openPane(key)
}

/** Take a tab on the right side full screen; closing it puts it back. */
export function fullScreenPane(key: PaneKey) {
  const view = openedViewOf(key)
  if (!view) return
  const threadId = view.kind === 'changes' ? view.threadId : null
  setFull({ view, threadId, fromSide: true })
  closePane(key)
}

/** Put the full-screen view in a tab on the right side. */
export function fullToSide() {
  const full = state.full
  setFull(null)
  if (full && state.currentId) void openView(full.view)
}

/** Close a view, full screen or in its tab, once it has nothing left to show, e.g. merged. */
export function closeView(view: OpenedView) {
  if (state.full && sameOpened(state.full.view, view)) setFull(null)
  else closePane(paneKeyOf(view))
}

const sameOpened = (a: OpenedView, b: OpenedView) => paneKeyOf(a) === paneKeyOf(b)

/** Close the full-screen view; one taken from the right side goes back there. */
export function closeFull() {
  const full = state.full
  setFull(null)
  if (full?.fromSide && state.currentId) void openView(full.view)
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
    void openView({ kind: 'artifact', id: artifact.id })
  } catch (error) {
    report(error)
  }
}

/** Save the open project's settings in one atomic update; returns why they were refused, or `null`. */
export async function saveProjectSettings(
  input: Parameters<typeof api.saveProjectSettings>[1],
  slots: number | null,
  mcp: string[],
): Promise<string | null> {
  const project = state.detail?.project
  if (!project) return null
  try {
    await api.updateProject(project.id, {
      settings: input.settings,
      ...(input.env === undefined ? {} : { env: input.env }),
      ...(slots !== project.slots ? { slots } : {}),
      ...(JSON.stringify(mcp) !== JSON.stringify(project.mcp) ? { mcp } : {}),
    })
    await refreshCurrent(['detail'])
    return null
  } catch (error) {
    if (error instanceof ApiError) return tm(error.localized)
    report(error)
    return null
  }
}

/** Pause the open project's autopilot, or let it go on. */
export async function setAutopilotPaused(paused: boolean) {
  const project = state.detail?.project
  if (!project) return
  try {
    await api.setAutopilotPaused(project.id, paused)
    await refreshCurrent(['detail'])
  } catch (error) {
    report(error)
  }
}

/** Download everything the project's log holds as a file. */
export async function exportProject(id: string) {
  try {
    saveDownload(await api.exportProject(id))
  } catch (error) {
    report(error)
  }
}

export function saveDownload({ blob, name }: { blob: Blob; name: string }) {
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = name
  link.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

export async function saveFileSources(
  projectId: string,
  input: Parameters<typeof api.setFileSources>[1],
): Promise<string | null> {
  try {
    await api.setFileSources(projectId, input)
    if (state.currentId === projectId) await refreshCurrent()
    return null
  } catch (error) {
    return error instanceof ApiError ? tm(error.localized) : String(error)
  }
}

/** Add a source the open project only reads; the reason when it was refused. */
export async function addReference(reference: {
  kind: string
  settings: Record<string, unknown>
}): Promise<string | null> {
  const project = state.detail?.project
  if (!project) return null
  try {
    await api.addReference(project.id, reference)
    await refreshCurrent()
    return null
  } catch (error) {
    if (error instanceof ApiError) return tm(error.localized)
    report(error)
    return null
  }
}

export async function removeReference(referenceId: string) {
  const project = state.detail?.project
  if (!project) return
  try {
    await api.removeReference(project.id, referenceId)
    await refreshCurrent()
  } catch (error) {
    report(error)
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

// Coalesce bursts of events into one refresh per frame (or a short window for high-frequency
// progress events), remembering every project touched and which parts of the open one.
const HIGH_FREQ_COALESCE_MS = 250
const HIGH_FREQ_EVENTS = new Set<string>([
  'turn.step',
  'research.step',
  'search.answered',
  'turn.changed',
  'branch.changes_synced',
])
let rafPending = false
let slowTimer: ReturnType<typeof setTimeout> | undefined
const touched = new Set<string>()
const parts = new Set<RefreshKey>()
const listeners = new Set<(event: AnyStoredEvent) => void>()
const updateStatusListeners = new Set<(status: UpdateStatus) => void>()

/** Hear every event as it arrives, e.g. to notify the user. */
export function onLiveEvent(listener: (event: AnyStoredEvent) => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

/** Hear pushed software update status and download progress changes. */
export function onUpdateStatus(listener: (status: UpdateStatus) => void): () => void {
  updateStatusListeners.add(listener)
  return () => updateStatusListeners.delete(listener)
}

/**
 * What an event changes of the open project's parts, by the group its type starts with.
 * A group not listed refreshes every part: a new event is never missed, only fetched more
 * than it needs.
 */
export function partsOf(type: AnyStoredEvent['type']): readonly Part[] {
  switch (type.split('.')[0]) {
    case 'prompt':
    case 'tool':
      return []
    case 'memory':
      return ['memories', 'conversations']
    case 'artifact':
      return ['artifacts', 'conversations']
    // What a turn does while it runs; its threads' stages on the plan stay as they are.
    case 'research':
    case 'search':
    case 'todo':
    case 'conversation':
    case 'notice':
    case 'routine':
    case 'project':
      return ['detail', 'conversations']
    // A recap is a line of the coordinator's conversation.
    case 'user':
      return ['conversations']
    case 'turn':
      return type === 'turn.step' || type === 'turn.changed'
        ? ['detail', 'conversations']
        : ['detail', 'conversations', 'plan']
    // Anything that may move a thread's stage, which the plan's nodes show.
    case 'message':
      // Reading only clears marks on the messages themselves.
      return type === 'message.read' ? ['conversations'] : ['detail', 'conversations', 'plan']
    case 'branch':
      return type === 'branch.changes_synced' ? ['detail'] : ['detail', 'conversations', 'plan']
    case 'plan':
    case 'thread':
    case 'attention':
    case 'workspace':
    case 'pull_request':
    case 'checks':
    case 'autopilot':
    case 'issue':
    case 'mcp':
      return ['detail', 'conversations', 'plan']
    default:
      return ALL_PARTS
  }
}

/** Whether an event can change a row in the project sidebar list (`ProjectView`). */
export function affectsProjectList(type: AnyStoredEvent['type']): boolean {
  switch (type.split('.')[0]) {
    case 'prompt':
    case 'tool':
    case 'todo':
    case 'memory':
    case 'artifact':
    case 'plan':
    case 'notice':
    case 'user':
      return false
    case 'message':
      return type === 'message.posted'
    case 'turn':
      return type !== 'turn.changed'
    case 'branch':
      return type !== 'branch.changes_synced'
    case 'conversation':
      return (
        type === 'conversation.wait_set' ||
        type === 'conversation.wait_ended' ||
        type === 'conversation.compacted'
      )
    default:
      return true
  }
}

/** Specific conversations an event touches, or `null` when any open conversation may change. */
export function touchedConversationsOf(
  event: AnyStoredEvent,
  coordinatorId: string | null = null,
): readonly string[] | null {
  const data = event.data as {
    conversationId?: unknown
    cites?: unknown
    author?: unknown
    comments?: unknown
  }
  const conversationId = typeof data?.conversationId === 'string' ? data.conversationId : null
  const group = event.type.split('.')[0]
  if (event.type === 'message.posted') {
    const ids = conversationId ? [conversationId] : []
    if (Array.isArray(data?.cites)) {
      for (const cite of data.cites) {
        const cited = (cite as { conversationId?: unknown })?.conversationId
        if (typeof cited === 'string' && !ids.includes(cited)) ids.push(cited)
      }
    }
    const hasComments = Array.isArray(data?.comments) && data.comments.length > 0
    // A user message in a thread without line comments also wakes the coordinator.
    if (data?.author === 'user' && !hasComments) {
      if (!coordinatorId) return null
      if (!ids.includes(coordinatorId)) ids.push(coordinatorId)
    }
    return ids.length ? ids : null
  }
  if (
    conversationId &&
    (group === 'turn' ||
      group === 'todo' ||
      group === 'research' ||
      group === 'search' ||
      group === 'conversation' ||
      group === 'message')
  ) {
    return [conversationId]
  }
  return null
}

function flushEvents() {
  rafPending = false
  if (slowTimer !== undefined) {
    clearTimeout(slowTimer)
    slowTimer = undefined
  }
  const currentId = state.currentId
  const changed = [...parts]
  const detailRefreshed =
    currentId !== null &&
    changed.includes('detail') &&
    state.projects.some((project) => project.id === currentId)
  const projects = [...touched].filter((id) => !(detailRefreshed && id === currentId))
  touched.clear()
  parts.clear()
  if (projects.length) void refreshProjects(projects).catch(report)
  if (currentId !== null && changed.length) void refreshCurrent(changed).catch(report)
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
  if (affectsProjectList(event.type)) touched.add(event.projectId)
  if (event.projectId === state.currentId) {
    for (const part of partsOf(event.type)) {
      if (part === 'conversations') {
        const conversations = touchedConversationsOf(
          event,
          state.detail?.project.coordinatorId ?? null,
        )
        if (conversations) {
          for (const cid of conversations) parts.add(`conversation:${cid}`)
        } else {
          parts.add('conversations')
        }
      } else {
        parts.add(part)
      }
    }
  }
  if (!touched.size && !parts.size) return
  if (HIGH_FREQ_EVENTS.has(event.type)) {
    if (!rafPending && slowTimer === undefined) {
      slowTimer = setTimeout(flushEvents, HIGH_FREQ_COALESCE_MS)
    }
    return
  }
  if (slowTimer !== undefined) {
    clearTimeout(slowTimer)
    slowTimer = undefined
  }
  if (rafPending) return
  rafPending = true
  requestAnimationFrame(flushEvents)
}

/** Take in the model settings as saved: whether the coordinator has one, and the choices. */
export function applyModelSettings(settings: ModelSettings) {
  const knownKinds = Array.isArray(settings.kinds)
    ? new Set(settings.kinds.map((kind) => kind.kind))
    : null
  const activeProviders = knownKinds
    ? settings.providers.filter((provider) => knownKinds.has(provider.kind))
    : settings.providers
  const activeProviderIds = new Set(activeProviders.map((provider) => provider.id))
  const activeModels = knownKinds
    ? settings.models.filter((model) => activeProviderIds.has(model.provider))
    : settings.models
  state.modelReady = !!settings.roles.thread.model
  state.threadDefault = settings.roles.thread.model
  state.threadDefaultEffort = settings.roles.thread.effort
  state.showCost = settings.showCost
  const labels = modelLabels(activeModels, activeProviders)
  state.models = activeModels.map((model, index) => ({
    id: model.id,
    label: labels[index]!.label,
    ...(model.efforts !== undefined ? { efforts: model.efforts } : {}),
  }))
}

/** Give a thread another model/effort, or back to the default with `model = null`. */
export async function setThreadModel(
  threadId: string,
  model: string | null,
  effort: Effort | null = null,
) {
  try {
    await api.setThreadModel(threadId, model, effort)
    await refreshCurrent()
  } catch (error) {
    report(error)
  }
}

async function load(restore: () => Promise<void>): Promise<number | null> {
  api.getModelSettings().then(applyModelSettings, () => {})
  api.getInputSettings().then(
    (input) => (state.input = input),
    () => {},
  )
  try {
    const lastSeq = await refreshProjects()
    await restore()
    return lastSeq
  } catch (error) {
    if (isUnauthorizedError(error)) {
      markUnauthorized()
      return null
    }
    report(error)
    return null
  }
}

export async function start(restore: () => Promise<void>) {
  streamSession = null
  stopStream?.()
  stopStream = null
  const lastSeq = await load(restore)
  if (state.stream === 'unauthorized') return
  // Follow from the state just loaded. If loading failed (say the page opened before the
  // server), follow from the server's newest event and load once the stream is up.
  let loaded = lastSeq !== null
  const session = (streamSession = Symbol())
  stopStream = followEvents(
    lastSeq,
    onEvent,
    (status) => {
      if (streamSession !== session || state.stream === 'unauthorized') return
      if (status === 'unauthorized') {
        markUnauthorized()
        return
      }
      state.stream = status
    },
    () => {
      if (streamSession !== session || state.stream === 'unauthorized') return
      // The server tells again what is running now.
      state.live = {}
      state.background = {}
      for (const [id, draft] of Object.entries(state.writing)) {
        if (!draft.posted) delete state.writing[id]
      }
      if (loaded) return
      loaded = true
      void load(restore)
    },
    onLive,
    onBackground,
    onDraft,
    (status) => {
      if (streamSession !== session || state.stream === 'unauthorized') return
      for (const listener of updateStatusListeners) listener(status)
    },
  )
}

/** How long a posted draft waits for its message to load before it goes anyway. */
const POSTED_KEEP_MS = 15_000

function onDraft(update: DraftUpdate) {
  const { conversationId, draft } = update
  if (!draft) {
    // A posted one is left to its message; only one being written is taken back.
    if (!state.writing[conversationId]?.posted) delete state.writing[conversationId]
    return
  }
  state.writing[conversationId] = draft
  if (!draft.posted) return
  setTimeout(() => {
    if (state.writing[conversationId]?.messageId === draft.messageId) {
      delete state.writing[conversationId]
    }
  }, POSTED_KEEP_MS)
}

function onBackground(update: BackgroundUpdate) {
  if (update.commands.length) state.background[update.conversationId] = update.commands
  else delete state.background[update.conversationId]
}

/** Wake a waiting conversation now, or call its wait off. */
export async function endWait(conversationId: string, how: 'now' | 'cancel') {
  await (how === 'now' ? api.wakeNow(conversationId) : api.cancelWait(conversationId)).catch(report)
}

export async function stopBackground(threadId: string, commandId: string) {
  await api.stopCommand(threadId, commandId).catch(report)
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
  projectId: string,
  threadId: string,
  comments: LineComment[],
  ids: readonly string[],
): Promise<boolean> {
  if (!comments.length) return false
  // The thread's own project, which need not be the one open now.
  const sent = await api
    .postMessage(projectId, threadId, '', { comments })
    .then(() => true)
    .catch((error: unknown) => {
      report(error)
      return false
    })
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

/** Take the thread's last merge back. */
export async function undoMerge(threadId: string) {
  await api.undoMerge(threadId).catch(report)
}

/** Let a thread the coordinator stopped go on. */
export async function resumeThread(threadId: string) {
  await api.resumeThread(threadId).catch(report)
}

/** Take back a source the user allowed one thread to read. */
export async function removeThreadReference(threadId: string, referenceId: string) {
  try {
    await api.removeThreadReference(threadId, referenceId)
    await refreshCurrent()
  } catch (error) {
    report(error)
  }
}

/** Keep such a source for the whole project. */
export async function keepThreadReference(threadId: string, referenceId: string) {
  try {
    await api.keepThreadReference(threadId, referenceId)
    await refreshCurrent()
  } catch (error) {
    report(error)
  }
}

/** The user takes up a thread that was set aside. */
export async function endAside(threadId: string) {
  await api.endAside(threadId).catch(report)
}

/** The user is through with what the thread put up for review. */
export async function completeThread(threadId: string) {
  await api.completeThread(threadId).catch(report)
}
