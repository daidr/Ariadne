// @vitest-environment happy-dom
import type { ProjectView, ThreadView } from '@ariadne/core'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { nextTick } from 'vue'
import { api } from '../api'
import { locale } from '../i18n'
import { emptyConversation, state } from '../store'
import ConversationBar from './ConversationBar.vue'
import ConversationPanel from './ConversationPanel.vue'

const wrappers: VueWrapper[] = []

function makeThread(id: string, overrides: Partial<ThreadView> = {}): ThreadView {
  return {
    id,
    projectId: 'prj_1',
    title: '历史 PR 讨论串',
    stage: 'ready',
    auto: false,
    reviewOf: null,
    review: null,
    workspace: {
      label: '/repo',
      branch: 'ariadne/legacy',
      base: 'main',
      removed: false,
      parked: false,
    },
    changes: null,
    pullRequest: {
      number: 179,
      url: 'https://github.com/daidr/Ariadne/pull/179',
      title: 'Fix diff capsule',
      state: 'open',
      headSha: 'abc1234',
      checks: { state: 'success', failed: [] },
    },
    merged: null,
    stopped: null,
    aside: null,
    doing: null,
    model: null,
    references: [],
    endedReferences: [],
    ...overrides,
  } as unknown as ThreadView
}

describe('ConversationPanel capsule changes backfill', () => {
  beforeEach(() => {
    locale.value = 'zh-CN'
    state.currentId = 'prj_1'
    state.models = [{ id: 'model-1', label: 'Gemini' }]
    state.threadDefault = 'model-1'
    state.artifacts = []
    state.live = {}
    state.background = {}
    state.writing = {}
    state.threads = {
      thr_legacy: emptyConversation(),
      thr_empty: emptyConversation(),
    }
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
      threads: [makeThread('thr_legacy'), makeThread('thr_empty')],
      needsMe: [],
      hasTracker: false,
    }
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

  test('backfills missing totals for a legacy PR thread and displays +added -removed', async () => {
    let resolveChanges!: (value: Awaited<ReturnType<typeof api.getChanges>>) => void
    const getChangesSpy = vi.spyOn(api, 'getChanges').mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveChanges = resolve
        }),
    )

    const wrapper = mount(ConversationPanel, {
      props: { threadId: 'thr_legacy' },
      attachTo: document.body,
    })
    wrappers.push(wrapper)
    await nextTick()

    expect(getChangesSpy).toHaveBeenCalledTimes(1)
    expect(getChangesSpy).toHaveBeenCalledWith('thr_legacy', undefined, expect.any(AbortSignal))
    const initialSignal = getChangesSpy.mock.calls[0]![2]!

    // Unrelated thread state updates must not abort the in-flight backfill request.
    state.detail!.threads[0]!.doing = '同步中'
    await nextTick()
    expect(initialSignal.aborted).toBe(false)

    // Before backfill finishes, the PR fallback label remains visible and clickable.
    const bar = wrapper.findComponent(ConversationBar)
    expect(bar.exists()).toBe(true)
    expect(bar.get('button[aria-label="改动"]').text()).toContain('改动')

    resolveChanges({
      changes: {
        branch: 'ariadne/legacy',
        base: 'main',
        from: '0000000000000000000000000000000000000001',
        head: '0000000000000000000000000000000000000002',
        baseHead: '0000000000000000000000000000000000000001',
        commits: [],
        files: [
          { path: 'a.ts', previousPath: null, status: 'modified', added: 18, removed: 4 },
          { path: 'b.ts', previousPath: null, status: 'added', added: 24, removed: 0 },
        ],
        uncommitted: [],
      },
      mergeable: false,
    })
    await flushPromises()
    await nextTick()

    const diffButton = bar.get('button[aria-label="改动"]')
    expect(diffButton.text()).toContain('+42')
    expect(diffButton.text()).toContain('−4')
    expect(diffButton.text()).not.toContain('改动')
  })

  test('hides the diff capsule when backfilled branch has zero changed files', async () => {
    vi.spyOn(api, 'getChanges').mockResolvedValue({
      changes: {
        branch: 'ariadne/legacy',
        base: 'main',
        from: '0000000000000000000000000000000000000001',
        head: '0000000000000000000000000000000000000001',
        baseHead: '0000000000000000000000000000000000000001',
        commits: [],
        files: [],
        uncommitted: [],
      },
      mergeable: false,
    })

    const wrapper = mount(ConversationPanel, {
      props: { threadId: 'thr_empty' },
      attachTo: document.body,
    })
    wrappers.push(wrapper)
    await flushPromises()
    await nextTick()

    const bar = wrapper.findComponent(ConversationBar)
    expect(bar.exists()).toBe(true)
    expect(bar.find('button[aria-label="改动"]').exists()).toBe(false)
    expect(bar.text()).toContain('#179')
  })

  test('retries backfill after switching away aborts an in-flight request on a reused panel', async () => {
    const resolvers = new Map<string, (value: Awaited<ReturnType<typeof api.getChanges>>) => void>()
    const signals: Array<{ threadId: string; signal: AbortSignal }> = []
    const getChangesSpy = vi
      .spyOn(api, 'getChanges')
      .mockImplementation((threadId, _since, signal) => {
        signals.push({ threadId, signal: signal! })
        return new Promise((resolve) => {
          resolvers.set(`${threadId}:${signals.length}`, resolve)
        })
      })

    const wrapper = mount(ConversationPanel, {
      props: { threadId: 'thr_legacy' },
      attachTo: document.body,
    })
    wrappers.push(wrapper)
    await nextTick()

    expect(getChangesSpy).toHaveBeenCalledTimes(1)
    expect(signals[0]!.threadId).toBe('thr_legacy')
    expect(signals[0]!.signal.aborted).toBe(false)

    // Switch to another thread before the first request settles, aborting it on the reused instance.
    await wrapper.setProps({ threadId: 'thr_empty' })
    await nextTick()
    expect(signals[0]!.signal.aborted).toBe(true)
    expect(getChangesSpy).toHaveBeenCalledTimes(2)
    expect(signals[1]!.threadId).toBe('thr_empty')

    // Switch back to the original thread; the aborted key must be retried.
    await wrapper.setProps({ threadId: 'thr_legacy' })
    await nextTick()
    expect(signals[1]!.signal.aborted).toBe(true)
    expect(getChangesSpy).toHaveBeenCalledTimes(3)
    expect(signals[2]!.threadId).toBe('thr_legacy')
    expect(signals[2]!.signal.aborted).toBe(false)

    resolvers.get('thr_legacy:3')!({
      changes: {
        branch: 'ariadne/legacy',
        base: 'main',
        from: '0000000000000000000000000000000000000001',
        head: '0000000000000000000000000000000000000002',
        baseHead: '0000000000000000000000000000000000000001',
        commits: [],
        files: [{ path: 'a.ts', previousPath: null, status: 'modified', added: 18, removed: 4 }],
        uncommitted: [],
      },
      mergeable: false,
    })
    await flushPromises()
    await nextTick()

    const bar = wrapper.findComponent(ConversationBar)
    const diffButton = bar.get('button[aria-label="改动"]')
    expect(diffButton.text()).toContain('+18')
    expect(diffButton.text()).toContain('−4')
  })
})
