// @vitest-environment happy-dom
import type { ConversationView, ProjectView, ThreadView, UsageTotals } from '@ariadne/core'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { nextTick } from 'vue'
import { api } from '../api'
import { locale, t } from '../i18n'
import { emptyConversation, merge, state } from '../store'
import ContextUsage from './ContextUsage.vue'
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

function makeConversation(id: string, overrides: Partial<ConversationView> = {}): ConversationView {
  return {
    id,
    projectId: 'prj_1',
    role: 'thread',
    turn: {
      turnId: 'trn_1',
      model: 'model-1',
      fallback: false,
      startedAt: Date.now() - 5_000,
      activity: '读取文件',
    },
    pendingSeq: 0,
    lastOutcome: null,
    lastError: null,
    wait: null,
    contextTokens: 0,
    compactions: 0,
    usage: emptyUsage(),
    ...overrides,
  } as unknown as ConversationView
}

const wrappers: VueWrapper[] = []

describe('ContextUsage and pre-message thread context display', () => {
  beforeEach(() => {
    locale.value = 'zh-CN'
    state.showCost = false
    state.currentId = 'prj_1'
    state.models = [{ id: 'model-1', label: 'Gemini 4' }]
    state.threadDefault = 'model-1'
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
      threads: [
        {
          id: 'thr_1',
          projectId: 'prj_1',
          title: '排查上下文使用量',
          stage: 'working',
          auto: false,
          reviewOf: null,
          review: null,
          workspace: null,
          pullRequest: null,
          merged: null,
          stopped: null,
          aside: null,
          doing: null,
          model: null,
          references: [],
          endedReferences: [],
        } as unknown as ThreadView,
      ],
      needsMe: [],
      hasTracker: false,
    }
    state.threads = {}
    vi.spyOn(api, 'getSkills').mockResolvedValue({ skills: [] })
  })

  afterEach(() => {
    wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
    document.body.innerHTML = ''
    state.threads = {}
    state.detail = null
    state.currentId = null
    vi.restoreAllMocks()
  })

  test('popover hides zero cumulative rows while showing mid-turn context window, then shows totals when available', async () => {
    const wrapper = mount(ContextUsage, {
      props: {
        used: 42_000,
        total: 102_400,
        compactions: 0,
        usage: emptyUsage(),
      },
      attachTo: document.body,
    })
    wrappers.push(wrapper)

    const trigger = wrapper.get('button')
    expect(trigger.attributes('aria-label')).toContain('42k')
    expect(trigger.attributes('aria-label')).toContain('102.4k')

    await trigger.trigger('click')
    await nextTick()

    const popoverText = document.body.textContent ?? ''
    expect(popoverText).toContain('42k / 102.4k')
    expect(popoverText).toContain(t('context.compactions'))
    expect(popoverText).not.toContain(t('usage.total'))
    expect(popoverText).not.toContain(t('usage.hitRate'))

    await wrapper.setProps({
      used: 55_000,
      usage: {
        input: 170_000,
        output: 47,
        cached: 167_000,
        cacheWrite: 0,
        reasoning: 7,
        cost: { USD: 0, CNY: 0 },
      },
    })
    await nextTick()

    const updatedText = document.body.textContent ?? ''
    expect(updatedText).toContain('55k / 102.4k')
    expect(updatedText).toContain(t('usage.total'))
    expect(updatedText).toContain('170k')
    expect(updatedText).toContain(t('usage.hitRate'))
  })

  test('shows context usage in thread composer before the model sends its first visible message', async () => {
    const initial = emptyConversation()
    state.threads.thr_1 = merge(initial, {
      conversation: makeConversation('thr_1', { contextTokens: 0 }),
      messages: [],
      notices: [],
      todo: { conversationId: 'thr_1', items: [], updatedAt: 0 },
      model: { id: 'model-1', contextTokens: 128_000, compactAt: 102_400 },
      more: false,
      lastSeq: 10,
    })

    const wrapper = mount(ConversationPanel, {
      props: { threadId: 'thr_1' },
      attachTo: document.body,
    })
    wrappers.push(wrapper)
    await flushPromises()
    await nextTick()

    expect(wrapper.findComponent(ContextUsage).exists()).toBe(false)

    // Simulated turn.step refresh while the thread is still executing tool calls and has zero messages.
    state.threads.thr_1 = merge(state.threads.thr_1, {
      conversation: makeConversation('thr_1', { contextTokens: 42_000 }),
      messages: [],
      notices: [],
      todo: { conversationId: 'thr_1', items: [], updatedAt: 0 },
      model: { id: 'model-1', contextTokens: 128_000, compactAt: 102_400 },
      more: false,
      lastSeq: 11,
    })
    await nextTick()

    const meter = wrapper.findComponent(ContextUsage)
    expect(meter.exists()).toBe(true)
    expect(meter.props('used')).toBe(42_000)
    expect(meter.props('total')).toBe(102_400)
    expect(state.threads.thr_1.messages).toHaveLength(0)
  })
})
