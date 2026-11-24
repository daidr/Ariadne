// @vitest-environment happy-dom
import type { RepoChanges } from '@ariadne/plugin-sdk'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { nextTick } from 'vue'
import { api } from '../api'
import { state, type TurnChangesRef } from '../store'
import ChangesView from './ChangesView.vue'
import { repoFileTree, scopedFileKey } from './files'

const SHA_A = 'a'.repeat(40)
const SHA_B = 'b'.repeat(40)
const SHA_C = 'c'.repeat(40)
const SHA_D = 'd'.repeat(40)

function seedThread(id: string) {
  state.detail = {
    project: {
      id: 'prj_1',
      name: 'Demo',
      createdAt: 1,
      settings: {
        delivery: { mode: 'local' },
        autopilot: { method: 'squash' },
      },
    },
    threads: [
      {
        id,
        projectId: 'prj_1',
        title: 'Multi-repo changes test',
        stage: 'review',
        activeAt: 1,
        workspace: {
          label: '/work/demo',
          branch: 'ariadne/demo',
          base: 'main',
          removed: false,
          parked: false,
        },
        pullRequest: null,
        merged: null,
        changes: { files: 2, added: 10, removed: 2 },
        review: null,
      },
    ],
  } as unknown as typeof state.detail
}

describe('ChangesView single-repo and multi-repo layout', () => {
  beforeEach(() => {
    seedThread('thr_changes')
    vi.spyOn(api, 'getLineComments').mockResolvedValue({ comments: [], hasMore: false })
    vi.spyOn(api, 'getReviews').mockResolvedValue({ rounds: [], hasMore: false })
    vi.spyOn(api, 'getChangedFile').mockResolvedValue({
      file: { before: 'old\n', after: 'new\n', skipped: null },
    })
    vi.spyOn(api, 'getChangedFiles').mockImplementation(async (_threadId, input) => ({
      files: input.files.map((f) => ({
        before: `before:${input.repoPath ?? ''}:${f.path}\n`,
        after: `after:${input.repoPath ?? ''}:${f.path}\n`,
        skipped: null,
      })),
    }))
  })

  afterEach(() => {
    vi.restoreAllMocks()
    state.detail = null
    document.body.innerHTML = ''
  })

  test('repoFileTree scopes duplicate file paths across repositories', () => {
    const tree = repoFileTree([
      {
        key: 'src_main\0',
        label: '主仓库',
        branch: 'ariadne/demo',
        embedded: false,
        files: [
          { path: 'README.md', previousPath: null, status: 'modified', added: 2, removed: 0 },
        ],
      },
      {
        key: 'src_main\0packages/sub',
        label: 'packages/sub',
        branch: 'feat/sub',
        embedded: true,
        commentPrefix: 'packages/sub',
        files: [{ path: 'README.md', previousPath: null, status: 'added', added: 4, removed: 0 }],
      },
    ])
    expect(tree).toHaveLength(2)
    expect(tree[0]!.children![0]!.key).toBe(scopedFileKey('src_main\0', 'README.md'))
    expect(tree[1]!.children![0]!.key).toBe(scopedFileKey('src_main\0packages/sub', 'README.md'))
    expect(tree[1]!.children![0]!.commentPath).toBe('packages/sub/README.md')
  })

  test('single primary repository keeps flat layout without repo section headers', async () => {
    vi.spyOn(api, 'getChanges').mockResolvedValue({
      changes: {
        branch: 'ariadne/demo',
        base: 'main',
        from: SHA_A,
        head: SHA_B,
        baseHead: SHA_A,
        commits: [{ sha: SHA_B, subject: 'update app' }],
        files: [
          { path: 'src/app.ts', previousPath: null, status: 'modified', added: 4, removed: 1 },
        ],
        uncommitted: [],
      },
      repos: [
        {
          sourceId: 'src_main',
          embedded: false,
          mergeable: true,
          changes: {
            branch: 'ariadne/demo',
            base: 'main',
            from: SHA_A,
            head: SHA_B,
            baseHead: SHA_A,
            commits: [{ sha: SHA_B, subject: 'update app' }],
            files: [
              { path: 'src/app.ts', previousPath: null, status: 'modified', added: 4, removed: 1 },
            ],
            uncommitted: [],
          },
        },
      ],
      mergeable: true,
    })

    const wrapper = mount(ChangesView, {
      props: { threadId: 'thr_changes', place: 'full' },
      attachTo: document.body,
    })
    await flushPromises()
    await nextTick()

    expect(wrapper.findAll('.repo-section-header')).toHaveLength(0)
    expect(wrapper.find('.changes-repo-tag').exists()).toBe(false)
    expect(wrapper.find('.changes-meta').text()).toContain('ariadne/demo → main')
    expect(wrapper.text()).toContain('合并到 main')
    wrapper.unmount()
  })

  test('single embedded repository keeps flat layout, annotates relative path and branch, and hides local merge', async () => {
    const embedded: RepoChanges = {
      sourceId: 'src_main',
      repoPath: 'packages/sub',
      embedded: true,
      mergeable: false,
      changes: {
        branch: 'feat/sub',
        base: 'main',
        from: SHA_C,
        head: SHA_D,
        commits: [{ sha: SHA_D, subject: 'sub fix' }],
        files: [{ path: 'index.ts', previousPath: null, status: 'modified', added: 3, removed: 0 }],
        uncommitted: [],
      },
    }
    vi.spyOn(api, 'getChanges').mockResolvedValue({
      changes: embedded.changes,
      repos: [embedded],
      mergeable: false,
    })

    const wrapper = mount(ChangesView, {
      props: { threadId: 'thr_changes', place: 'full' },
      attachTo: document.body,
    })
    await flushPromises()
    await nextTick()

    expect(wrapper.findAll('.repo-section-header')).toHaveLength(0)
    expect(wrapper.find('.changes-repo-tag').text()).toContain('内嵌仓库')
    const meta = wrapper.find('.changes-meta').text()
    expect(meta).toContain('packages/sub')
    expect(meta).toContain('feat/sub → main')
    expect(wrapper.text()).not.toContain('合并到')
    wrapper.unmount()
  })

  test('multiple repositories group by repository in tree and diff list and pass repoPath to file loader', async () => {
    const getFileSpy = vi.spyOn(api, 'getChangedFile')
    vi.spyOn(api, 'getChanges').mockResolvedValue({
      changes: {
        branch: 'ariadne/demo',
        base: 'main',
        from: SHA_A,
        head: SHA_B,
        baseHead: SHA_A,
        commits: [{ sha: SHA_B, subject: 'outer' }],
        files: [
          { path: 'README.md', previousPath: null, status: 'modified', added: 2, removed: 0 },
        ],
        uncommitted: [],
      },
      repos: [
        {
          sourceId: 'src_main',
          embedded: false,
          mergeable: true,
          changes: {
            branch: 'ariadne/demo',
            base: 'main',
            from: SHA_A,
            head: SHA_B,
            baseHead: SHA_A,
            commits: [{ sha: SHA_B, subject: 'outer' }],
            files: [
              { path: 'README.md', previousPath: null, status: 'modified', added: 2, removed: 0 },
            ],
            uncommitted: [],
          },
        },
        {
          sourceId: 'src_main',
          repoPath: 'packages/sub',
          embedded: true,
          mergeable: false,
          changes: {
            branch: 'feat/sub',
            base: 'main',
            from: SHA_C,
            head: SHA_D,
            commits: [{ sha: SHA_D, subject: 'sub' }],
            files: [
              { path: 'README.md', previousPath: null, status: 'added', added: 5, removed: 1 },
            ],
            uncommitted: [],
          },
        },
      ],
      mergeable: true,
    })

    const wrapper = mount(ChangesView, {
      props: { threadId: 'thr_changes', place: 'full' },
      attachTo: document.body,
    })
    await flushPromises()
    await nextTick()

    expect(wrapper.find('.changes-meta').text()).toContain('2 个仓库')
    const headers = wrapper.findAll('.repo-section-header')
    expect(headers).toHaveLength(2)
    expect(headers[0]!.text()).toContain('主仓库')
    expect(headers[1]!.text()).toContain('packages/sub')
    expect(headers[1]!.text()).toContain('内嵌仓库')
    expect(headers[1]!.text()).toContain('feat/sub → main')

    const repoPaths = getFileSpy.mock.calls.map((call) => call[1].repoPath ?? '')
    expect(repoPaths).toContain('')
    expect(repoPaths).toContain('packages/sub')
    wrapper.unmount()
  })

  test('multi-repo turn snapshot view groups turn.repos without calling getChanges', async () => {
    const getChangesSpy = vi.spyOn(api, 'getChanges')
    const turn: TurnChangesRef = {
      turnId: 'turn_1',
      from: '',
      to: '',
      files: [],
      total: { files: 2, added: 7, removed: 1 },
      repos: [
        {
          sourceId: 'src_main',
          branch: 'ariadne/demo',
          from: SHA_A,
          to: SHA_B,
          files: [
            { path: 'main.go', previousPath: null, status: 'modified', added: 3, removed: 1 },
          ],
          total: { files: 1, added: 3, removed: 1 },
        },
        {
          sourceId: 'src_main',
          repoPath: 'vendor/sdk',
          branch: 'dev',
          from: SHA_C,
          to: SHA_D,
          files: [{ path: 'client.go', previousPath: null, status: 'added', added: 4, removed: 0 }],
          total: { files: 1, added: 4, removed: 0 },
        },
      ],
    }

    const wrapper = mount(ChangesView, {
      props: { threadId: 'thr_changes', turn, place: 'full' },
      attachTo: document.body,
    })
    await flushPromises()
    await nextTick()

    expect(getChangesSpy).not.toHaveBeenCalled()
    expect(wrapper.find('.changes-meta').text()).toContain('2 个仓库')
    const headers = wrapper.findAll('.repo-section-header')
    expect(headers).toHaveLength(2)
    expect(headers[1]!.text()).toContain('vendor/sdk')
    expect(headers[1]!.text()).toContain('dev')
    wrapper.unmount()
  })
})
