// @vitest-environment happy-dom
import type { ProjectView, ThreadView } from '@ariadne/core'
import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { nextTick } from 'vue'
import { locale } from '../i18n'
import { state } from '../store'
import ThreadList from './ThreadList.vue'
import { openGroups } from './thread-list'

const wrappers: VueWrapper[] = []

function makeThread(over: Partial<ThreadView> & { id: string; title: string }): ThreadView {
  return {
    projectId: 'prj_1',
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
    branchSlug: null,
    usage: {
      input: 0,
      output: 0,
      cached: 0,
      cacheWrite: 0,
      reasoning: 0,
      cost: { USD: 0, CNY: 0 },
    },
    progress: null,
    doing: null,
    createdAt: 1000,
    updatedAt: 1000,
    activeAt: 1000,
    reviewOf: null,
    review: null,
    auto: false,
    autopilotHeld: null,
    ...over,
  }
}

function setThreads(threads: ThreadView[]) {
  state.detail = {
    project: { id: 'prj_1', name: 'Demo', coordinatorId: 'crd_1' } as ProjectView,
    threads,
    needsMe: [],
    hasTracker: false,
  }
}

beforeEach(() => {
  locale.value = 'zh-CN'
  openGroups.value = ['review', 'waiting', 'working', 'paused', 'idle']
})

afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  state.detail = null
})

describe('ThreadList stability under concurrent updates', () => {
  test('keeps row order and status-line structure stable while concurrent working threads update', async () => {
    setThreads([
      makeThread({ id: 'thr_1', title: '任务一', stage: 'working', createdAt: 100, activeAt: 100 }),
      makeThread({ id: 'thr_2', title: '任务二', stage: 'queued', createdAt: 200, activeAt: 120 }),
      makeThread({
        id: 'thr_3',
        title: '任务三',
        stage: 'working',
        createdAt: 300,
        activeAt: 110,
        doing: '分析日志',
      }),
    ])

    const wrapper = mount(ThreadList)
    wrappers.push(wrapper)

    const titles = () => wrapper.findAll('.row-title').map((node) => node.text().trim())
    const feet = () => wrapper.findAll('.row-foot').map((node) => node.text().trim())

    expect(titles()).toEqual(['任务三', '任务二', '任务一'])
    expect(feet()).toEqual(['分析日志', '排队中', '工作中'])

    // Simulate concurrent tool steps, todo changes, and queued -> working transitions
    // bumping activeAt in reverse order.
    setThreads([
      makeThread({
        id: 'thr_1',
        title: '任务一',
        stage: 'working',
        createdAt: 100,
        activeAt: 900,
        doing: '修改组件',
      }),
      makeThread({
        id: 'thr_2',
        title: '任务二',
        stage: 'working',
        createdAt: 200,
        activeAt: 800,
        doing: null,
      }),
      makeThread({
        id: 'thr_3',
        title: '任务三',
        stage: 'working',
        createdAt: 300,
        activeAt: 400,
        doing: null,
      }),
    ])
    await nextTick()

    expect(titles()).toEqual(['任务三', '任务二', '任务一'])
    expect(feet()).toEqual(['工作中', '工作中', '修改组件'])
  })

  test('moves a thread needing attention to waiting without shuffling remaining working threads', async () => {
    setThreads([
      makeThread({ id: 'thr_1', title: '任务一', stage: 'working', createdAt: 100, activeAt: 500 }),
      makeThread({ id: 'thr_2', title: '任务二', stage: 'working', createdAt: 200, activeAt: 300 }),
      makeThread({ id: 'thr_3', title: '任务三', stage: 'working', createdAt: 300, activeAt: 200 }),
    ])

    const wrapper = mount(ThreadList)
    wrappers.push(wrapper)

    setThreads([
      makeThread({ id: 'thr_1', title: '任务一', stage: 'working', createdAt: 100, activeAt: 900 }),
      makeThread({ id: 'thr_2', title: '任务二', stage: 'answer', createdAt: 200, activeAt: 850 }),
      makeThread({ id: 'thr_3', title: '任务三', stage: 'working', createdAt: 300, activeAt: 400 }),
    ])
    await nextTick()

    const waitingTitles = wrapper
      .findAll('#thread-group-waiting .row-title')
      .map((node) => node.text().trim())
    const workingTitles = wrapper
      .findAll('#thread-group-working .row-title')
      .map((node) => node.text().trim())

    expect(waitingTitles).toEqual(['任务二'])
    expect(workingTitles).toEqual(['任务三', '任务一'])
  })
})
