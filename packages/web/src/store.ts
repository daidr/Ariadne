import type {
  AnyStoredEvent,
  ArtifactKind,
  ArtifactSummary,
  ConversationView,
  MessageView,
  PlanOp,
  PlanView,
  ProjectView,
  TodoView,
} from '@ariadne/core'
import { reactive } from 'vue'
import { api, ApiError, followEvents, type ProjectDetail, type StreamStatus } from './api'
import { t, tm } from './i18n'

/** What the middle column shows. */
export type MainView =
  | { kind: 'conversation' }
  | { kind: 'plan' }
  | { kind: 'artifacts' }
  | { kind: 'artifact'; id: string }

interface State {
  stream: StreamStatus
  projects: ProjectView[]
  currentId: string | null
  detail: ProjectDetail | null
  /** The thread open in the middle column; `null` shows the coordinator. */
  threadId: string | null
  conversation: ConversationView | null
  messages: MessageView[]
  todo: TodoView | null
  plan: PlanView | null
  artifacts: ArtifactSummary[]
  main: MainView
  /** Bumped whenever the open project's events arrive, so open views can reload. */
  revision: number
  error: string | null
}

export const state = reactive<State>({
  stream: 'connecting',
  projects: [],
  currentId: null,
  detail: null,
  threadId: null,
  conversation: null,
  messages: [],
  todo: null,
  plan: null,
  artifacts: [],
  main: { kind: 'conversation' },
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
  const [conversation, plan, artifacts] = await Promise.all([
    api.getConversation(threadId ?? detail.project.coordinatorId),
    api.getPlan(id),
    api.listArtifacts(id),
  ])
  // The user may have switched while this was loading; a late answer is stale.
  if (state.currentId !== id || state.threadId !== threadId) return
  state.detail = detail
  state.conversation = conversation.conversation
  state.messages = conversation.messages
  state.todo = conversation.todo
  state.plan = plan.plan
  state.artifacts = artifacts.artifacts
  state.revision++
}

function clearConversation() {
  state.conversation = null
  state.messages = []
  state.todo = null
}

export async function openProject(id: string) {
  state.currentId = id
  state.threadId = null
  state.detail = null
  state.plan = null
  state.artifacts = []
  state.main = { kind: 'conversation' }
  clearConversation()
  await refreshCurrent().catch(report)
}

/** Show a thread's conversation in the middle column, or the coordinator's with `null`. */
export async function openThread(threadId: string | null) {
  state.main = { kind: 'conversation' }
  if (state.threadId === threadId) return
  state.threadId = threadId
  clearConversation()
  await refreshCurrent().catch(report)
}

/** The conversation open in the middle column. */
export function currentConversationId(): string | null {
  return state.threadId ?? state.detail?.project.coordinatorId ?? null
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

export async function sendMessage(content: string) {
  const project = state.detail?.project
  const conversationId = currentConversationId()
  if (!project || !conversationId) return
  await api.postMessage(project.id, conversationId, content).catch(report)
}

export async function stopTurn() {
  const conversationId = currentConversationId()
  if (conversationId) await api.stopTurn(conversationId).catch(report)
}

export async function retryTurn() {
  const conversationId = currentConversationId()
  if (conversationId) await api.retryTurn(conversationId).catch(report)
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
export async function editPlan(reason: string, ops: PlanOp[]): Promise<boolean> {
  const project = state.detail?.project
  if (!project || !ops.length) return false
  try {
    state.plan = (await api.revisePlan(project.id, { reason, ops })).plan
    return true
  } catch (error) {
    report(error)
    return false
  }
}

export async function revertPlan(revisionId: string) {
  const project = state.detail?.project
  if (!project) return
  try {
    state.plan = (await api.revertPlan(project.id, revisionId, t('plan.revertReason'))).plan
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
function onEvent(event: AnyStoredEvent) {
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

async function load(): Promise<number | null> {
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
