// @vitest-environment happy-dom
import type { AllNeedsMeResult, ProjectView, ThreadView } from '@ariadne/core'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { DouConfigProvider } from 'dou-ui'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { api } from '../api'
import { douLocale, locale, t } from '../i18n'
import { state } from '../store'
import AttentionBar from './AttentionBar.vue'
import WorkspaceHeader from './WorkspaceHeader.vue'

type ProjectResponse = Awaited<ReturnType<typeof api.getProject>>

function makeProjectSummary(id: string, name: string, needsMe = 0): ProjectView {
  return {
    id,
    name,
    unnamed: false,
    coordinatorId: `crd_${id}`,
    createdAt: 1,
    updatedAt: 1,
    needsMe,
    source: null,
    references: [],
    slots: null,
    mcp: [],
    settings: {
      instructions: '',
      delivery: { mode: 'merge', instructions: '' },
      tools: [],
      workspace: {
        retention: 'auto',
        auto: { setup: false, check: false },
        setup: '',
        shell: 'auto',
        check: '',
        branchPrefix: '',
      },
      routines: [],
      review: { enabled: false },
      autopilot: { enabled: false, method: 'merge' },
      models: { thread: null, coordinator: null, research: null },
    },
    detected: { setup: null, check: null },
    autopilotPaused: false,
    usage: {
      input: 0,
      output: 0,
      cached: 0,
      cacheWrite: 0,
      reasoning: 0,
      cost: { USD: 0, CNY: 0 },
    },
  }
}

function makeProjectDetail(id: string, name: string): ProjectResponse {
  return {
    project: makeProjectSummary(id, name, 0),
    threads: [],
    needsMe: [],
    hasTracker: false,
    sync: { cursor: '', partial: false },
  }
}

const wrappers: VueWrapper[] = []

function mountWithProvider(component: unknown, props: Record<string, unknown> = {}) {
  const Host = defineComponent({
    setup() {
      return () =>
        h(DouConfigProvider, { locale: douLocale.value }, () => h(component as never, props))
    },
  })
  const wrapper = mount(Host, { attachTo: document.body })
  wrappers.push(wrapper)
  return wrapper
}

describe('Cross-project AttentionBar', () => {
  const sampleAllNeedsMe: AllNeedsMeResult = {
    items: [
      {
        projectId: 'prj_gamma',
        projectName: 'Gamma',
        at: 3000,
        item: {
          kind: 'attention',
          attention: {
            id: 'att_gamma',
            projectId: 'prj_gamma',
            threadId: 'thr_gamma',
            joinedBy: [],
            block: {
              type: 'confirm',
              title: '批准 Gamma 上线？',
              description: '将推送到生产分支',
              detail: null,
              tone: 'warning',
              approveText: '批准',
              denyText: '拒绝',
            },
            messageId: null,
            conversationId: 'thr_gamma',
            createdAt: 3000,
          },
          threadId: 'thr_gamma',
          joinedIds: [],
        },
      },
      {
        projectId: 'prj_beta',
        projectName: 'Beta',
        at: 2000,
        item: { kind: 'thread', threadId: 'thr_beta_halted' },
      },
      {
        projectId: 'prj_beta',
        projectName: 'Beta',
        at: 1800,
        item: { kind: 'thread', threadId: 'thr_beta_aside' },
      },
      {
        projectId: 'prj_alpha',
        projectName: 'Alpha',
        at: 1000,
        item: { kind: 'thread', threadId: 'thr_alpha_review' },
      },
    ],
    threads: {
      thr_alpha_review: {
        id: 'thr_alpha_review',
        projectId: 'prj_alpha',
        title: 'Alpha 代码评审',
        stage: 'review',
        lastHeadline: '已完成自测',
        lastArtifacts: [{ id: 'art_alpha', kind: 'document', title: 'Alpha 设计方案' }],
        aside: null,
        review: { reviewerId: 'rev_a', rounds: 1, reviewing: false },
        stopped: null,
        autopilotHeld: null,
      },
      thr_beta_halted: {
        id: 'thr_beta_halted',
        projectId: 'prj_beta',
        title: 'Beta 构建中断',
        stage: 'halted',
        lastHeadline: null,
        lastArtifacts: [],
        aside: null,
        review: null,
        stopped: null,
        autopilotHeld: null,
      },
      thr_beta_aside: {
        id: 'thr_beta_aside',
        projectId: 'prj_beta',
        title: 'Beta 搁置到期',
        stage: 'idle',
        lastHeadline: null,
        lastArtifacts: [],
        aside: { until: 1800, reason: '等待上游合并', by: 'user', due: true },
        review: null,
        stopped: null,
        autopilotHeld: null,
      },
      thr_gamma: {
        id: 'thr_gamma',
        projectId: 'prj_gamma',
        title: 'Gamma 发布审批',
        stage: 'answer',
        lastHeadline: null,
        lastArtifacts: [],
        aside: null,
        review: null,
        stopped: null,
        autopilotHeld: null,
      },
    },
  }

  beforeEach(() => {
    locale.value = 'zh-CN'
    state.projects = [
      makeProjectSummary('prj_alpha', 'Alpha', 1),
      makeProjectSummary('prj_beta', 'Beta', 2),
      makeProjectSummary('prj_gamma', 'Gamma', 1),
    ]
    state.currentId = 'prj_alpha'
    state.detail = makeProjectDetail('prj_alpha', 'Alpha')
    state.starting = false
    state.workspaceLevel = 'coordinator'
    state.main = { kind: 'conversation' }
    state.allNeedsMe = null
    state.allNeedsMeLoading = false
    state.allNeedsMeError = false
    state.attentionOpen = false

    vi.spyOn(api, 'listAllNeedsMe').mockResolvedValue(sampleAllNeedsMe)
    vi.spyOn(api, 'getProject').mockImplementation(async (id) =>
      makeProjectDetail(id, id === 'prj_gamma' ? 'Gamma' : id === 'prj_beta' ? 'Beta' : 'Alpha'),
    )
    vi.spyOn(api, 'getConversation').mockResolvedValue({
      conversation: null,
      messages: [],
      notices: [],
      todo: null,
      model: null,
      more: false,
      lastSeq: 0,
    } as unknown as Awaited<ReturnType<typeof api.getConversation>>)
    vi.spyOn(api, 'getPlan').mockResolvedValue({
      plan: { nodes: [], edges: [], revisions: [] },
    } as unknown as Awaited<ReturnType<typeof api.getPlan>>)
    vi.spyOn(api, 'listArtifacts').mockResolvedValue({ artifacts: [] } as unknown as Awaited<
      ReturnType<typeof api.listArtifacts>
    >)
    vi.spyOn(api, 'listMemories').mockResolvedValue({ memories: [] } as unknown as Awaited<
      ReturnType<typeof api.listMemories>
    >)
  })

  afterEach(() => {
    while (wrappers.length) wrappers.pop()?.unmount()
    document.body.innerHTML = ''
    state.attentionOpen = false
    state.allNeedsMe = null
    vi.restoreAllMocks()
  })

  test('stays visible in WorkspaceHeader with total count even on start screen and mobile projects view', () => {
    state.currentId = null
    state.detail = null
    state.workspaceLevel = 'projects'

    const wrapper = mountWithProvider(WorkspaceHeader, {
      layoutMode: 'narrow',
      projectsCollapsed: false,
    })

    const bell = wrapper.find('button.attention-trigger')
    expect(bell.exists()).toBe(true)
    expect(bell.text()).toContain('4')
  })

  test('pins current project first, orders other projects by latest trigger time, and handles inline actions + cross-project jump', async () => {
    const answerSpy = vi.spyOn(api, 'answerAttention').mockResolvedValue(undefined)
    const retrySpy = vi.spyOn(api, 'retryTurn').mockResolvedValue(undefined)
    const asideSpy = vi.spyOn(api, 'endAside').mockResolvedValue({ thread: {} as ThreadView })

    const wrapper = mountWithProvider(AttentionBar)
    const trigger = wrapper.find<HTMLButtonElement>('button.attention-trigger')
    expect(trigger.text()).toContain('4')

    await trigger.trigger('click')
    await flushPromises()
    await nextTick()

    expect(api.listAllNeedsMe).toHaveBeenCalledTimes(1)
    const sections = [...document.querySelectorAll<HTMLElement>('[data-attention-project-id]')]
    // Current project (Alpha, at=1000) pinned first; then Gamma (at=3000), then Beta (at=2000).
    expect(sections.map((el) => el.dataset.attentionProjectId)).toEqual([
      'prj_alpha',
      'prj_gamma',
      'prj_beta',
    ])
    expect(sections[0]!.textContent).toContain('当前项目')

    // Inline continue on Beta halted thread does not switch current project.
    const betaButtons = [...sections[2]!.querySelectorAll<HTMLButtonElement>('button')]
    const continueBtn = betaButtons.find((btn) => btn.textContent?.includes('继续'))
    expect(continueBtn).toBeDefined()
    continueBtn!.click()
    await flushPromises()
    expect(retrySpy).toHaveBeenCalledWith('thr_beta_halted')
    expect(state.currentId).toBe('prj_alpha')

    // Inline endAside on Beta aside-due thread does not switch current project.
    const asideBtn = betaButtons.find((btn) => btn.textContent?.includes('知道了'))
    expect(asideBtn).toBeDefined()
    asideBtn!.click()
    await flushPromises()
    expect(asideSpy).toHaveBeenCalledWith('thr_beta_aside')
    expect(state.currentId).toBe('prj_alpha')

    // Inline confirm approve on Gamma attention card does not switch current project.
    const gammaButtons = [...sections[1]!.querySelectorAll<HTMLButtonElement>('button')]
    const approveBtn = gammaButtons.find((btn) => btn.textContent?.includes('批准'))
    expect(approveBtn).toBeDefined()
    approveBtn!.click()
    await flushPromises()
    expect(answerSpy).toHaveBeenCalledWith('att_gamma', { type: 'confirm', approved: true })
    expect(state.currentId).toBe('prj_alpha')

    // Clicking a cross-project thread link switches to Gamma and closes the popover.
    const gammaLink = [...sections[1]!.querySelectorAll<HTMLElement>('a, button')].find((el) =>
      el.textContent?.includes('Gamma 发布审批'),
    )
    expect(gammaLink).toBeDefined()
    gammaLink!.click()
    await flushPromises()
    expect(state.currentId).toBe('prj_gamma')
    expect(state.attentionOpen).toBe(false)
  })

  test('shows loading and error states on reopen after previously loading an empty queue', async () => {
    let rejectPending!: (reason?: unknown) => void
    const listSpy = vi
      .spyOn(api, 'listAllNeedsMe')
      .mockResolvedValueOnce({ items: [], threads: {} })
      .mockImplementationOnce(
        () =>
          new Promise<AllNeedsMeResult>((_resolve, reject) => {
            rejectPending = reject
          }),
      )

    state.projects = [makeProjectSummary('prj_alpha', 'Alpha', 0)]
    const wrapper = mountWithProvider(AttentionBar)
    const trigger = wrapper.find<HTMLButtonElement>('button.attention-trigger')

    // First open when queue is empty caches { items: [], threads: {} }.
    await trigger.trigger('click')
    await flushPromises()
    await nextTick()
    expect(state.allNeedsMe).toEqual({ items: [], threads: {} })
    expect(document.body.textContent).toContain(t('attention.empty'))

    // Close popover, then another project gains a waiting item.
    state.attentionOpen = false
    await nextTick()
    state.projects = [
      makeProjectSummary('prj_alpha', 'Alpha', 0),
      makeProjectSummary('prj_beta', 'Beta', 1),
    ]

    // Reopen: should show loading spinner rather than the empty state while pending.
    await trigger.trigger('click')
    await nextTick()
    expect(listSpy).toHaveBeenCalledTimes(2)
    expect(document.body.textContent).not.toContain(t('attention.empty'))
    expect(document.querySelector('.attention-status')).not.toBeNull()

    // On failure: should show error + retry button rather than the empty state.
    rejectPending(new Error('network down'))
    await flushPromises()
    await nextTick()
    expect(document.body.textContent).toContain(t('attention.error'))
    expect(document.body.textContent).toContain(t('conversation.retry'))
    expect(document.body.textContent).not.toContain(t('attention.empty'))
  })

  test('exits trace and narrow project list when jumping to current project header or report artifact', async () => {
    vi.spyOn(api, 'getArtifact').mockResolvedValue({
      artifact: {
        id: 'art_alpha',
        projectId: 'prj_alpha',
        kind: 'document',
        title: 'Alpha 设计方案',
        content: '# 设计',
        version: 1,
        updatedAt: 1,
        createdAt: 1,
      },
      comments: [],
      feedback: [],
    } as unknown as Awaited<ReturnType<typeof api.getArtifact>>)

    state.main = { kind: 'trace', conversationId: 'crd_prj_alpha' }
    state.workspaceLevel = 'projects'

    const wrapper = mountWithProvider(AttentionBar)
    const trigger = wrapper.find<HTMLButtonElement>('button.attention-trigger')
    await trigger.trigger('click')
    await flushPromises()
    await nextTick()

    // Clicking current project header should return to coordinator conversation.
    const alphaSection = document.querySelector<HTMLElement>(
      '[data-attention-project-id="prj_alpha"]',
    )!
    const alphaHeaderLink = alphaSection.querySelector<HTMLElement>('.attention-group-name')!
    alphaHeaderLink.click()
    await flushPromises()
    expect(state.currentId).toBe('prj_alpha')
    expect(state.main.kind).toBe('conversation')
    expect(state.workspaceLevel).toBe('coordinator')
    expect(state.attentionOpen).toBe(false)

    // Reopen while in trace view and click current project's report artifact: must exit trace.
    state.main = { kind: 'trace', conversationId: 'crd_prj_alpha' }
    await trigger.trigger('click')
    await flushPromises()
    await nextTick()

    const reopenedAlpha = document.querySelector<HTMLElement>(
      '[data-attention-project-id="prj_alpha"]',
    )!
    const artifactLink = [...reopenedAlpha.querySelectorAll<HTMLElement>('a, button')].find((el) =>
      el.textContent?.includes('Alpha 设计方案'),
    )
    expect(artifactLink).toBeDefined()
    artifactLink!.click()
    await flushPromises()
    expect(state.main.kind).toBe('conversation')
    expect(state.attentionOpen).toBe(false)
  })
})
