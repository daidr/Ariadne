import type { AnyStoredEvent, MessageView, ProjectView, TodoView } from '@ariadne/core'
import { reactive } from 'vue'
import { api, ApiError, followEvents, type ProjectDetail } from './api'
import { tm } from './i18n'

interface State {
  connected: boolean
  projects: ProjectView[]
  currentId: string | null
  detail: ProjectDetail | null
  messages: MessageView[]
  todo: TodoView | null
  error: string | null
}

export const state = reactive<State>({
  connected: false,
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
  state.projects = (await api.listProjects()).projects
}

async function refreshCurrent() {
  if (!state.currentId) return
  const detail = await api.getProject(state.currentId)
  const conversation = await api.getConversation(detail.project.coordinatorId)
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

// Coalesce bursts of events into one refresh per frame.
let pending = false
function onEvent(event: AnyStoredEvent) {
  if (pending) return
  pending = true
  requestAnimationFrame(() => {
    pending = false
    void refreshProjects().catch(report)
    if (event.projectId === state.currentId) void refreshCurrent().catch(report)
  })
}

export async function start() {
  try {
    await refreshProjects()
    const first = state.projects[0]
    if (first) await openProject(first.id)
  } catch (error) {
    report(error)
  }
  followEvents(onEvent, (connected) => (state.connected = connected))
}
