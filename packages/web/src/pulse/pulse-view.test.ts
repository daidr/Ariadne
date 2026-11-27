// @vitest-environment happy-dom
import type {
  AllNeedsMeResult,
  AttentionView,
  ConversationView,
  PlanView,
  ProjectView,
  PulseSnapshot,
  ThreadView,
} from '@ariadne/core'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { DouConfigProvider } from 'dou-ui'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { api } from '../api'
import { douLocale } from '../i18n'
import { openMain, state } from '../store'
import type { WorkspaceLayoutMode } from '../workspace-layout'
import PulseView from './PulseView.vue'

const MINUTE = 60_000
const NOW = 1_795_700_000_000

const wrappers: VueWrapper[] = []

function makeSnapshot(overrides: Partial<PulseSnapshot> = {}): PulseSnapshot {
  return {
    lanes: [
      {
        conversationId: 'crd_1',
        projectId: 'prj_1',
        projectName: 'Ariadne',
        title: '协调器',
        role: 'coordinator',
        reviewOf: null,
        stage: 'idle',
        completed: false,
        attentionId: null,
        liveCommand: null,
        liveTail: null,
        lastHeadline: '已分配切片 B 与审阅任务',
        activeAt: NOW - 5 * MINUTE,
      },
      {
        conversationId: 'thr_needs',
        projectId: 'prj_1',
        projectName: 'Ariadne',
        title: '接入推送渠道权限审批',
        role: 'worker',
        reviewOf: null,
        stage: 'answer',
        completed: false,
        attentionId: 'att_1',
        liveCommand: null,
        liveTail: null,
        lastHeadline: '需要确认写入生产配置',
        activeAt: NOW - 4 * MINUTE,
      },
      {
        conversationId: 'thr_author',
        projectId: 'prj_1',
        projectName: 'Ariadne',
        title: 'Pulse 桌面端实时泳道画布',
        role: 'worker',
        reviewOf: null,
        stage: 'review',
        completed: false,
        attentionId: null,
        liveCommand: null,
        liveTail: null,
        lastHeadline: '已提交 PR #270 等待审阅',
        activeAt: NOW - 2 * MINUTE,
      },
      {
        conversationId: 'thr_reviewer',
        projectId: 'prj_1',
        projectName: 'Ariadne',
        title: '审阅 · Pulse 实时泳道画布',
        role: 'reviewer',
        reviewOf: 'thr_author',
        stage: 'working',
        completed: false,
        attentionId: null,
        liveCommand: 'bun run test packages/web/src/pulse',
        liveTail: null,
        lastHeadline: '正在核对 SVG 交接连线与深色变量',
        activeAt: NOW - 30_000,
      },
      {
        conversationId: 'thr_done',
        projectId: 'prj_1',
        projectName: 'Ariadne',
        title: 'Pulse 切片 A 后端投影',
        role: 'worker',
        reviewOf: null,
        stage: 'done',
        completed: true,
        attentionId: null,
        liveCommand: null,
        liveTail: null,
        lastHeadline: '已合并 #268',
        activeAt: NOW - 35 * MINUTE,
      },
    ],
    spans: [
      {
        id: 1,
        projectId: 'prj_1',
        conversationId: 'crd_1',
        stage: 'working',
        occupiesSlot: false,
        startedAt: NOW - 48 * MINUTE,
        endedAt: NOW - 44 * MINUTE,
        seqStart: 10,
        seqEnd: 12,
        label: '拆解任务',
      },
      {
        id: 2,
        projectId: 'prj_1',
        conversationId: 'thr_needs',
        stage: 'working',
        occupiesSlot: true,
        startedAt: NOW - 30 * MINUTE,
        endedAt: NOW - 12 * MINUTE,
        seqStart: 15,
        seqEnd: 18,
        label: '实现渠道校验',
      },
      {
        id: 3,
        projectId: 'prj_1',
        conversationId: 'thr_needs',
        stage: 'answer',
        occupiesSlot: false,
        startedAt: NOW - 12 * MINUTE,
        endedAt: null,
        seqStart: 18,
        seqEnd: null,
        label: '等待确认',
      },
      {
        id: 4,
        projectId: 'prj_1',
        conversationId: 'thr_author',
        stage: 'working',
        occupiesSlot: true,
        startedAt: NOW - 36 * MINUTE,
        endedAt: NOW - 10 * MINUTE,
        seqStart: 20,
        seqEnd: 25,
        label: '绘制时间线画布',
      },
      {
        id: 5,
        projectId: 'prj_1',
        conversationId: 'thr_author',
        stage: 'review',
        occupiesSlot: false,
        startedAt: NOW - 10 * MINUTE,
        endedAt: null,
        seqStart: 25,
        seqEnd: null,
        label: '等待审阅',
      },
      {
        id: 6,
        projectId: 'prj_1',
        conversationId: 'thr_reviewer',
        stage: 'working',
        occupiesSlot: true,
        startedAt: NOW - 10 * MINUTE,
        endedAt: null,
        seqStart: 26,
        seqEnd: null,
        label: '审阅中',
      },
      {
        id: 7,
        projectId: 'prj_1',
        conversationId: 'thr_done',
        stage: 'working',
        occupiesSlot: true,
        startedAt: NOW - 52 * MINUTE,
        endedAt: NOW - 36 * MINUTE,
        seqStart: 5,
        seqEnd: 9,
        label: 'GetPulse 接口',
      },
    ],
    markers: [
      {
        seq: 25,
        projectId: 'prj_1',
        conversationId: 'thr_author',
        relatedThreadId: 'thr_reviewer',
        kind: 'review_handoff',
        at: NOW - 10 * MINUTE,
        label: '提交审阅',
      },
      {
        seq: 9,
        projectId: 'prj_1',
        conversationId: 'thr_done',
        relatedThreadId: null,
        kind: 'pr_merged',
        at: NOW - 35 * MINUTE,
        label: 'PR #268',
      },
    ],
    handoffs: [
      {
        seq: 25,
        projectId: 'prj_1',
        fromConversationId: 'thr_author',
        toConversationId: 'thr_reviewer',
        kind: 'review_handoff',
        at: NOW - 10 * MINUTE,
        label: '请求审阅',
      },
    ],
    concurrency: {
      slots: 3,
      current: 1,
      peak: 2,
      points: [
        { at: NOW - 52 * MINUTE, active: 1 },
        { at: NOW - 36 * MINUTE, active: 2 },
        { at: NOW - 12 * MINUTE, active: 1 },
      ],
    },
    lastReturnedAt: NOW - 24 * MINUTE,
    cursorSeq: 26,
    fromMs: NOW - 75 * MINUTE,
    toMs: NOW + 5 * MINUTE,
    serverNowMs: NOW,
    ...overrides,
  }
}

function mountPulse(layoutMode: WorkspaceLayoutMode = 'wide') {
  const Host = defineComponent({
    setup() {
      return () =>
        h(DouConfigProvider, { locale: douLocale.value }, () => h(PulseView, { layoutMode }))
    },
  })
  const wrapper = mount(Host, { attachTo: document.body })
  wrappers.push(wrapper)
  return wrapper
}

describe('PulseView', () => {
  beforeEach(() => {
    localStorage.clear()
    const project = {
      id: 'prj_1',
      name: 'Ariadne',
      coordinatorId: 'crd_1',
      needsMe: 1,
      runningTurns: 1,
      slots: 3,
    } as ProjectView

    state.projects = [project]
    state.currentId = 'prj_1'
    state.starting = false
    state.stream = 'open'
    state.live = {}
    state.allNeedsMe = null
    state.detail = {
      project,
      threads: [
        { id: 'thr_needs', title: '接入推送渠道权限审批', stage: 'answer' } as ThreadView,
        { id: 'thr_author', title: 'Pulse 桌面端实时泳道画布', stage: 'review' } as ThreadView,
        { id: 'thr_reviewer', title: '审阅 · Pulse 实时泳道画布', stage: 'working' } as ThreadView,
        { id: 'thr_done', title: 'Pulse 切片 A 后端投影', stage: 'done' } as ThreadView,
      ],
      needsMe: [],
      hasTracker: false,
    }
    state.plan = {
      projectId: 'prj_1',
      nodes: [
        {
          id: 'node_b',
          kind: 'work',
          title: 'Pulse 切片 B',
          description: '',
          acceptance: '',
          dependsOn: [],
          status: 'running',
          threads: [
            {
              id: 'thr_author',
              title: 'Pulse 桌面端实时泳道画布',
              stage: 'review',
              stoppedBy: null,
              lastHeadline: null,
              pullRequest: null,
            },
          ],
          changedIn: null,
          issues: [],
          gate: null,
        },
      ],
    } as unknown as PlanView
    openMain({ kind: 'pulse' })

    vi.spyOn(api, 'listAllNeedsMe').mockResolvedValue({ items: [], threads: {} })
    vi.spyOn(api, 'getConversation').mockImplementation(async (id) => ({
      conversation: { id } as ConversationView,
      messages: [],
      notices: [],
      todo: { conversationId: id, items: [], updatedAt: 0 },
      model: null,
      more: false,
      lastSeq: 1,
    }))
  })

  afterEach(() => {
    while (wrappers.length) wrappers.pop()?.unmount()
    document.body.innerHTML = ''
    localStorage.clear()
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  test('renders ribbon, axis, return line, grouped lanes, handoff arcs and collapsible completed section', async () => {
    const getPulse = vi.spyOn(api, 'getPulse').mockResolvedValue(makeSnapshot())
    const wrapper = mountPulse()
    await flushPromises()
    await nextTick()

    expect(getPulse).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-pulse-ribbon]').exists()).toBe(true)
    expect(wrapper.get('[data-pulse-slot-badge]').text()).toBe('1 / 3')
    expect(wrapper.find('[data-pulse-return-badge]').exists()).toBe(true)
    expect(wrapper.find('[data-pulse-return-band]').exists()).toBe(true)
    expect(wrapper.find('[data-pulse-now-line]').exists()).toBe(true)
    expect(wrapper.findAll('[data-pulse-handoff-arc]')).toHaveLength(1)

    // Active lanes visible, reviewer nested under author, completed folded by default.
    expect(wrapper.find('[data-lane-id="crd_1"]').exists()).toBe(true)
    expect(wrapper.find('[data-lane-id="thr_needs"]').exists()).toBe(true)
    expect(wrapper.find('[data-lane-id="thr_author"]').exists()).toBe(true)
    expect(wrapper.find('[data-lane-id="thr_reviewer"]').classes()).toContain('is-nested')
    expect(wrapper.find('[data-lane-completed="true"]').exists()).toBe(false)

    // Expand completed section -> completed lane renders.
    await wrapper.get('[data-pulse-completed-toggle]').trigger('click')
    await nextTick()
    expect(wrapper.find('[data-lane-id="thr_done"]').exists()).toBe(true)
  })

  test('supports status filtering, density toggle, layer toggles and pan/snap-to-now follow state', async () => {
    vi.spyOn(api, 'getPulse').mockResolvedValue(makeSnapshot())
    const wrapper = mountPulse()
    await flushPromises()
    await nextTick()

    // Filter to needs-only.
    await wrapper.get('[data-pulse-filter="needs"]').trigger('click')
    await nextTick()
    expect(wrapper.find('[data-lane-id="thr_needs"]').exists()).toBe(true)
    expect(wrapper.find('[data-lane-id="thr_author"]').exists()).toBe(false)

    // Filter back to all.
    await wrapper.get('[data-pulse-filter="all"]').trigger('click')
    await nextTick()
    expect(wrapper.find('[data-lane-id="thr_author"]').exists()).toBe(true)

    // Toggle density from comfortable (44px) to compact (34px).
    expect(wrapper.get('.pulse-canvas').attributes('style')).toContain('--pulse-lane-h: 44px')
    await wrapper.get('[data-pulse-density]').trigger('click')
    await nextTick()
    expect(wrapper.get('.pulse-canvas').attributes('style')).toContain('--pulse-lane-h: 34px')

    // Sideways wheel pans the timeline and switches follow button from live to back-to-now.
    const followBtn = wrapper.get('[data-pulse-follow]')
    expect(followBtn.text()).toContain('实时跟随')
    const scrollEl = wrapper.get('.pulse-lanes-scroll')
    const axisTrack = wrapper.get('.pulse-axis-track').element as HTMLElement
    vi.spyOn(axisTrack, 'getBoundingClientRect').mockReturnValue({
      left: 258,
      top: 0,
      right: 1058,
      bottom: 30,
      width: 800,
      height: 30,
      x: 258,
      y: 0,
      toJSON: () => ({}),
    })
    scrollEl.element.dispatchEvent(
      new WheelEvent('wheel', { deltaX: -120, deltaY: 0, cancelable: true }),
    )
    await nextTick()
    expect(followBtn.text()).toContain('回到此刻')

    // Clicking follow snaps back to live following.
    await followBtn.trigger('click')
    await nextTick()
    expect(followBtn.text()).toContain('实时跟随')

    // Layer toggles hide concurrency ribbon, handoff arcs and return line.
    await wrapper.get('[data-pulse-layers]').trigger('click')
    await nextTick()
    const layerSwitches = [
      ...document.querySelectorAll<HTMLButtonElement>('.pulse-layers-list [role="switch"]'),
    ]
    expect(layerSwitches).toHaveLength(3)
    for (const sw of layerSwitches) sw.click()
    await nextTick()
    expect(wrapper.find('[data-pulse-ribbon]').exists()).toBe(false)
    expect(wrapper.find('[data-pulse-handoff-svg]').exists()).toBe(false)
    expect(wrapper.find('[data-pulse-return-band]').exists()).toBe(false)
  })

  test('groups active lanes by plan node when switching groupBy to node', async () => {
    vi.spyOn(api, 'getPulse').mockResolvedValue(makeSnapshot())
    const wrapper = mountPulse()
    await flushPromises()
    await nextTick()

    const nodeOption = wrapper
      .findAll('.dou-segmented-control label, .dou-segmented-control button')
      .find((el) => el.text().includes('按计划节点'))
    expect(nodeOption).toBeDefined()
    const radio = nodeOption!.find('input[type="radio"]')
    if (radio.exists()) await radio.setValue(true)
    else await nodeOption!.trigger('click')
    await nextTick()

    expect(wrapper.find('[data-pulse-group="node:node_b"]').exists()).toBe(true)
    expect(wrapper.find('[data-pulse-group="node:unlinked"]').exists()).toBe(true)
  })

  test('opens a worker thread and exits pulse when clicking the lane title', async () => {
    vi.spyOn(api, 'getPulse').mockResolvedValue(makeSnapshot())
    const wrapper = mountPulse()
    await flushPromises()
    await nextTick()

    const authorTitle = wrapper.get('[data-lane-id="thr_author"] .pulse-lane-title')
    await authorTitle.trigger('click')
    await flushPromises()

    expect(state.main.kind).toBe('conversation')
    expect(state.workspaceLevel).toBe('side')
  })

  test('shows filtered empty state with reset action when no lanes match', async () => {
    const snap = makeSnapshot({
      lanes: [
        {
          conversationId: 'crd_1',
          projectId: 'prj_1',
          projectName: 'Ariadne',
          title: '协调器',
          role: 'coordinator',
          reviewOf: null,
          stage: 'idle',
          completed: false,
          attentionId: null,
          liveCommand: null,
          liveTail: null,
          lastHeadline: null,
          activeAt: NOW - MINUTE,
        },
        {
          conversationId: 'thr_author',
          projectId: 'prj_1',
          projectName: 'Ariadne',
          title: '正常执行中',
          role: 'worker',
          reviewOf: null,
          stage: 'working',
          completed: false,
          attentionId: null,
          liveCommand: null,
          liveTail: null,
          lastHeadline: null,
          activeAt: NOW - MINUTE,
        },
      ],
      handoffs: [],
    })
    vi.spyOn(api, 'getPulse').mockResolvedValue(snap)
    const wrapper = mountPulse()
    await flushPromises()
    await nextTick()

    await wrapper.get('[data-pulse-filter="needs"]').trigger('click')
    await nextTick()
    expect(wrapper.find('[data-pulse-inline-empty]').exists()).toBe(true)

    await wrapper.get('[data-pulse-inline-empty] button').trigger('click')
    await nextTick()
    expect(wrapper.find('[data-pulse-inline-empty]').exists()).toBe(false)
    expect(wrapper.find('[data-lane-id="thr_author"]').exists()).toBe(true)
  })

  test('shrinks handoff SVG height using flow offsetHeight rather than stale scrollHeight', async () => {
    vi.spyOn(api, 'getPulse').mockResolvedValue(makeSnapshot())
    const wrapper = mountPulse()
    await flushPromises()
    await nextTick()

    const inner = wrapper.get('.pulse-lanes-inner').element as HTMLElement
    let flowHeight = 620
    const staleScrollHeight = 620
    Object.defineProperty(inner, 'clientWidth', { configurable: true, get: () => 1058 })
    Object.defineProperty(inner, 'offsetHeight', { configurable: true, get: () => flowHeight })
    Object.defineProperty(inner, 'scrollHeight', {
      configurable: true,
      get: () => staleScrollHeight,
    })

    const authorEl = wrapper.get('[data-lane-id="thr_author"]').element as HTMLElement
    const reviewerEl = wrapper.get('[data-lane-id="thr_reviewer"]').element as HTMLElement
    Object.defineProperty(authorEl, 'offsetTop', { configurable: true, get: () => 88 })
    Object.defineProperty(authorEl, 'offsetHeight', { configurable: true, get: () => 44 })
    Object.defineProperty(reviewerEl, 'offsetTop', { configurable: true, get: () => 132 })
    Object.defineProperty(reviewerEl, 'offsetHeight', { configurable: true, get: () => 44 })

    // Expand completed section at taller flow height -> SVG grows to 620px.
    await wrapper.get('[data-pulse-completed-toggle]').trigger('click')
    await flushPromises()
    await nextTick()
    expect(wrapper.get('[data-pulse-handoff-svg]').attributes('style')).toContain('height: 620px')

    // Collapse completed section -> flow offsetHeight shrinks to 260px while scrollHeight stays inflated at 620px.
    flowHeight = 260
    await wrapper.get('[data-pulse-completed-toggle]').trigger('click')
    await flushPromises()
    await nextTick()
    const svg = wrapper.get('[data-pulse-handoff-svg]')
    expect(svg.attributes('style')).toContain('height: 260px')
    expect(svg.attributes('viewBox')).toBe('0 0 800 260')
  })

  test('opens right inspector on span click and supports in-place approval, retry, trace and title jump', async () => {
    const confirmAttention = {
      id: 'att_1',
      conversationId: 'thr_needs',
      block: {
        type: 'confirm',
        title: '允许推送到远端分支',
        detail: 'git push origin feature/pulse-c',
        approveText: '允许推送',
        denyText: '拒绝',
      },
    } as unknown as AttentionView

    state.detail!.needsMe = [
      {
        kind: 'attention',
        attention: confirmAttention,
        threadId: 'thr_needs',
        joinedIds: [],
      },
    ]
    state.detail!.threads.push({
      id: 'thr_blocked',
      title: '修复 Windows 打包脚本',
      stage: 'halted',
      pullRequest: {
        number: 274,
        title: 'fix: windows build',
        url: 'https://github.com/daidr/Ariadne/pull/274',
        state: 'open',
        merged: false,
        draft: false,
        checks: { state: 'failing', passed: 2, failed: ['native-windows'], pending: 0 },
      },
    } as unknown as ThreadView)

    const base = makeSnapshot()
    const snap = makeSnapshot({
      lanes: [
        ...base.lanes,
        {
          conversationId: 'thr_blocked',
          projectId: 'prj_1',
          projectName: 'Ariadne',
          title: '修复 Windows 打包脚本',
          role: 'worker',
          reviewOf: null,
          stage: 'halted',
          completed: false,
          attentionId: null,
          liveCommand: null,
          liveTail: null,
          lastHeadline: 'native-windows 检查失败',
          activeAt: NOW - 90_000,
        },
      ],
      spans: [
        ...base.spans,
        {
          id: 99,
          conversationId: 'thr_blocked',
          projectId: 'prj_1',
          stage: 'halted',
          occupiesSlot: false,
          startedAt: NOW - 4 * MINUTE,
          endedAt: null,
          label: 'CI 失败',
          seqStart: 90,
          seqEnd: null,
        },
      ],
    })

    const getPulseSpy = vi.spyOn(api, 'getPulse').mockResolvedValue(snap)
    const answerSpy = vi.spyOn(api, 'answerAttention').mockResolvedValue({} as never)
    const retrySpy = vi.spyOn(api, 'retryTurn').mockResolvedValue({} as never)

    const wrapper = mountPulse('wide')
    await flushPromises()
    await nextTick()

    // Click attention span -> opens docked right inspector with inline AskBlock.
    await wrapper.get('[data-span-id="3"]').trigger('click')
    await nextTick()
    const inspector = wrapper.get('[data-pulse-inspector]')
    expect(inspector.text()).toContain('接入推送渠道权限审批')
    expect(inspector.find('[data-pulse-inline-ask]').exists()).toBe(true)

    const approveBtn = inspector.findAll('button').find((btn) => btn.text().includes('允许推送'))
    expect(approveBtn).toBeDefined()
    await approveBtn!.trigger('click')
    await flushPromises()
    expect(answerSpy).toHaveBeenCalledWith('att_1', { type: 'confirm', approved: true })
    expect(getPulseSpy.mock.calls.length).toBeGreaterThanOrEqual(2)

    // Click blocked lane span -> shows failed check & triggers retryTurn.
    await wrapper.get('[data-span-id="99"]').trigger('click')
    await nextTick()
    expect(wrapper.get('[data-pulse-retry-card]').text()).toContain('native-windows')
    await wrapper.get('[data-pulse-retry]').trigger('click')
    await flushPromises()
    expect(retrySpy).toHaveBeenCalledWith('thr_blocked')

    // Click Trace button -> navigates to trace view for that conversation.
    await wrapper.get('[data-pulse-open-trace]').trigger('click')
    await nextTick()
    expect(state.main).toEqual({ kind: 'trace', conversationId: 'thr_blocked' })

    // Reopen pulse, click inspector header title -> exits pulse to thread.
    openMain({ kind: 'pulse' })
    await wrapper.get('[data-span-id="4"]').trigger('click')
    await nextTick()
    await wrapper.get('[data-pulse-inspector-title]').trigger('click')
    await flushPromises()
    expect(state.main.kind).toBe('conversation')
    expect(state.workspaceLevel).toBe('side')
  })

  test('switches to all active projects scope and supports cross-project inline approval', async () => {
    const project2 = {
      id: 'prj_2',
      name: 'DouUI',
      coordinatorId: 'crd_2',
      needsMe: 1,
      runningTurns: 1,
      slots: 2,
    } as ProjectView
    state.projects = [...state.projects, project2]

    const crossAttention = {
      id: 'att_cross',
      conversationId: 'thr_cross',
      block: {
        type: 'confirm',
        title: '发布 DouUI 0.9.1',
        detail: 'npm publish --tag latest',
        approveText: '确认发布',
        denyText: '暂不',
      },
    } as unknown as AttentionView

    const allNeedsResult: AllNeedsMeResult = {
      items: [
        {
          projectId: 'prj_2',
          projectName: 'DouUI',
          at: NOW - MINUTE,
          item: {
            kind: 'attention',
            attention: crossAttention,
            threadId: 'thr_cross',
            joinedIds: [],
          },
        },
      ],
      threads: {
        thr_cross: {
          id: 'thr_cross',
          projectId: 'prj_2',
          title: '发布 DouUI 0.9.1',
          stage: 'answer',
          lastHeadline: '等待确认发布 npm 包',
          lastArtifacts: [],
          aside: null,
          review: null,
          stopped: null,
          autopilotHeld: null,
        },
      },
    }

    const base = makeSnapshot()
    const allSnap = makeSnapshot({
      lanes: [
        ...base.lanes,
        {
          conversationId: 'crd_2',
          projectId: 'prj_2',
          projectName: 'DouUI',
          title: '协调器',
          role: 'coordinator',
          reviewOf: null,
          stage: 'idle',
          completed: false,
          attentionId: null,
          liveCommand: null,
          liveTail: null,
          lastHeadline: null,
          activeAt: NOW - 4 * MINUTE,
        },
        {
          conversationId: 'thr_cross',
          projectId: 'prj_2',
          projectName: 'DouUI',
          title: '发布 DouUI 0.9.1',
          role: 'worker',
          reviewOf: null,
          stage: 'answer',
          completed: false,
          attentionId: 'att_cross',
          liveCommand: null,
          liveTail: null,
          lastHeadline: '等待确认发布 npm 包',
          activeAt: NOW - MINUTE,
        },
      ],
      spans: [
        ...base.spans,
        {
          id: 201,
          conversationId: 'thr_cross',
          projectId: 'prj_2',
          stage: 'answer',
          occupiesSlot: false,
          startedAt: NOW - 8 * MINUTE,
          endedAt: null,
          label: '等待确认发布',
          seqStart: 100,
          seqEnd: null,
        },
      ],
    })

    const getPulseSpy = vi.spyOn(api, 'getPulse').mockImplementation(async (params) => {
      return params?.projectId === '' ? allSnap : base
    })
    const listAllNeedsSpy = vi.spyOn(api, 'listAllNeedsMe').mockResolvedValue(allNeedsResult)
    const answerSpy = vi.spyOn(api, 'answerAttention').mockResolvedValue({} as never)

    const wrapper = mountPulse('wide')
    await flushPromises()
    await nextTick()

    const allOption = wrapper
      .findAll('[data-pulse-scope] label, [data-pulse-scope] button')
      .find((el) => el.text().includes('全部活跃项目'))
    expect(allOption).toBeDefined()
    const radio = allOption!.find('input[type="radio"]')
    if (radio.exists()) await radio.setValue(true)
    else await allOption!.trigger('click')
    await flushPromises()
    await nextTick()

    expect(getPulseSpy).toHaveBeenLastCalledWith(expect.objectContaining({ projectId: '' }))
    expect(listAllNeedsSpy).toHaveBeenCalled()
    expect(wrapper.find('[data-pulse-group="project:prj_1"]').exists()).toBe(true)
    expect(wrapper.find('[data-pulse-group="project:prj_2"]').exists()).toBe(true)

    // Open cross-project lane inspector and approve in place.
    await wrapper.get('[data-span-id="201"]').trigger('click')
    await nextTick()
    const inspector = wrapper.get('[data-pulse-inspector]')
    expect(inspector.text()).toContain('DouUI')
    const confirmBtn = inspector.findAll('button').find((btn) => btn.text().includes('确认发布'))
    expect(confirmBtn).toBeDefined()
    await confirmBtn!.trigger('click')
    await flushPromises()
    expect(answerSpy).toHaveBeenCalledWith('att_cross', { type: 'confirm', approved: true })
  })

  test('renders narrow pulse cards and key event feed with inline actions on narrow viewports', async () => {
    const confirmAttention = {
      id: 'att_1',
      conversationId: 'thr_needs',
      block: {
        type: 'confirm',
        title: '允许推送到远端分支',
        detail: 'git push origin feature/pulse-c',
        approveText: '允许',
        denyText: '拒绝',
      },
    } as unknown as AttentionView

    state.detail!.needsMe = [
      {
        kind: 'attention',
        attention: confirmAttention,
        threadId: 'thr_needs',
        joinedIds: [],
      },
    ]

    vi.spyOn(api, 'getPulse').mockResolvedValue(
      makeSnapshot({
        concurrency: {
          slots: 3,
          current: 2,
          peak: 2,
          points: [],
        },
      }),
    )
    const answerSpy = vi.spyOn(api, 'answerAttention').mockResolvedValue({} as never)

    const wrapper = mountPulse('narrow')
    await flushPromises()
    await nextTick()

    expect(wrapper.find('[data-pulse-narrow]').exists()).toBe(true)
    expect(wrapper.find('[data-pulse-canvas]').exists()).toBe(false)
    expect(wrapper.find('[data-pulse-narrow-cards]').exists()).toBe(true)
    expect(wrapper.get('[data-pulse-narrow-slots]').text()).toContain('2 / 3')
    expect(wrapper.find('[data-lane-id="thr_needs"] [data-pulse-inline-ask]').exists()).toBe(true)

    const allowBtn = wrapper
      .get('[data-lane-id="thr_needs"]')
      .findAll('button')
      .find((btn) => btn.text().includes('允许'))
    expect(allowBtn).toBeDefined()
    await allowBtn!.trigger('click')
    await flushPromises()
    expect(answerSpy).toHaveBeenCalledWith('att_1', { type: 'confirm', approved: true })

    // Switch to key events feed tab.
    const feedOption = wrapper
      .findAll('[data-pulse-narrow-tabs] label, [data-pulse-narrow-tabs] button')
      .find((el) => el.text().includes('关键事件'))
    expect(feedOption).toBeDefined()
    const radio = feedOption!.find('input[type="radio"]')
    if (radio.exists()) await radio.setValue(true)
    else await feedOption!.trigger('click')
    await nextTick()

    expect(wrapper.find('[data-pulse-narrow-feed]').exists()).toBe(true)
    expect(wrapper.find('[data-pulse-feed-return]').exists()).toBe(true)
    expect(wrapper.findAll('.pulse-feed-row').length).toBeGreaterThan(0)

    // Clicking a feed item opens the bottom sheet drawer, whose title jumps into the discussion thread.
    await wrapper.get('[data-feed-lane-id="thr_author"]').trigger('click')
    await nextTick()
    const titleBtn = document.body.querySelector<HTMLButtonElement>('[data-pulse-inspector-title]')
    expect(titleBtn).not.toBeNull()
    expect(titleBtn!.textContent).toContain('Pulse 桌面端实时泳道画布')
    titleBtn!.click()
    await flushPromises()
    expect(state.main.kind).toBe('conversation')
    expect(state.workspaceLevel).toBe('side')
  })

  test('supports inspector actions for worker timed wait, set-aside before due, cross-project stopped and single failed CI check', async () => {
    state.detail!.threads.push({
      id: 'thr_ci_one',
      title: '单条 CI 失败待重试',
      stage: 'review',
      pullRequest: {
        number: 276,
        title: 'fix: pulse',
        url: 'https://github.com/daidr/Ariadne/pull/276',
        state: 'open',
        merged: false,
        draft: false,
        checks: { state: 'failing', passed: 4, failed: ['proto'], pending: 0 },
      },
    } as unknown as ThreadView)

    const snap = makeSnapshot({
      lanes: [
        {
          conversationId: 'thr_wait',
          projectId: 'prj_1',
          projectName: 'Ariadne',
          title: '等待远端发布',
          role: 'worker',
          reviewOf: null,
          stage: 'idle',
          completed: false,
          attentionId: null,
          liveCommand: null,
          liveTail: null,
          lastHeadline: null,
          activeAt: NOW - MINUTE,
        },
        {
          conversationId: 'thr_aside',
          projectId: 'prj_1',
          projectName: 'Ariadne',
          title: '暂搁需求',
          role: 'worker',
          reviewOf: null,
          stage: 'idle',
          completed: false,
          attentionId: null,
          liveCommand: null,
          liveTail: null,
          lastHeadline: null,
          activeAt: NOW - 2 * MINUTE,
        },
        {
          conversationId: 'thr_stopped_cross',
          projectId: 'prj_2',
          projectName: 'DouUI',
          title: '已暂停的跨项目任务',
          role: 'worker',
          reviewOf: null,
          stage: 'stopped',
          completed: false,
          attentionId: null,
          liveCommand: null,
          liveTail: null,
          lastHeadline: null,
          activeAt: NOW - 3 * MINUTE,
        },
        {
          conversationId: 'thr_ci_one',
          projectId: 'prj_1',
          projectName: 'Ariadne',
          title: '单条 CI 失败待重试',
          role: 'worker',
          reviewOf: null,
          stage: 'review',
          completed: false,
          attentionId: null,
          liveCommand: null,
          liveTail: null,
          lastHeadline: null,
          activeAt: NOW - 4 * MINUTE,
        },
      ],
      spans: [
        {
          id: 301,
          conversationId: 'thr_wait',
          projectId: 'prj_1',
          stage: 'waiting',
          occupiesSlot: false,
          startedAt: NOW - 10 * MINUTE,
          endedAt: null,
          label: '等待构建镜像完成',
          seqStart: 1,
          seqEnd: null,
        },
        {
          id: 302,
          conversationId: 'thr_aside',
          projectId: 'prj_1',
          stage: 'aside',
          occupiesSlot: false,
          startedAt: NOW - 20 * MINUTE,
          endedAt: null,
          label: '等设计评审',
          seqStart: 2,
          seqEnd: null,
        },
        {
          id: 303,
          conversationId: 'thr_stopped_cross',
          projectId: 'prj_2',
          stage: 'halted',
          occupiesSlot: false,
          startedAt: NOW - 15 * MINUTE,
          endedAt: null,
          label: '手动暂停',
          seqStart: 3,
          seqEnd: null,
        },
        {
          id: 304,
          conversationId: 'thr_ci_one',
          projectId: 'prj_1',
          stage: 'halted',
          occupiesSlot: false,
          startedAt: NOW - 5 * MINUTE,
          endedAt: null,
          label: 'PR #276: proto',
          seqStart: 4,
          seqEnd: null,
        },
      ],
      markers: [],
      handoffs: [],
    })

    vi.spyOn(api, 'getPulse').mockResolvedValue(snap)
    const wakeSpy = vi.spyOn(api, 'wakeNow').mockResolvedValue({} as never)
    const asideSpy = vi.spyOn(api, 'endAside').mockResolvedValue({} as never)
    const resumeSpy = vi.spyOn(api, 'resumeThread').mockResolvedValue({} as never)
    const retrySpy = vi.spyOn(api, 'retryTurn').mockResolvedValue({} as never)

    const wrapper = mountPulse('wide')
    await flushPromises()
    await nextTick()

    // Worker timed wait detected from open waiting span with note.
    await wrapper.get('[data-span-id="301"]').trigger('click')
    await nextTick()
    expect(wrapper.get('[data-pulse-wait-card]').text()).toContain('等待构建镜像完成')
    await wrapper.get('[data-pulse-wake]').trigger('click')
    await flushPromises()
    expect(wakeSpy).toHaveBeenCalledWith('thr_wait')

    // Worker set-aside detected before due from open aside span.
    await wrapper.get('[data-span-id="302"]').trigger('click')
    await nextTick()
    expect(wrapper.get('[data-pulse-aside-card]').text()).toContain('等设计评审')
    await wrapper.get('[data-pulse-end-aside]').trigger('click')
    await flushPromises()
    expect(asideSpy).toHaveBeenCalledWith('thr_aside')

    // Cross-project stopped lane shows resume card even without local ThreadView/allNeedsMe entry.
    await wrapper.get('[data-span-id="303"]').trigger('click')
    await nextTick()
    expect(wrapper.get('[data-pulse-stopped-card]').text()).toContain('手动暂停')
    await wrapper.get('[data-pulse-resume]').trigger('click')
    await flushPromises()
    expect(resumeSpy).toHaveBeenCalledWith('thr_stopped_cross')

    // Single failed CI check on a PR thread in review stage shows CI retry card.
    await wrapper.get('[data-span-id="304"]').trigger('click')
    await nextTick()
    expect(wrapper.get('[data-pulse-retry-card]').text()).toContain('proto')
    await wrapper.get('[data-pulse-retry]').trigger('click')
    await flushPromises()
    expect(retrySpy).toHaveBeenCalledWith('thr_ci_one')
  })

  test('narrow view shows active coordinator under active filter without rendering duplicate empty state', async () => {
    const snap = makeSnapshot({
      lanes: [
        {
          conversationId: 'crd_1',
          projectId: 'prj_1',
          projectName: 'Ariadne',
          title: '协调器',
          role: 'coordinator',
          reviewOf: null,
          stage: 'working',
          completed: false,
          attentionId: null,
          liveCommand: null,
          liveTail: null,
          lastHeadline: '正在拆分计划节点',
          activeAt: NOW - MINUTE,
        },
      ],
      spans: [
        {
          id: 401,
          conversationId: 'crd_1',
          projectId: 'prj_1',
          stage: 'working',
          occupiesSlot: false,
          startedAt: NOW - 5 * MINUTE,
          endedAt: null,
          label: '正在规划',
          seqStart: 1,
          seqEnd: null,
        },
      ],
      markers: [],
      handoffs: [],
    })
    vi.spyOn(api, 'getPulse').mockResolvedValue(snap)

    const wrapper = mountPulse('narrow')
    await flushPromises()
    await nextTick()

    await wrapper.get('[data-pulse-filter="active"]').trigger('click')
    await nextTick()
    expect(wrapper.find('[data-lane-id="crd_1"]').exists()).toBe(true)
    expect(wrapper.find('[data-pulse-inline-empty]').exists()).toBe(false)

    await wrapper.get('[data-pulse-filter="needs"]').trigger('click')
    await nextTick()
    expect(wrapper.find('[data-lane-id="crd_1"]').exists()).toBe(false)
    expect(wrapper.find('[data-pulse-inline-empty]').exists()).toBe(true)
  })
})
