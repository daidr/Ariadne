// @vitest-environment happy-dom
import type {
  ArtifactSummary,
  ConversationView,
  ProjectView,
  ThreadView,
  UsageTotals,
} from '@ariadne/core'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { nextTick } from 'vue'
import { api } from '../api'
import { locale, t } from '../i18n'
import { emptyConversation, state } from '../store'
import ConversationPanel from './ConversationPanel.vue'

function emptyUsage(): UsageTotals {
  return {
    input: 0,
    output: 0,
    cached: 0,
    cacheWrite: 0,
    reasoning: 0,
    cost: { USD: 0, CNY: 0 },
  }
}

function makeThread(overrides: Partial<ThreadView> = {}): ThreadView {
  return {
    id: 'thr_1',
    projectId: 'prj_1',
    title: '调研讨论串',
    nodeId: null,
    stage: 'done',
    lastReport: 'done',
    lastSummary: '已完成调研',
    lastHeadline: '调研已完成',
    lastArtifacts: [],
    workspace: null,
    merged: null,
    archived: false,
    changes: null,
    pullRequest: null,
    aside: null,
    stopped: null,
    references: [],
    endedReferences: [],
    model: null,
    effort: null,
    branchSlug: null,
    usage: emptyUsage(),
    progress: null,
    doing: null,
    createdAt: 1000,
    updatedAt: 1000,
    activeAt: 1000,
    reviewOf: null,
    review: null,
    auto: false,
    autopilotHeld: null,
    ...overrides,
  }
}

function makeConversation(
  id = 'thr_1',
  role: 'coordinator' | 'thread' = 'thread',
): ConversationView {
  return {
    id,
    projectId: 'prj_1',
    role,
    pendingSeq: 0,
    turn: null,
    lastOutcome: 'completed',
    lastError: null,
    usage: emptyUsage(),
    contextTokens: 0,
    compactions: 0,
    wait: null,
  }
}

const wrappers: VueWrapper[] = []

describe('ConversationPanel resolved state and reopen action', () => {
  beforeEach(() => {
    locale.value = 'zh-CN'
    state.currentId = 'prj_1'
    state.models = []
    state.artifacts = []
    state.live = {}
    state.background = {}
    state.writing = {}
    state.detail = {
      project: {
        id: 'prj_1',
        name: 'Ariadne',
        coordinatorId: 'crd_1',
        source: null,
        fileSources: [],
        references: [],
        mcp: [],
      } as unknown as ProjectView,
      threads: [makeThread()],
      needsMe: [],
      hasTracker: false,
    }
    state.threads = {
      thr_1: {
        ...emptyConversation(),
        conversation: makeConversation('thr_1', 'thread'),
      },
    }
    state.coordinator = {
      ...emptyConversation(),
      conversation: makeConversation('crd_1', 'coordinator'),
    }
    vi.spyOn(api, 'getSkills').mockResolvedValue({ skills: [] })
  })

  afterEach(() => {
    wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
    document.body.innerHTML = ''
    state.coordinator = emptyConversation()
    state.threads = {}
    state.detail = null
    state.currentId = null
    state.artifacts = []
    state.models = []
    vi.restoreAllMocks()
  })

  test('hides chat input for completed threads, keeps capsules, and reopens to idle with focus', async () => {
    state.artifacts = [
      {
        id: 'art_1',
        projectId: 'prj_1',
        nodeId: null,
        kind: 'document',
        title: '调研结论文档',
        version: 1,
        createdBy: 'thr_1',
        updatedBy: 'thr_1',
        createdAt: 1000,
        updatedAt: 1000,
        openComments: 0,
        preview: { kind: 'document', excerpt: '', lead: '' },
        commentedAt: 0,
      } satisfies ArtifactSummary,
    ]
    const reopenedThread = makeThread({
      stage: 'idle',
      lastReport: 'idle',
      lastHeadline: null,
    })
    const reopenSpy = vi.spyOn(api, 'reopenThread').mockResolvedValue({ thread: reopenedThread })

    const wrapper = mount(ConversationPanel, {
      attachTo: document.body,
      props: { threadId: 'thr_1' },
      global: { stubs: { teleport: true } },
    })
    wrappers.push(wrapper)
    await flushPromises()

    const resolvedBar = wrapper.find('[data-thread-resolved]')
    expect(resolvedBar.exists()).toBe(true)
    expect(resolvedBar.text()).toContain(t('thread.resolved'))
    expect(resolvedBar.text()).toContain(t('thread.reopen'))
    expect(wrapper.find('.dou-chat-input').exists()).toBe(false)
    expect(wrapper.find('.conversation-capsules').exists()).toBe(true)
    expect(wrapper.find('.conversation-capsules').text()).toContain(
      t('artifacts.count', { count: 1 }),
    )

    const reopenButton = resolvedBar.find('button')
    expect(reopenButton.exists()).toBe(true)
    await reopenButton.trigger('click')
    await flushPromises()
    await nextTick()

    expect(reopenSpy).toHaveBeenCalledTimes(1)
    expect(reopenSpy).toHaveBeenCalledWith('thr_1')
    expect(state.detail?.threads[0]?.stage).toBe('idle')
    expect(wrapper.find('[data-thread-resolved]').exists()).toBe(false)
    expect(wrapper.find('.dou-chat-input').exists()).toBe(true)
    expect(document.activeElement).toBe(wrapper.find('textarea').element)
  })

  test('shows chat input directly for idle research threads and coordinator', async () => {
    state.detail!.threads = [makeThread({ stage: 'idle', lastReport: 'idle' })]

    const threadWrapper = mount(ConversationPanel, {
      props: { threadId: 'thr_1' },
      global: { stubs: { teleport: true } },
    })
    wrappers.push(threadWrapper)
    await flushPromises()

    expect(threadWrapper.find('[data-thread-resolved]').exists()).toBe(false)
    expect(threadWrapper.find('.dou-chat-input').exists()).toBe(true)

    const coordinatorWrapper = mount(ConversationPanel, {
      global: { stubs: { teleport: true } },
    })
    wrappers.push(coordinatorWrapper)
    await flushPromises()

    expect(coordinatorWrapper.find('[data-thread-resolved]').exists()).toBe(false)
    expect(coordinatorWrapper.find('.dou-chat-input').exists()).toBe(true)
  })

  test('ignores repeated reopen clicks while request is in flight', async () => {
    let resolveRequest!: (value: { thread: ThreadView }) => void
    const reopenSpy = vi.spyOn(api, 'reopenThread').mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveRequest = resolve
        }),
    )

    const wrapper = mount(ConversationPanel, {
      props: { threadId: 'thr_1' },
      global: { stubs: { teleport: true } },
    })
    wrappers.push(wrapper)
    await flushPromises()

    const button = wrapper.find('[data-thread-resolved] button')
    await button.trigger('click')
    await button.trigger('click')
    expect(reopenSpy).toHaveBeenCalledTimes(1)

    resolveRequest({
      thread: makeThread({ stage: 'idle', lastReport: 'idle', lastHeadline: null }),
    })
    await flushPromises()
    expect(wrapper.find('.dou-chat-input').exists()).toBe(true)
  })
})
