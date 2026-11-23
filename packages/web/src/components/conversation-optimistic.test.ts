// @vitest-environment happy-dom
import type { ConversationView, MessageView, ProjectView } from '@ariadne/core'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { api } from '../api'
import { coordinatorComposerFor, emptyConversation, merge, state } from '../store'
import ConversationPanel from './ConversationPanel.vue'

function makeMessage(
  id: string,
  content: string,
  overrides: Partial<MessageView> = {},
): MessageView {
  return {
    id,
    conversationId: 'crd_1',
    author: 'user',
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
    createdAt: 1000,
    editedAt: null,
    editReason: null,
    originalContent: null,
    editedSinceRead: false,
    ...overrides,
  }
}

const project = {
  id: 'prj_1',
  name: 'Project 1',
  coordinatorId: 'crd_1',
  source: null,
} as unknown as ProjectView

const conversation = {
  id: 'crd_1',
  projectId: 'prj_1',
  turn: null,
  pendingSeq: 0,
  lastOutcome: 'completed',
  lastError: null,
  wait: null,
  contextTokens: 0,
  compactions: 0,
  usage: { input: 0, output: 0, cached: 0, cacheWrite: 0, reasoning: 0, costUsd: 0, costCny: 0 },
} as unknown as ConversationView

describe('ConversationPanel optimistic user messages', () => {
  beforeEach(() => {
    vi.stubGlobal(
      'ResizeObserver',
      class {
        observe() {}
        unobserve() {}
        disconnect() {}
      },
    )
    vi.spyOn(api, 'getSkills').mockResolvedValue({ skills: [] })
    vi.spyOn(api, 'markRead').mockResolvedValue(undefined)
    state.currentId = 'prj_1'
    state.detail = { project, threads: [], needsMe: [], hasTracker: false }
    state.coordinator = { ...emptyConversation(), conversation, seq: 1 }
    state.threads = {}
    state.artifacts = []
    state.writing = {}
    state.live = {}
    state.background = {}
    state.pendingMessages = {}
    state.modelReady = true
    coordinatorComposerFor('prj_1').draft.value = ''
    coordinatorComposerFor('prj_1').files.value = []
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    state.pendingMessages = {}
  })

  test('renders immediately while sending and reuses the same DOM node on confirmation', async () => {
    let resolvePost!: (value: { message: MessageView }) => void
    vi.spyOn(api, 'postMessage').mockImplementation(
      () =>
        new Promise((resolve) => {
          resolvePost = resolve
        }),
    )

    const wrapper = mount(ConversationPanel, { attachTo: document.body })
    const textarea = wrapper.get('textarea')
    await textarea.setValue('马上上屏的消息')
    await textarea.trigger('keydown', { key: 'Enter' })
    await nextTick()

    // Draft cleared and optimistic message rendered immediately.
    expect((textarea.element as HTMLTextAreaElement).value).toBe('')
    const pendingNodes = wrapper.findAll('[data-message-id]')
    expect(pendingNodes).toHaveLength(1)
    const bubbleEl = pendingNodes[0]!.element
    expect(pendingNodes[0]!.attributes('data-message-id')).toMatch(/^pending:/)
    expect(pendingNodes[0]!.attributes('data-pending')).toBe('')
    expect(pendingNodes[0]!.text()).toContain('马上上屏的消息')

    // Confirm from server: same DOM element stays mounted without flicker or duplication.
    resolvePost({ message: makeMessage('msg_server_1', '马上上屏的消息', { createdAt: 2000 }) })
    await flushPromises()
    await nextTick()

    const confirmedNodes = wrapper.findAll('[data-message-id]')
    expect(confirmedNodes).toHaveLength(1)
    expect(confirmedNodes[0]!.element).toBe(bubbleEl)
    expect(confirmedNodes[0]!.attributes('data-message-id')).toBe('msg_server_1')
    expect(confirmedNodes[0]!.attributes('data-pending')).toBeUndefined()

    wrapper.unmount()
  })

  test('deduplicates smoothly when a conversation refresh arrives before postMessage resolves', async () => {
    let resolvePost!: (value: { message: MessageView }) => void
    vi.spyOn(api, 'postMessage').mockImplementation(
      () =>
        new Promise((resolve) => {
          resolvePost = resolve
        }),
    )

    const wrapper = mount(ConversationPanel, { attachTo: document.body })
    const textarea = wrapper.get('textarea')
    await textarea.setValue('事件先到达的消息')
    await textarea.trigger('keydown', { key: 'Enter' })
    await nextTick()

    const bubbleEl = wrapper.get('[data-message-id]').element
    const confirmed = makeMessage('msg_sse_first', '事件先到达的消息', { createdAt: 2000 })

    // Simulate an event-driven conversation refresh arriving before HTTP response returns.
    state.coordinator = merge(state.coordinator, {
      conversation,
      messages: [confirmed],
      notices: [],
      todo: { conversationId: 'crd_1', items: [], updatedAt: 1 },
      model: null,
      more: false,
      lastSeq: 2,
    })
    await nextTick()

    const nodesDuringRace = wrapper.findAll('[data-message-id]')
    expect(nodesDuringRace).toHaveLength(1)
    expect(nodesDuringRace[0]!.element).toBe(bubbleEl)
    expect(nodesDuringRace[0]!.attributes('data-message-id')).toBe('msg_sse_first')
    expect(nodesDuringRace[0]!.attributes('data-pending')).toBeUndefined()

    // Now the HTTP response resolves: still 1 bubble, same DOM node.
    resolvePost({ message: confirmed })
    await flushPromises()
    await nextTick()

    const nodesAfter = wrapper.findAll('[data-message-id]')
    expect(nodesAfter).toHaveLength(1)
    expect(nodesAfter[0]!.element).toBe(bubbleEl)
    expect(nodesAfter[0]!.attributes('data-message-id')).toBe('msg_sse_first')

    wrapper.unmount()
  })

  test('rolls back the optimistic bubble and restores draft on send failure', async () => {
    let rejectPost!: (error: Error) => void
    vi.spyOn(api, 'postMessage').mockImplementation(
      () =>
        new Promise((_, reject) => {
          rejectPost = reject
        }),
    )

    const wrapper = mount(ConversationPanel, { attachTo: document.body })
    const textarea = wrapper.get('textarea')
    await textarea.setValue('会发送失败的消息')
    await textarea.trigger('keydown', { key: 'Enter' })
    await nextTick()

    expect(wrapper.findAll('[data-message-id]')).toHaveLength(1)
    expect((textarea.element as HTMLTextAreaElement).value).toBe('')

    rejectPost(new Error('network down'))
    await flushPromises()
    await nextTick()

    expect(wrapper.findAll('[data-message-id]')).toHaveLength(0)
    expect((textarea.element as HTMLTextAreaElement).value).toBe('会发送失败的消息')

    wrapper.unmount()
  })

  test('shows uploaded attachment previews immediately on an optimistic message and restores them on failure', async () => {
    let rejectPost!: (error: Error) => void
    vi.spyOn(api, 'postMessage').mockImplementation(
      () =>
        new Promise((_, reject) => {
          rejectPost = reject
        }),
    )
    coordinatorComposerFor('prj_1').files.value = [
      {
        uid: 'att_1',
        name: 'design.png',
        size: 1024,
        type: 'image/png',
        url: 'blob:design',
        fileId: 'file_design',
        status: 'done',
      },
    ]

    const wrapper = mount(ConversationPanel, { attachTo: document.body })
    const textarea = wrapper.get('textarea')
    await textarea.setValue('看看这张图')
    await textarea.trigger('keydown', { key: 'Enter' })
    await nextTick()

    expect(coordinatorComposerFor('prj_1').files.value).toHaveLength(0)
    const node = wrapper.get('[data-message-id]')
    expect(node.attributes('data-pending')).toBe('')
    expect(node.text()).toContain('看看这张图')
    expect(node.html()).toContain('/api/files/file_design')

    rejectPost(new Error('upload confirm failed'))
    await flushPromises()
    await nextTick()

    expect(wrapper.findAll('[data-message-id]')).toHaveLength(0)
    expect(coordinatorComposerFor('prj_1').files.value).toHaveLength(1)
    expect(coordinatorComposerFor('prj_1').files.value[0]?.fileId).toBe('file_design')

    wrapper.unmount()
  })
})
