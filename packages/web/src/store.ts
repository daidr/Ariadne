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
  /** What happened in the conversation besides messages, shown in line with them. */
  notices: NoticeView[]
  todo: TodoView | null
  plan: PlanView | null
  artifacts: ArtifactSummary[]
  /** The project's memory: standing preferences every conversation sees. */
  memories: MemoryView[]
  main: MainView
  /** Whether a default model is set; `null` until known. */
  modelReady: boolean | null
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
  threadId: null,
  conversation: null,
  messages: [],
  notices: [],
  todo: null,
  plan: null,
  artifacts: [],
  memories: [],
  main: { kind: 'conversation' },
  modelReady: null,
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
  const [conversation, plan, artifacts, memory] = await Promise.all([
    api.getConversation(threadId ?? detail.project.coordinatorId),
    api.getPlan(id),
    api.listArtifacts(id),
    api.listMemories(id),
  ])
  // The user may have switched while this was loading; a late answer is stale.
  if (state.currentId !== id || state.threadId !== threadId) return
  state.detail = detail
  state.conversation = conversation.conversation
  state.messages = conversation.messages
  state.notices = conversation.notices
  state.todo = conversation.todo
  state.plan = plan.plan
  state.artifacts = artifacts.artifacts
  state.memories = memory.memories
  state.revision++
}

function clearConversation() {
  state.conversation = null
  state.messages = []
  state.notices = []
  state.todo = null
}

export async function openProject(id: string) {
  state.currentId = id
  state.threadId = null
  state.detail = null
  state.plan = null
  state.artifacts = []
  state.memories = []
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
  api.getModelSettings().then(
    (settings) => (state.modelReady = !!settings.default),
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
