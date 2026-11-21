// @vitest-environment happy-dom
import type {
  ConversationView,
  MessageView,
  PlanView,
  ProjectView,
  ThreadView,
} from '@ariadne/core'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { nextTick } from 'vue'
import { createMemoryHistory } from 'vue-router'
import App from '../App.vue'
import { api } from '../api'
import { createWorkspaceRouter } from '../workspace-route'
import * as side from '../side/layout'
import {
  backMain,
  closePane,
  coordinatorComposerFor,
  deleteProject,
  emptyConversation,
  merge,
  openMain,
  openProject,
  openThread,
  openView,
  setSide,
  startNewProject,
  state,
} from '../store'

function makeProject(id: string, coordinatorId: string): ProjectView {
  return {
    id,
    name: `Project ${id}`,
    unnamed: false,
    coordinatorId,
    createdAt: 1,
    updatedAt: 1,
    needsMe: 0,
    slots: null,
    autopilotPaused: false,
    settings: {
      instructions: '',
      delivery: { mode: 'pr', instructions: '' },
      autopilot: { enabled: false, method: 'merge', maxThreads: 0, maxProject: 0 },
      workspace: { retention: 'keep' },
      routines: [],
    },
    source: null,
    fileSources: [],
    references: [],
    mcp: [],
    detected: { setup: null, check: null },
    usage: { input: 0, output: 0, cached: 0, cacheWrite: 0, reasoning: 0, costUsd: 0, costCny: 0 },
    runningTurns: 0,
  } as unknown as ProjectView
}

function makeThread(id: string, projectId = 'prj_1'): ThreadView {
  return {
    id,
    projectId,
    title: `Thread ${id}`,
    stage: 'working',
    createdAt: 1,
    updatedAt: 1,
    lastMessageAt: 1,
    archived: false,
    auto: false,
    reviewOf: null,
    review: null,
    workspace: null,
    pullRequest: null,
    merged: null,
    stopped: null,
    aside: null,
    doing: null,
    lastHeadline: null,
    model: null,
    references: [],
    endedReferences: [],
  } as unknown as ThreadView
}

function makeConversation(id: string, projectId = 'prj_1'): ConversationView {
  return {
    id,
    projectId,
    turn: null,
    pendingSeq: 0,
    lastOutcome: 'completed',
    lastError: null,
    wait: null,
    contextTokens: 0,
    compactions: 0,
    usage: { input: 0, output: 0, cached: 0, cacheWrite: 0, reasoning: 0, costUsd: 0, costCny: 0 },
  } as unknown as ConversationView
}

function makeMessage(
  id: string,
  conversationId: string,
  content: string,
  author: 'user' | 'coordinator' | 'thread' = 'coordinator',
): MessageView {
  return {
    id,
    conversationId,
    author,
    kind: 'chat',
    content,
    blocks: [],
    ask: null,
    attachments: [],
    mentions: [],
    comments: [],
    cites: [],
    relayedTo: [],
    replyTo: null,
    reactions: [],
    createdAt: Date.now(),
    editedAt: null,
    editReason: null,
    originalContent: null,
    editedSinceRead: false,
  } as unknown as MessageView
}

async function mountApp() {
  const router = createWorkspaceRouter(createMemoryHistory(), App)
  await router.replace('/projects/prj_1')
  const wrapper = mount(App, { global: { plugins: [router] }, attachTo: document.body })
  await flushPromises()
  await nextTick()
  return wrapper
}

const fullTextarea = () =>
  document.querySelector('.full-conversation textarea') as HTMLTextAreaElement

describe('conversation draft preservation', () => {
  const project1 = makeProject('prj_1', 'crd_1')
  const project2 = makeProject('prj_2', 'crd_2')
  const thread1 = makeThread('thr_1', 'prj_1')
  const thread2 = makeThread('thr_2', 'prj_1')

  beforeEach(() => {
    localStorage.clear()
    vi.stubGlobal(
      'EventSource',
      class {
        addEventListener() {}
        close() {}
      },
    )

    vi.spyOn(api, 'getSkills').mockResolvedValue({ skills: [] })
    vi.spyOn(api, 'markRead').mockResolvedValue({} as never)
    vi.spyOn(api, 'listProjects').mockResolvedValue({ projects: [project1, project2], lastSeq: 1 })
    vi.spyOn(api, 'getModelSettings').mockResolvedValue({
      models: [],
      providers: [],
      roles: { thread: { model: 'm1' } },
      showCost: false,
    } as never)
    vi.spyOn(api, 'getInputSettings').mockResolvedValue({
      submitOn: 'enter',
      snippets: [],
      keys: {},
    } as never)
    vi.spyOn(api, 'getProject').mockImplementation(async (id) => ({
      project: id === 'prj_2' ? project2 : project1,
      threads: id === 'prj_2' ? [] : [thread1, thread2],
      needsMe: [],
      hasTracker: false,
      sync: { cursor: '1', partial: false },
    }))
    vi.spyOn(api, 'getConversation').mockImplementation(async (id) => ({
      conversation: makeConversation(id, id === 'crd_2' ? 'prj_2' : 'prj_1'),
      messages: [],
      notices: [],
      todo: { conversationId: id, items: [], updatedAt: 0 },
      model: null,
      more: false,
      lastSeq: 1,
    }))
    vi.spyOn(api, 'getPlan').mockResolvedValue({
      plan: { nodes: [], revisions: [], proposals: [] } as unknown as PlanView,
    })
    vi.spyOn(api, 'listArtifacts').mockResolvedValue({ artifacts: [] })
    vi.spyOn(api, 'listMemories').mockResolvedValue({ memories: [] })
    vi.spyOn(api, 'sourceKinds').mockResolvedValue({ kinds: [] })
    vi.spyOn(api, 'getTrace').mockResolvedValue({ turns: [], more: false })
    vi.spyOn(api, 'getChanges').mockResolvedValue({
      branch: 'main',
      base: 'main',
      files: [],
      total: { files: 0, added: 0, removed: 0 },
    } as never)
    vi.spyOn(api, 'postMessage').mockResolvedValue({
      message: makeMessage('msg_sent', 'crd_1', 'ok', 'user'),
    } as never)
    vi.spyOn(api, 'deleteProject').mockResolvedValue({} as never)

    state.projects = [project1, project2]
    state.currentId = 'prj_1'
    state.detail = {
      project: project1,
      threads: [thread1, thread2],
      needsMe: [],
      hasTracker: false,
    }
    state.coordinator = {
      ...emptyConversation(),
      conversation: makeConversation('crd_1', 'prj_1'),
      seq: 1,
    }
    state.threads = {}
    state.artifacts = []
    state.plan = { nodes: [], revisions: [], proposals: [] } as unknown as PlanView
    state.writing = {}
    state.live = {}
    state.background = {}
    state.full = null
    state.main = { kind: 'conversation' }
    state.starting = false
    state.planWide = false
    setSide(side.defaultLayout())

    // Reset composers used across tests.
    coordinatorComposerFor('prj_1').draft.value = ''
    coordinatorComposerFor('prj_1').files.value = []
    coordinatorComposerFor('prj_2').draft.value = ''
    coordinatorComposerFor('prj_2').files.value = []
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  test('preserves coordinator and open thread drafts across trace, plan, artifacts, start screen, and incoming messages', async () => {
    const wrapper = await mountApp()

    const coordTa = () => wrapper.get('[data-panel="coordinator"] textarea')
    await coordTa().setValue('协调器未发送草稿')
    expect((coordTa().element as HTMLTextAreaElement).value).toBe('协调器未发送草稿')

    await openThread('thr_1')
    await flushPromises()
    await nextTick()
    const threadTa = () => wrapper.get('[data-panel="thr_1"] textarea')
    await threadTa().setValue('讨论串未发送草稿')

    // Open coordinator trace (unmounts projectBody & ConversationPanel) and receive a new message while there.
    openMain({ kind: 'trace', conversationId: 'crd_1' })
    await flushPromises()
    await nextTick()
    expect(wrapper.find('[data-panel="coordinator"]').exists()).toBe(false)

    state.coordinator = merge(state.coordinator, {
      conversation: makeConversation('crd_1', 'prj_1'),
      messages: [makeMessage('msg_1', 'crd_1', '追踪期间到达的新消息')],
      notices: [],
      todo: { conversationId: 'crd_1', items: [], updatedAt: 1 },
      model: null,
      more: false,
      lastSeq: 2,
    })

    backMain()
    await flushPromises()
    await nextTick()
    expect((coordTa().element as HTMLTextAreaElement).value).toBe('协调器未发送草稿')
    expect((threadTa().element as HTMLTextAreaElement).value).toBe('讨论串未发送草稿')

    // Switch through plan, artifacts, and start-new-project screen, then return.
    openMain({ kind: 'plan' })
    await flushPromises()
    await nextTick()
    openMain({ kind: 'artifacts' })
    await flushPromises()
    await nextTick()
    startNewProject()
    await flushPromises()
    await nextTick()
    state.starting = false
    openMain({ kind: 'conversation' })
    await flushPromises()
    await nextTick()

    expect((coordTa().element as HTMLTextAreaElement).value).toBe('协调器未发送草稿')
    expect((threadTa().element as HTMLTextAreaElement).value).toBe('讨论串未发送草稿')

    // New message arriving while actively viewing coordinator keeps draft intact.
    state.coordinator = merge(state.coordinator, {
      conversation: makeConversation('crd_1', 'prj_1'),
      messages: [makeMessage('msg_2', 'crd_1', '另一条新消息')],
      notices: [],
      todo: { conversationId: 'crd_1', items: [], updatedAt: 2 },
      model: null,
      more: false,
      lastSeq: 3,
    })
    await nextTick()
    expect((coordTa().element as HTMLTextAreaElement).value).toBe('协调器未发送草稿')

    wrapper.unmount()
  })

  test('isolates coordinator and thread drafts per project and drops them when the project is deleted', async () => {
    const wrapper = await mountApp()
    const coordTa = () => wrapper.get('[data-panel="coordinator"] textarea')

    await coordTa().setValue('项目一的草稿')
    await openThread('thr_1')
    await flushPromises()
    await nextTick()
    await wrapper.get('[data-panel="thr_1"] textarea').setValue('项目一讨论串草稿')

    // Switch to project 2: must NOT leak project 1's draft.
    await openProject('prj_2')
    await flushPromises()
    await nextTick()
    expect((coordTa().element as HTMLTextAreaElement).value).toBe('')
    await coordTa().setValue('项目二的草稿')

    // Switch back to project 1: both coordinator and open thread drafts return.
    await openProject('prj_1')
    await flushPromises()
    await nextTick()
    expect((coordTa().element as HTMLTextAreaElement).value).toBe('项目一的草稿')
    expect(
      (wrapper.get('[data-panel="thr_1"] textarea').element as HTMLTextAreaElement).value,
    ).toBe('项目一讨论串草稿')

    // Closing the thread tab drops that thread's draft, while keeping the coordinator draft.
    closePane(side.threadKey('thr_1'))
    await flushPromises()
    await nextTick()
    await openThread('thr_1')
    await flushPromises()
    await nextTick()
    expect(
      (wrapper.get('[data-panel="thr_1"] textarea').element as HTMLTextAreaElement).value,
    ).toBe('')
    expect((coordTa().element as HTMLTextAreaElement).value).toBe('项目一的草稿')

    // Deleting project 1 releases its composer.
    const revoke = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    coordinatorComposerFor('prj_1').files.value = [
      { uid: 'f1', name: 'a.png', size: 1, type: 'image/png', url: 'blob:test-1' },
    ]
    await deleteProject('prj_1')
    await flushPromises()
    expect(revoke).toHaveBeenCalledWith('blob:test-1')
    expect(coordinatorComposerFor('prj_1').draft.value).toBe('')

    wrapper.unmount()
  })

  test('switches thread composer cleanly in FullView without leaking drafts between threads', async () => {
    const wrapper = await mountApp()
    await openView({ kind: 'changes', threadId: 'thr_1' }, { from: 'thr_1' })
    await flushPromises()
    await nextTick()

    expect(fullTextarea()).not.toBeNull()
    fullTextarea().value = '讨论串一的全屏草稿'
    fullTextarea().dispatchEvent(new Event('input'))
    await nextTick()
    expect(fullTextarea().value).toBe('讨论串一的全屏草稿')

    // Switch FullView to thr_2: must show thr_2's own empty draft.
    await openThread('thr_2')
    await flushPromises()
    await nextTick()
    expect(fullTextarea().value).toBe('')

    wrapper.unmount()
  })

  test('sending clears the coordinator composer and restores it on send failure', async () => {
    const wrapper = await mountApp()
    const coordTa = () => wrapper.get('[data-panel="coordinator"] textarea')

    await coordTa().setValue('要发送的内容')
    const send = vi.spyOn(api, 'postMessage').mockRejectedValueOnce(new Error('network down'))
    await coordTa().trigger('keydown', { key: 'Enter' })
    await flushPromises()
    await nextTick()
    expect(send).toHaveBeenCalledTimes(1)
    expect((coordTa().element as HTMLTextAreaElement).value).toBe('要发送的内容')

    send.mockResolvedValueOnce({
      message: makeMessage('msg_sent', 'crd_1', '要发送的内容', 'user'),
    } as never)
    await coordTa().trigger('keydown', { key: 'Enter' })
    await flushPromises()
    await nextTick()
    expect(send).toHaveBeenCalledTimes(2)
    expect((coordTa().element as HTMLTextAreaElement).value).toBe('')
    expect(coordinatorComposerFor('prj_1').draft.value).toBe('')

    wrapper.unmount()
  })
})
