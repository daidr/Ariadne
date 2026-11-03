import type { AnyStoredEvent, MessageView, ProjectView, TodoView } from '@ariadne/core'
import { reactive } from 'vue'
import { api, ApiError, followEvents, type ProjectDetail, type StreamStatus } from './api'
import { tm } from './i18n'

interface State {
  stream: StreamStatus
  projects: ProjectView[]
  currentId: string | null
  detail: ProjectDetail | null
  messages: MessageView[]
  todo: TodoView | null
  error: string | null
}

export const state = reactive<State>({
  stream: 'connecting',
  projects: [],
  currentId: null,
  detail: null,
  messages: [],
  todo: null,
  error: null,
})

function report(error: unknown) {
  state.error = error instanceof ApiError ? tm(error.localized) : String(error)
}

async function refreshProjects() {
  const { projects, lastSeq } = await api.listProjects()
  state.projects = projects
  return lastSeq
}

async function refreshCurrent() {
  const id = state.currentId
  if (!id) return
  const detail = await api.getProject(id)
  const conversation = await api.getConversation(detail.project.coordinatorId)
  // The user may have switched projects while this was loading; a late answer is stale.
  if (state.currentId !== id) return
  state.detail = detail
  state.messages = conversation.messages
  state.todo = conversation.todo
}

export async function openProject(id: string) {
  state.currentId = id
  state.detail = null
  state.messages = []
  state.todo = null
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

export async function sendToCoordinator(content: string) {
  const project = state.detail?.project
  if (!project) return
  await api.postMessage(project.id, project.coordinatorId, content).catch(report)
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

export async function start() {
  let lastSeq = 0
  try {
    lastSeq = await refreshProjects()
    const first = state.projects[0]
    if (first) await openProject(first.id)
  } catch (error) {
    report(error)
  }
  // Follow from the state just loaded; anything older is already on screen.
  followEvents(lastSeq, onEvent, (status) => (state.stream = status))
}
