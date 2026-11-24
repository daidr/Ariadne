// @vitest-environment happy-dom
import type { ThreadView } from '@ariadne/core'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { api, type ProjectWorktrees, type StorageSnapshot } from '../api'
import { locale } from '../i18n'
import WorktreeSettings from './WorktreeSettings.vue'
import { splitWorktreePaths } from './worktree-path'

const wrappers: VueWrapper[] = []

function thread(partial: Partial<ThreadView> & { id: string; title: string }): ThreadView {
  return {
    projectId: 'proj_1',
    nodeId: null,
    stage: 'done',
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
    createdAt: 1,
    updatedAt: 1,
    activeAt: 1,
    reviewOf: null,
    review: null,
    auto: false,
    autopilotHeld: null,
    ...partial,
  }
}

beforeEach(() => {
  locale.value = 'zh-CN'
})

afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  vi.restoreAllMocks()
})

describe('splitWorktreePaths', () => {
  test('extracts common parent directory for Windows worktree paths', () => {
    const result = splitWorktreePaths([
      'E:\\github\\ariadne\\github\\worktrees\\daidr\\Ariadne\\thr_01a1',
      'E:\\github\\ariadne\\github\\worktrees\\daidr\\Ariadne\\thr_01a2',
    ])
    expect(result).toEqual({
      commonDir: 'E:\\github\\ariadne\\github\\worktrees\\daidr\\Ariadne',
      relative: ['thr_01a1', 'thr_01a2'],
    })
  })

  test('extracts common parent directory even for a single worktree', () => {
    const result = splitWorktreePaths([
      'E:\\github\\ariadne\\github\\worktrees\\daidr\\Ariadne\\thr_01a1',
    ])
    expect(result).toEqual({
      commonDir: 'E:\\github\\ariadne\\github\\worktrees\\daidr\\Ariadne',
      relative: ['thr_01a1'],
    })
  })

  test('keeps source subdirectory when a project spans multiple repos under one root', () => {
    const result = splitWorktreePaths([
      '/Users/me/.ariadne/worktrees/repo-a/thr_1',
      '/Users/me/.ariadne/worktrees/repo-b/thr_2',
    ])
    expect(result).toEqual({
      commonDir: '/Users/me/.ariadne/worktrees',
      relative: ['repo-a/thr_1', 'repo-b/thr_2'],
    })
  })

  test('handles UNC paths without collapsing to bare server name', () => {
    expect(
      splitWorktreePaths(['\\\\server\\share\\wt\\thr_1', '\\\\server\\share\\wt\\thr_2']),
    ).toEqual({
      commonDir: '\\\\server\\share\\wt',
      relative: ['thr_1', 'thr_2'],
    })
    expect(splitWorktreePaths(['\\\\server\\a\\thr_1', '\\\\server\\b\\thr_2'])).toEqual({
      commonDir: null,
      relative: ['\\\\server\\a\\thr_1', '\\\\server\\b\\thr_2'],
    })
  })

  test('falls back to full paths when only drive or root is shared', () => {
    expect(splitWorktreePaths(['C:\\a\\thr_1', 'E:\\b\\thr_2'])).toEqual({
      commonDir: null,
      relative: ['C:\\a\\thr_1', 'E:\\b\\thr_2'],
    })
    expect(splitWorktreePaths(['C:\\a\\thr_1', 'C:\\b\\thr_2'])).toEqual({
      commonDir: null,
      relative: ['C:\\a\\thr_1', 'C:\\b\\thr_2'],
    })
    expect(splitWorktreePaths(['/a/thr_1', '/b/thr_2'])).toEqual({
      commonDir: null,
      relative: ['/a/thr_1', '/b/thr_2'],
    })
    expect(splitWorktreePaths(['thr_1'])).toEqual({
      commonDir: null,
      relative: ['thr_1'],
    })
  })
})

describe('WorktreeSettings', () => {
  test('renders shared base dir, status tags, sizes, and full path tooltips', async () => {
    const base = 'E:\\github\\ariadne\\github\\worktrees\\daidr\\Ariadne'
    const projects: ProjectWorktrees[] = [
      {
        id: 'proj_1',
        name: 'Ariadne',
        shared: true,
        error: null,
        worktrees: [
          {
            threadId: 'thr_1',
            path: `${base}\\thr_01a1`,
            bytes: 385_700_000,
            thread: thread({
              id: 'thr_1',
              title: 'Protobuf 迁移（域 1）：设置、插件、更新与连接检查',
              stage: 'done',
            }),
          },
          {
            threadId: 'thr_2',
            path: `${base}\\thr_01a2`,
            bytes: 372_300_000,
            thread: thread({
              id: 'thr_2',
              title: '调研外部画板协作交互设计并规划融入 Ariadne',
              stage: 'working',
            }),
          },
          {
            threadId: 'thr_3',
            path: `${base}\\thr_01a3`,
            bytes: null,
            orphaned: true,
            thread: null,
          },
          {
            threadId: 'thr_4',
            path: `${base}\\thr_01a4`,
            bytes: 5_800_000,
            thread: thread({
              id: 'thr_4',
              title: 'AI 修改计划图节点免二次确认',
              stage: 'done',
              merged: {
                branch: 'ariadne/thr_4',
                base: 'main',
                method: 'squash',
                sha: 'abc1234',
                head: 'def5678',
                at: 2,
              },
            }),
          },
        ],
      },
    ]

    const storage: StorageSnapshot = {
      managed: 1_500_000_000,
      total: 1_300_000_000,
      logical: false,
      measuredAt: Date.now(),
      worktrees: {
        [`${base}\\thr_01a1`]: { bytes: 385_700_000, exclusive: 12_000_000 },
        [`${base}\\thr_01a2`]: { bytes: 372_300_000, exclusive: 8_000_000 },
        [`${base}\\thr_01a3`]: { bytes: null, exclusive: null },
        [`${base}\\thr_01a4`]: { bytes: 5_800_000, exclusive: 5_800_000 },
      },
    }

    vi.spyOn(api, 'listWorktrees').mockResolvedValue({ projects, windows: true })

    const wrapper = mount(WorktreeSettings, {
      props: { storage, measuring: false },
    })
    wrappers.push(wrapper)
    await flushPromises()

    expect(wrapper.find('.worktrees-total').text()).toContain('文件分配量')
    expect(wrapper.find('.worktrees-dir').text()).toBe(base)
    expect(wrapper.find('.worktrees-dir').attributes('title')).toBe(base)

    const rows = wrapper.findAll('.worktree')
    expect(rows).toHaveLength(4)

    expect(rows[0]!.classes()).toContain('worktree--has-size')
    expect(rows[0]!.find('.worktree-title').text()).toBe(
      'Protobuf 迁移（域 1）：设置、插件、更新与连接检查',
    )
    expect(rows[0]!.find('.worktree-status').text()).toBe('已完成')
    expect(rows[0]!.find('.worktree-path').text()).toBe('thr_01a1')
    expect(rows[0]!.find('.worktree-path').attributes('title')).toBe(`${base}\\thr_01a1`)
    expect(rows[0]!.find('.worktree-size').text()).toBeTruthy()

    expect(rows[1]!.find('.worktree-status').text()).toBe('工作中')
    expect(rows[1]!.find('.worktree-path').text()).toBe('thr_01a2')

    expect(rows[2]!.classes()).not.toContain('worktree--has-size')
    expect(rows[2]!.find('.worktree-title').text()).toBe('已不存在的讨论串')
    expect(rows[2]!.find('.worktree-status').text()).toBe('残留工作区')
    expect(rows[2]!.find('.worktree-size').exists()).toBe(false)

    expect(rows[3]!.find('.worktree-status').text()).toBe('已合并')
  })

  test('hides total size while loading, when empty, or when loading fails', async () => {
    const storage: StorageSnapshot = {
      managed: 500_000,
      total: 0,
      logical: false,
      measuredAt: Date.now(),
      worktrees: {},
    }

    // 1. Loading (projects is still null): must not flash cached total.
    let resolveList!: (value: { projects: ProjectWorktrees[]; windows: boolean }) => void
    vi.spyOn(api, 'listWorktrees').mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveList = resolve
        }),
    )
    const loadingWrapper = mount(WorktreeSettings, {
      props: { storage: { ...storage, total: 1_300_000_000 }, measuring: false },
    })
    wrappers.push(loadingWrapper)
    expect(loadingWrapper.find('.worktrees-total').exists()).toBe(false)

    // 2. Empty after loading resolves with no worktrees: must not render "0 B".
    resolveList({ projects: [], windows: true })
    await flushPromises()
    expect(loadingWrapper.find('.worktrees-total').exists()).toBe(false)

    // 3. Request failure: must not render stale cached total.
    vi.spyOn(api, 'listWorktrees').mockRejectedValueOnce(new Error('network failed'))
    const failedWrapper = mount(WorktreeSettings, {
      props: { storage: { ...storage, total: 1_300_000_000 }, measuring: false },
    })
    wrappers.push(failedWrapper)
    await flushPromises()
    expect(failedWrapper.find('.worktrees-total').exists()).toBe(false)

    // 4. Project-level error: must also stay hidden.
    vi.spyOn(api, 'listWorktrees').mockResolvedValueOnce({
      windows: true,
      projects: [
        {
          id: 'proj_err',
          name: 'Broken',
          shared: null,
          worktrees: [],
          error: { key: 'error.connection' },
        },
      ],
    })
    const projectErrorWrapper = mount(WorktreeSettings, {
      props: { storage: { ...storage, total: 1_300_000_000 }, measuring: false },
    })
    wrappers.push(projectErrorWrapper)
    await flushPromises()
    expect(projectErrorWrapper.find('.worktrees-total').exists()).toBe(false)
  })
})
