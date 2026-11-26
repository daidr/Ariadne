import type {
  ArtifactSummary,
  ConversationView,
  PlanView,
  ProjectView,
  ThreadView,
} from '@ariadne/core'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { nextTick } from 'vue'
import { createMemoryHistory } from 'vue-router'
import { api } from './api'
import * as side from './side/layout'
import {
  artifactNavigation,
  captureWorkspace,
  closeFull,
  closePane,
  navigateArtifactInPlace,
  openGallery,
  openProject,
  openProjectArtifact,
  openProjectThread,
  openThread,
  openView,
  setSide,
  showWorkspace,
  state,
} from './store'
import { createWorkspaceRouter } from './workspace-route'
import { connectWorkspaceRouter } from './workspace-routing'

const projects = ['prj_first', 'prj_second'].map(
  (id) => ({ id, coordinatorId: `crd_${id}` }) as ProjectView,
)
const thread = { id: 'thr_second' } as ThreadView
const artifact = { id: 'art_second', kind: 'document' } as ArtifactSummary
const boardArtifact = { id: 'art_board', kind: 'whiteboard' } as ArtifactSummary
const app = { render: () => null }
const bindings: ReturnType<typeof connectWorkspaceRouter>[] = []

beforeEach(() => {
  state.projects = projects
  state.currentId = null
  state.detail = null
  vi.spyOn(api, 'getProject').mockImplementation(async (id) => ({
    project: projects.find((project) => project.id === id)!,
    threads: id === 'prj_second' ? [thread] : [],
    needsMe: [],
    hasTracker: false,
    sync: { cursor: '1', partial: false },
  }))
  vi.spyOn(api, 'getConversation').mockImplementation(async (id) => ({
    conversation: { id } as ConversationView,
    messages: [],
    notices: [],
    todo: { conversationId: id, items: [], updatedAt: 0 },
    model: null,
    more: false,
    lastSeq: 1,
  }))
  vi.spyOn(api, 'getPlan').mockResolvedValue({ plan: { nodes: [] } as unknown as PlanView })
  vi.spyOn(api, 'listArtifacts').mockResolvedValue({ artifacts: [artifact, boardArtifact] })
  vi.spyOn(api, 'listMemories').mockResolvedValue({ memories: [] })
})

afterEach(() => {
  for (const binding of bindings.splice(0)) binding.stop()
  vi.restoreAllMocks()
})

async function enter(path: string, saved = {}) {
  const history = createMemoryHistory()
  const router = createWorkspaceRouter(history, app)
  const url = router.resolve(path)
  await router.replace({ path: url.path, query: url.query, state: saved })
  const binding = connectWorkspaceRouter(router)
  bindings.push(binding)
  await binding.restore()
  await vi.waitFor(() => expect(history.state.ariadneWorkspace).toBeTypeOf('string'))
  return { router, history, binding }
}

describe('workspace routes', () => {
  test('refresh restores the project, dock, a turn diff and a document modal', async () => {
    const { router, history, binding } = await enter('/projects/prj_second')
    await openThread(thread.id, { beside: 'vertical' })
    const turn = {
      turnId: 'tu_second',
      from: 'a',
      to: 'b',
      files: [],
      total: { files: 0, added: 0, removed: 0 },
    }
    await openView({ kind: 'changes', threadId: thread.id, turn, path: 'example.txt' })
    await openView({ kind: 'artifact', id: artifact.id }, { from: thread.id })
    await vi.waitFor(() =>
      expect(router.currentRoute.value.query.modal).toBe('artifact:art_second'),
    )
    const address = router.currentRoute.value.fullPath
    if (state.side.root.type !== 'split') throw new Error('expected a split dock')
    setSide({ ...state.side, root: { ...state.side.root, size: 37 } })
    await nextTick()
    const before = JSON.parse(JSON.stringify(captureWorkspace()))

    binding.stop()
    state.currentId = null
    state.detail = null
    const refreshed = await enter(address, history.state)

    expect(refreshed.router.currentRoute.value.fullPath).toBe(address)
    expect(captureWorkspace()).toEqual(before)
    expect(state.threads[thread.id]?.conversation?.id).toBe(thread.id)
    closeFull()
    closePane(side.turnKey(thread.id, turn.turnId))
    expect(side.focusedThread(state.side)).toBe(thread.id)
  })

  test('refresh keeps background tab changes that do not change the URL', async () => {
    const { router, history, binding } = await enter('/projects/prj_second?pane=side&tab=threads')
    await openView({ kind: 'artifact', id: artifact.id })
    setSide(side.activate(state.side, 'threads'))
    await vi.waitFor(() => expect(router.currentRoute.value.query.tab).toBe('threads'))
    await vi.waitFor(() => expect(history.state.ariadneWorkspace).toContain('artifact:art_second'))
    const address = router.currentRoute.value.fullPath
    const before = history.state.ariadneWorkspace
    closePane(side.artifactKey(artifact.id))
    await vi.waitFor(() => expect(history.state.ariadneWorkspace).not.toBe(before))
    expect(router.currentRoute.value.fullPath).toBe(address)
    binding.stop()
    state.currentId = null
    state.detail = null
    await enter(address, history.state)
    expect(side.groupsOf(state.side.root).flatMap(side.tabsOf)).toEqual(side.LISTS)
    expect(state.workspaceLevel).toBe('side')
  })

  test('browser back restores the previous project and selected tab', async () => {
    const { router } = await enter('/projects/prj_second?pane=side&tab=thread:thr_second')
    await openProject('prj_first')
    await vi.waitFor(() => expect(router.currentRoute.value.params.projectId).toBe('prj_first'))
    router.back()
    await vi.waitFor(() => expect(state.detail?.project.id).toBe('prj_second'))
    expect(side.focusedThread(state.side)).toBe(thread.id)
    expect(state.workspaceLevel).toBe('side')
  })

  test('a missing project falls back to the project list and a removed tab falls back to a list', async () => {
    const { router } = await enter('/projects/prj_missing?pane=side&tab=thread:thr_missing')
    expect(state.currentId).toBe('prj_first')
    expect(state.workspaceLevel).toBe('projects')
    await router.push('/projects/prj_second?pane=side&tab=thread:thr_missing')
    await vi.waitFor(() => expect(state.detail?.project.id).toBe('prj_second'))
    expect(side.focusedThread(state.side)).toBeNull()
    expect(state.workspaceLevel).toBe('side')
    showWorkspace('coordinator')
    await vi.waitFor(() => expect(router.currentRoute.value.query.pane).toBeUndefined())
  })

  test('gallery route loads directly and browser back returns to gallery after opening a project artifact', async () => {
    const { router } = await enter('/gallery')
    expect(state.main).toEqual({ kind: 'gallery' })
    expect(state.workspaceLevel).toBe('coordinator')
    expect(router.currentRoute.value.fullPath).toBe('/gallery')

    await openProjectArtifact('prj_second', artifact.id)
    await vi.waitFor(() =>
      expect(router.currentRoute.value.fullPath).toContain('/projects/prj_second'),
    )
    expect(state.main.kind).toBe('conversation')

    router.back()
    await vi.waitFor(() => expect(state.main).toEqual({ kind: 'gallery' }))
    expect(router.currentRoute.value.fullPath).toBe('/gallery')

    await openProject('prj_first')
    await vi.waitFor(() => expect(router.currentRoute.value.params.projectId).toBe('prj_first'))
    openGallery()
    await vi.waitFor(() => expect(router.currentRoute.value.fullPath).toBe('/gallery'))

    // Opening a thread in the already-loaded project while on /gallery exits the gallery view.
    await openProject('prj_second')
    openGallery()
    await vi.waitFor(() => expect(state.main).toEqual({ kind: 'gallery' }))
    await openProjectThread('prj_second', thread.id)
    expect(state.main.kind).toBe('conversation')
    expect(side.focusedThread(state.side)).toBe(thread.id)
  })

  test('refresh and browser back preserve artifact Back/Forward history', async () => {
    const { router, history, binding } = await enter('/projects/prj_second')
    await openView({ kind: 'artifact', id: artifact.id }, { from: thread.id })
    await vi.waitFor(() =>
      expect(router.currentRoute.value.query.modal).toBe(`artifact:${artifact.id}`),
    )

    navigateArtifactInPlace({
      place: 'full',
      fromId: artifact.id,
      toId: boardArtifact.id,
      focusNodeId: 'grp-nav',
    })
    await vi.waitFor(() =>
      expect(router.currentRoute.value.query.modal).toBe(`artifact:${boardArtifact.id}`),
    )
    const address = router.currentRoute.value.fullPath
    const savedState = history.state

    // Browser back moves back to the original document while keeping forward history.
    router.back()
    await vi.waitFor(() => expect(state.full?.view).toEqual({ kind: 'artifact', id: artifact.id }))
    expect(artifactNavigation('full', artifact.id)).toEqual({
      back: null,
      forward: { id: boardArtifact.id, focusNodeId: 'grp-nav' },
    })

    // Refreshing on the navigated artifact restores its Back history from history.state.
    binding.stop()
    state.currentId = null
    state.detail = null
    await enter(address, savedState)
    expect(state.full?.view).toEqual({ kind: 'artifact', id: boardArtifact.id })
    expect(artifactNavigation('full', boardArtifact.id)).toEqual({
      back: { id: artifact.id },
      forward: null,
    })
  })
})
