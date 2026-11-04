import type {
  AnyStoredEvent,
  ArtifactKind,
  ArtifactSummary,
  ConversationView,
  MemoryView,
  MessageView,
  NoticeView,
  PlanOp,
  PlanView,
  ProjectView,
  TodoView,
} from '@ariadne/core'
import { reactive } from 'vue'
import {
  api,
  ApiError,
  followEvents,
  type ModelSettings,
  type ProjectDetail,
  type StreamStatus,
} from './api'
import { t, tm } from './i18n'

/** What the middle column shows. */
export type MainView =
  | { kind: 'conversation' }
  | { kind: 'plan' }
  | { kind: 'artifacts' }
  | { kind: 'artifact'; id: string }
  /** What a thread's branch changed, with merging it. */
  | { kind: 'changes'; threadId: string }
  /** What a conversation's agent did, turn by turn, for finding out what went wrong. */
  | { kind: 'trace'; conversationId: string }

/** One conversation as shown on screen: the coordinator's, or the open thread's. */
export interface ConversationState {
  conversation: ConversationView | null
  messages: MessageView[]
  /** What happened in the conversation besides messages, shown in line with them. */
  notices: NoticeView[]
  todo: TodoView | null
  /** The model it uses now, with how much its context holds. */
  model: { id: string; contextTokens: number } | null
}

const emptyConversation = (): ConversationState => ({
  conversation: null,
  messages: [],
  notices: [],
  todo: null,
  model: null,
})

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
  /** Bumped whenever the open project's events arrive, so open views can reload. */
  revision: number
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
  revision: 0,
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

async function refreshCurrent() {
  const id = state.currentId
  const threadId = state.threadId
  if (!id) return
  const detail = await api.getProject(id)
  const [coordinator, thread, plan, artifacts, memory] = await Promise.all([
    api.getConversation(detail.project.coordinatorId),
    threadId ? api.getConversation(threadId) : null,
    api.getPlan(id),
    api.listArtifacts(id),
    api.listMemories(id),
  ])
  // The user may have switched while this was loading; a late answer is stale.
  if (state.currentId !== id || state.threadId !== threadId) return
  state.detail = detail
  state.coordinator = pick(coordinator)
  state.thread = thread ? pick(thread) : emptyConversation()
  state.plan = plan.plan
  state.artifacts = artifacts.artifacts
  state.memories = memory.memories
  state.revision++
}

function pick(view: ConversationState): ConversationState {
  const { conversation, messages, notices, todo, model } = view
  return { conversation, messages, notices, todo, model }
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
  await refreshCurrent().catch(report)
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

export async function sendMessage(conversationId: string, content: string) {
  const project = state.detail?.project
  if (!project) return
  await api.postMessage(project.id, conversationId, content).catch(report)
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

/** Cap how many turns of the project run at once; returns why it was refused, or `null`. */
export async function setProjectSlots(slots: number | null): Promise<string | null> {
  const project = state.detail?.project
  if (!project) return null
  try {
    await api.setProjectSlots(project.id, slots)
    await refreshCurrent()
    return null
  } catch (error) {
    if (error instanceof ApiError) return tm(error.localized)
    report(error)
    return null
  }
}

/** Save the project's working directory; returns why it was refused, or `null`. */
export async function setSource(
  source: { kind: string; settings: Record<string, unknown> } | null,
): Promise<string | null> {
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

export async function revertPlan(revisionId: string) {
  const project = state.detail?.project
  if (!project) return
  try {
    const { plan } = await api.revertPlan(project.id, revisionId, t('plan.revertReason'))
    if (state.detail?.project.id === plan.projectId) state.plan = plan
  } catch (error) {
    report(error)
  }
}

export async function answerAttention(id: string, choice: number) {
  await api.answerAttention(id, choice).catch(report)
}

// Coalesce bursts of events into one refresh per frame, remembering every project touched.
let pending = false
const touched = new Set<string>()
const listeners = new Set<(event: AnyStoredEvent) => void>()

/** Hear every event as it arrives, e.g. to notify the user. */
export function onLiveEvent(listener: (event: AnyStoredEvent) => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

function onEvent(event: AnyStoredEvent) {
  for (const listener of listeners) listener(event)
  touched.add(event.projectId)
  if (pending) return
  pending = true
  requestAnimationFrame(() => {
    pending = false
    const current = state.currentId !== null && touched.has(state.currentId)
    touched.clear()
    void refreshProjects().catch(report)
    if (current) void refreshCurrent().catch(report)
  })
}

/** Take in the model settings as saved: whether the coordinator has one, and the choices. */
export function applyModelSettings(settings: ModelSettings) {
  state.modelReady = !!settings.roles.coordinator
  state.threadDefault = settings.roles.thread ?? settings.roles.coordinator
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
      if (loaded) return
      loaded = true
      void load()
    },
  )
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
