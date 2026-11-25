// @vitest-environment happy-dom
import type { ProjectView, SearchResult } from '@ariadne/core'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { DouConfigProvider } from 'dou-ui'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { api } from '../api'
import { douLocale, locale, t } from '../i18n'
import { state } from '../store'
import ProjectSearch from './ProjectSearch.vue'

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

function mountSearch() {
  const Host = defineComponent({
    setup() {
      return () => h(DouConfigProvider, { locale: douLocale.value }, () => h(ProjectSearch))
    },
  })
  const wrapper = mount(Host, { attachTo: document.body })
  wrappers.push(wrapper)
  return wrapper
}

describe('Cross-project ⌘K ProjectSearch', () => {
  const sampleResult: SearchResult = {
    terms: ['部署'],
    threads: [
      {
        kind: 'thread',
        id: 'thr_alpha',
        title: 'Alpha 部署流水线',
        snippet: '当前项目命中',
        at: 1000,
        projectId: 'prj_alpha',
        projectName: 'Alpha',
        stage: 'review',
      },
      {
        kind: 'thread',
        id: 'thr_beta',
        title: 'Beta 部署回滚',
        snippet: '',
        at: 2000,
        projectId: 'prj_beta',
        projectName: 'Beta',
        stage: 'halted',
      },
    ],
    artifacts: [
      {
        kind: 'artifact',
        id: 'art_beta',
        artifactKind: 'document',
        title: 'Beta 部署手册',
        snippet: '回滚步骤说明',
        at: 2100,
        projectId: 'prj_beta',
        projectName: 'Beta',
      },
    ],
    messages: [
      {
        kind: 'message',
        id: 'msg_beta',
        conversationId: 'crd_beta',
        where: null,
        snippet: '确认 Beta 部署时间',
        at: 2200,
        projectId: 'prj_beta',
        projectName: 'Beta',
      },
    ],
  }

  beforeEach(() => {
    vi.useFakeTimers()
    locale.value = 'zh-CN'
    state.projects = [
      makeProjectSummary('prj_alpha', 'Alpha', 0),
      makeProjectSummary('prj_beta', 'Beta 部署平台', 2),
    ]
    state.currentId = 'prj_alpha'
    state.detail = makeProjectDetail('prj_alpha', 'Alpha')
    state.searchOpen = false
    state.starting = false
    state.main = { kind: 'conversation' }

    vi.spyOn(api, 'search').mockResolvedValue(sampleResult)
    vi.spyOn(api, 'getProject').mockImplementation(async (id) =>
      makeProjectDetail(id, id === 'prj_beta' ? 'Beta 部署平台' : 'Alpha'),
    )
    vi.spyOn(api, 'getConversation').mockResolvedValue({
      conversation: { id: 'crd_beta', projectId: 'prj_beta', role: 'coordinator' },
      messages: [{ id: 'msg_beta', conversationId: 'crd_beta', content: '确认 Beta 部署时间' }],
      notices: [],
      todo: null,
      model: null,
      more: false,
      lastSeq: 1,
    } as unknown as Awaited<ReturnType<typeof api.getConversation>>)
    vi.spyOn(api, 'getPlan').mockResolvedValue({
      plan: { nodes: [], edges: [], revisions: [] },
    } as unknown as Awaited<ReturnType<typeof api.getPlan>>)
    vi.spyOn(api, 'listArtifacts').mockResolvedValue({
      artifacts: [{ id: 'art_beta', kind: 'document', title: 'Beta 部署手册' }],
    } as unknown as Awaited<ReturnType<typeof api.listArtifacts>>)
    vi.spyOn(api, 'listMemories').mockResolvedValue({ memories: [] } as unknown as Awaited<
      ReturnType<typeof api.listMemories>
    >)
  })

  afterEach(() => {
    while (wrappers.length) wrappers.pop()?.unmount()
    document.body.innerHTML = ''
    state.searchOpen = false
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  test('searches globally with preferProjectId, matches projects, labels cross-project hits, and switches project on select', async () => {
    mountSearch()
    state.searchOpen = true
    await nextTick()

    const input = document.querySelector<HTMLInputElement>('input')
    expect(input).not.toBeNull()
    input!.value = '部署'
    input!.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    await vi.advanceTimersByTimeAsync(250)
    await flushPromises()
    await nextTick()

    expect(api.search).toHaveBeenCalledWith('', '部署', 'prj_alpha')

    const dialogText = document.body.textContent ?? ''
    // Matched project under "项目" group.
    expect(dialogText).toContain('Beta 部署平台')
    // Cross-project hits label their project name (including thread hit with empty snippet and message hit).
    expect(dialogText).toContain('Beta · 回滚步骤说明')
    expect(dialogText).toContain('Beta · 协调器')
    // Cross-project thread hit renders StageIcon using server-provided stage.
    expect(document.querySelector(`[aria-label="${t('thread.stage.halted')}"]`)).not.toBeNull()

    // Clicking cross-project thread hit switches to prj_beta and opens thr_beta.
    const options = [...document.querySelectorAll<HTMLElement>('[role="option"]')]
    const betaThreadOption = options.find((el) => el.textContent?.includes('Beta 部署回滚'))
    expect(betaThreadOption).toBeDefined()
    betaThreadOption!.click()
    await flushPromises()

    expect(state.currentId).toBe('prj_beta')
    expect(api.getProject).toHaveBeenCalledWith('prj_beta', undefined)
  })

  test('switches project directly when selecting a matched project item in ⌘K', async () => {
    mountSearch()
    state.searchOpen = true
    await nextTick()

    const input = document.querySelector<HTMLInputElement>('input')
    input!.value = 'Beta'
    input!.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()

    const options = [...document.querySelectorAll<HTMLElement>('[role="option"]')]
    const projectOption = options.find((el) => el.textContent?.includes('Beta 部署平台'))
    expect(projectOption).toBeDefined()
    projectOption!.click()
    await flushPromises()

    expect(state.currentId).toBe('prj_beta')
    expect(api.getProject).toHaveBeenCalledWith('prj_beta', undefined)
  })

  test('runs global search even when no project is currently open', async () => {
    state.currentId = null
    state.detail = null
    mountSearch()
    state.searchOpen = true
    await nextTick()

    const input = document.querySelector<HTMLInputElement>('input')
    input!.value = '部署'
    input!.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    await vi.advanceTimersByTimeAsync(250)
    await flushPromises()

    expect(api.search).toHaveBeenCalledWith('', '部署', undefined)
  })

  test('exits trace view when jumping to a current-project thread message', async () => {
    vi.spyOn(api, 'search').mockResolvedValue({
      terms: ['部署'],
      threads: [],
      artifacts: [],
      messages: [
        {
          kind: 'message',
          id: 'msg_alpha_thr',
          conversationId: 'thr_alpha',
          where: 'Alpha 部署流水线',
          snippet: '当前项目讨论串里的部署消息',
          at: 2500,
          projectId: 'prj_alpha',
          projectName: 'Alpha',
        },
      ],
    })
    vi.spyOn(api, 'getConversation').mockResolvedValue({
      conversation: { id: 'thr_alpha', projectId: 'prj_alpha', role: 'thread' },
      messages: [
        { id: 'msg_alpha_thr', conversationId: 'thr_alpha', content: '当前项目讨论串里的部署消息' },
      ],
      notices: [],
      todo: null,
      model: null,
      more: false,
      lastSeq: 1,
    } as unknown as Awaited<ReturnType<typeof api.getConversation>>)

    state.detail = {
      ...makeProjectDetail('prj_alpha', 'Alpha'),
      threads: [
        {
          id: 'thr_alpha',
          projectId: 'prj_alpha',
          title: 'Alpha 部署流水线',
          stage: 'review',
        } as unknown as ProjectResponse['threads'][number],
      ],
    }
    state.main = { kind: 'trace', conversationId: 'crd_prj_alpha' }
    mountSearch()
    state.searchOpen = true
    await nextTick()

    const input = document.querySelector<HTMLInputElement>('input')!
    input.value = '部署'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    await vi.advanceTimersByTimeAsync(250)
    await flushPromises()
    await nextTick()

    const options = [...document.querySelectorAll<HTMLElement>('[role="option"]')]
    const msgOption = options.find((el) => el.textContent?.includes('当前项目讨论串里的部署消息'))
    expect(msgOption).toBeDefined()
    msgOption!.click()
    await flushPromises()

    expect(state.currentId).toBe('prj_alpha')
    expect(state.main.kind).toBe('conversation')
    expect(state.focus?.conversationId).toBe('thr_alpha')
    expect(state.focus?.messageId).toBe('msg_alpha_thr')
  })

  test('ignores stale in-flight search responses after clearing query', async () => {
    let resolvePending!: (value: SearchResult) => void
    vi.spyOn(api, 'search').mockImplementationOnce(
      () =>
        new Promise<SearchResult>((resolve) => {
          resolvePending = resolve
        }),
    )

    mountSearch()
    state.searchOpen = true
    await nextTick()

    const input = document.querySelector<HTMLInputElement>('input')!
    input.value = '部署'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    await vi.advanceTimersByTimeAsync(250)
    expect(api.search).toHaveBeenCalledTimes(1)

    // User clears input while the request is still in-flight.
    input.value = ''
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()

    // Stale response arrives later and must be ignored.
    resolvePending(sampleResult)
    await flushPromises()
    await nextTick()

    expect(document.body.textContent).not.toContain('Beta 部署回滚')
    expect(document.body.textContent).not.toContain('Beta 部署手册')
  })
})
