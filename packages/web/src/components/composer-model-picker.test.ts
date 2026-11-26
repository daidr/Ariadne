// @vitest-environment happy-dom
import type { ConversationView, ProjectView, ThreadView, UsageTotals } from '@ariadne/core'
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
    title: '模型与思考力度切换',
    nodeId: null,
    stage: 'working',
    lastReport: null,
    lastSummary: null,
    lastHeadline: null,
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

function makeConversation(): ConversationView {
  return {
    id: 'thr_1',
    projectId: 'prj_1',
    role: 'thread',
    pendingSeq: 0,
    turn: null,
    lastOutcome: null,
    lastError: null,
    usage: emptyUsage(),
    contextTokens: 0,
    compactions: 0,
    wait: null,
  }
}

const wrappers: VueWrapper[] = []

describe('ConversationPanel cascading model and effort switcher', () => {
  beforeEach(() => {
    locale.value = 'zh-CN'
    state.currentId = 'prj_1'
    state.models = [
      { id: 'm-gemini', label: 'Gemini 4', efforts: ['low', 'medium', 'high'] },
      { id: 'm-claude', label: 'Claude Opus', efforts: ['low', 'medium', 'high', 'max'] },
      { id: 'm-plain', label: 'GPT-4o', efforts: [] },
    ]
    state.threadDefault = 'm-gemini'
    state.threadDefaultEffort = 'high'
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
        conversation: makeConversation(),
        model: {
          id: 'm-gemini',
          contextTokens: 200_000,
          compactAt: 120_000,
          effort: 'high',
        },
      },
    }
    vi.spyOn(api, 'getSkills').mockResolvedValue({ skills: [] })
    vi.spyOn(api, 'getPlan').mockResolvedValue({
      plan: {
        projectId: 'prj_1',
        nodes: [],
        revisions: [],
        proposals: [],
      },
    })
    vi.spyOn(api, 'listArtifacts').mockResolvedValue({ artifacts: [] })
    vi.spyOn(api, 'listMemories').mockResolvedValue({ memories: [] })
  })

  afterEach(() => {
    wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
    document.body.innerHTML = ''
    state.coordinator = emptyConversation()
    state.threads = {}
    state.detail = null
    state.currentId = null
    state.threadDefault = null
    state.threadDefaultEffort = null
    state.models = []
    vi.restoreAllMocks()
  })

  test('renders current model + effort and sends model/effort updates from cascading dropdown', async () => {
    const setModelSpy = vi.spyOn(api, 'setThreadModel').mockResolvedValue({
      thread: makeThread({ model: 'm-claude', effort: 'max' }),
    })
    vi.spyOn(api, 'getProject').mockResolvedValue({
      ...state.detail!,
      sync: { cursor: '1', partial: false },
    })
    vi.spyOn(api, 'getConversation').mockResolvedValue({
      conversation: makeConversation(),
      messages: [],
      notices: [],
      more: false,
      lastSeq: 1,
      todo: { conversationId: 'thr_1', items: [], updatedAt: 0 },
      model: { id: 'm-claude', contextTokens: 200_000, compactAt: 120_000, effort: 'max' },
    })

    const wrapper = mount(ConversationPanel, {
      props: { threadId: 'thr_1' },
      global: { stubs: { teleport: true } },
    })
    wrappers.push(wrapper)
    await flushPromises()

    expect(wrapper.find('.conversation-model').text()).toBe('Gemini 4 · 高')

    const trigger = wrapper.find(`button[title="${t('thread.model')}"]`)
    expect(trigger.exists()).toBe(true)
    await trigger.trigger('click')
    await flushPromises()

    const radioGroup = wrapper.findComponent({ name: 'DouDropdownRadioGroup' })
    expect(radioGroup.exists()).toBe(true)
    expect(radioGroup.props('modelValue')).toBe('')

    // Switch to explicit model + effort.
    await radioGroup.vm.$emit('update:modelValue', JSON.stringify(['m-claude', 'max']))
    await flushPromises()
    expect(setModelSpy).toHaveBeenLastCalledWith('thr_1', 'm-claude', 'max')

    // Switch to explicit model with model default effort.
    await radioGroup.vm.$emit('update:modelValue', JSON.stringify(['m-claude', '']))
    await flushPromises()
    expect(setModelSpy).toHaveBeenLastCalledWith('thr_1', 'm-claude', null)

    // Switch to a model that does not support effort.
    await radioGroup.vm.$emit('update:modelValue', JSON.stringify(['m-plain', '']))
    await flushPromises()
    expect(setModelSpy).toHaveBeenLastCalledWith('thr_1', 'm-plain', null)

    // Switch back to default.
    await radioGroup.vm.$emit('update:modelValue', '')
    await flushPromises()
    expect(setModelSpy).toHaveBeenLastCalledWith('thr_1', null, null)
  })

  test('shows switcher when only one model is configured if it accepts efforts, and hides when it accepts none', async () => {
    state.models = [{ id: 'm-gemini', label: 'Gemini 4', efforts: ['low', 'medium', 'high'] }]
    const wrapper = mount(ConversationPanel, {
      props: { threadId: 'thr_1' },
      global: { stubs: { teleport: true } },
    })
    wrappers.push(wrapper)
    await flushPromises()

    expect(wrapper.find(`button[title="${t('thread.model')}"]`).exists()).toBe(true)

    state.models = [{ id: 'm-gemini', label: 'Gemini 4', efforts: [] }]
    await nextTick()
    expect(wrapper.find(`button[title="${t('thread.model')}"]`).exists()).toBe(false)
    expect(wrapper.find('.conversation-model').text()).toBe('Gemini 4 · 高')
  })

  test('displays coordinator model with resolved low effort in the composer footer', async () => {
    state.coordinator = {
      ...emptyConversation(),
      conversation: {
        ...makeConversation(),
        id: 'crd_1',
        role: 'coordinator',
      },
      model: {
        id: 'm-gemini',
        contextTokens: 200_000,
        compactAt: 120_000,
        effort: 'low',
      },
    }

    const wrapper = mount(ConversationPanel, {
      global: { stubs: { teleport: true } },
    })
    wrappers.push(wrapper)
    await flushPromises()

    expect(wrapper.find(`button[title="${t('thread.model')}"]`).exists()).toBe(false)
    expect(wrapper.find('.conversation-model').text()).toBe('Gemini 4 · 低')

    state.coordinator = {
      ...state.coordinator,
      model: {
        id: 'm-plain',
        contextTokens: 128_000,
        compactAt: 76_800,
      },
    }
    await nextTick()
    expect(wrapper.find('.conversation-model').text()).toBe('GPT-4o')
  })

  test('shows project thread model override as the default label in the thread model picker', async () => {
    state.detail!.project = {
      ...state.detail!.project,
      settings: {
        instructions: '',
        delivery: { mode: 'merge', instructions: '' },
        tools: [],
        workspace: {
          retention: 'auto',
          auto: { setup: true, check: true },
          setup: '',
          shell: 'auto',
          check: '',
          branchPrefix: '',
        },
        routines: [],
        review: { enabled: true },
        autopilot: { enabled: false, method: 'squash' },
        models: {
          thread: { model: 'm-claude', effort: 'max' },
          coordinator: null,
          research: null,
        },
      },
    }

    const wrapper = mount(ConversationPanel, {
      props: { threadId: 'thr_1' },
      global: { stubs: { teleport: true } },
    })
    wrappers.push(wrapper)
    await flushPromises()

    await wrapper.find(`button[title="${t('thread.model')}"]`).trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('默认（Claude Opus · Max）')
  })
})
