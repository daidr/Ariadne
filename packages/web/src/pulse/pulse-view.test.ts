// @vitest-environment happy-dom
import type {
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

function mountPulse() {
  const Host = defineComponent({
    setup() {
      return () => h(DouConfigProvider, { locale: douLocale.value }, () => h(PulseView))
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
})
