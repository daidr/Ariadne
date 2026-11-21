// @vitest-environment happy-dom
import type { PlanNodeView, ThreadPullRequest, ThreadView } from '@ariadne/core'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, expect, test, vi } from 'vitest'
import { h } from 'vue'
import { api, type ProjectDetail } from '../api'
import { t } from '../i18n'
import PlanNode from '../plan/PlanNode.vue'
import PlanNodeDetail from '../plan/PlanNodeDetail.vue'
import PullRequestTag from '../components/PullRequestTag.vue'
import { state } from '../store'
import { clearLinkCaches } from './link-summaries'
import MessageLink from './MessageLink.vue'

const url = 'https://github.com/daidr/dou-ui/pull/472'
const summary: NonNullable<PlanNodeView['threads'][number]['pullRequest']> = {
  number: 472,
  url,
  state: 'open',
  checks: { state: 'pending', failed: [] },
}
const node: PlanNodeView = {
  id: 'node-test',
  kind: 'task',
  title: 'Geometry icons',
  description: '',
  acceptance: '',
  status: 'done',
  dependsOn: [],
  changedIn: null,
  issues: [],
  gate: null,
  threads: [
    {
      id: 'thr_test',
      title: 'Add geometry icons',
      stage: 'done',
      lastHeadline: null,
      stoppedBy: null,
      pullRequest: summary,
    },
  ],
}

function track(pr: Partial<ThreadPullRequest>) {
  state.detail = {
    threads: [
      {
        ...node.threads[0],
        pullRequest: { ...summary, title: 'New icons', headSha: 'current-head', ...pr },
      } as ThreadView,
    ],
  } as ProjectDetail
}

const global = {
  stubs: {
    DouPopover: {
      props: ['open'],
      template: '<div><slot /><slot v-if="open" name="content" /></div>',
    },
  },
}

afterEach(() => {
  state.detail = null
  clearLinkCaches()
  vi.restoreAllMocks()
})

test.each(['details', 'graph'] as const)(
  'renders the shared PR link in plan %s',
  async (surface) => {
    track({ state: 'merged', checks: { state: 'success', failed: [] } })
    vi.spyOn(api, 'previewLink').mockResolvedValue({ preview: null })
    const wrapper =
      surface === 'details'
        ? mount(PlanNodeDetail, { props: { node, editable: true }, global })
        : mount(PlanNode, {
            props: {
              data: {
                ...node,
                ahead: [],
                compact: false,
                fresh: false,
                mark: null,
                proposed: false,
              },
            },
            global,
          })
    try {
      const link = wrapper.get(`a[href="${url}"]`)
      expect(link.text()).toBe('#472')
      expect(link.attributes('aria-description')).toBe(t('link.pr.merged'))
      expect(link.find('.pr-icon-primary').exists()).toBe(true)
      expect(link.find('.dou-status-dot').exists()).toBe(true)
      expect(wrapper.find('.pr-card').exists()).toBe(false)
      await link.trigger('focus')
      expect(wrapper.get('.pr-card-title').text()).toBe('New icons')
      expect(wrapper.get('.pr-checks button').text()).toBe('CI')
    } finally {
      wrapper.unmount()
    }
  },
)

test('shows the plan snapshot status before live thread or preview data is available', () => {
  const wrapper = mount(PlanNodeDetail, { props: { node }, global })
  try {
    const link = wrapper.get(`a[href="${url}"]`)
    expect(link.attributes('aria-description')).toBe(t('link.pr.open'))
    expect(link.find('.dou-status-dot').exists()).toBe(true)
  } finally {
    wrapper.unmount()
  }
})

test('uses tracked PR state for a message permalink and previews the canonical address', async () => {
  track({ state: 'closed' })
  const summarize = vi.spyOn(api, 'summarizeLinks')
  const preview = vi.spyOn(api, 'previewLink').mockResolvedValue({ preview: null })
  const href = 'https://github.com/daidr/Dou-UI/pull/472?tab=files#discussion'
  const wrapper = mount(MessageLink, {
    props: { href, external: true, bare: true, children: h('span', href) },
    global,
  })
  try {
    const link = wrapper.get(`a[href="${href}"]`)
    expect(link.text()).toBe('#472')
    expect(link.attributes('aria-description')).toBe(t('link.pr.closed'))
    expect(summarize).not.toHaveBeenCalled()
    await link.trigger('focus')
    await flushPromises()
    expect(preview).toHaveBeenCalledWith(url, expect.any(AbortSignal))
  } finally {
    wrapper.unmount()
  }
})

test('shows merge conflict indicator on open PR pill and hover card', async () => {
  track({ state: 'open', conflicted: true, checks: { state: 'success', failed: [] } })
  vi.spyOn(api, 'previewLink').mockResolvedValue({ preview: null })
  const wrapper = mount(PullRequestTag, {
    props: { thread: state.detail!.threads[0], pill: true },
    global,
  })
  try {
    const link = wrapper.get(`a[href="${url}"]`)
    expect(link.classes()).toContain('pr-pill-conflicted')
    expect(link.find('.pr-icon-warning').exists()).toBe(true)
    expect(link.attributes('aria-description')).toBe(t('link.pr.conflicted'))
    await link.trigger('focus')
    const tags = wrapper.findAll('.pr-card-head .dou-tag')
    expect(tags).toHaveLength(1)
    expect(tags[0]!.text()).toBe(t('link.pr.conflicted'))
  } finally {
    wrapper.unmount()
  }
})

test('shows both draft and merge conflict tags for a conflicted draft PR', async () => {
  track({ state: 'open', conflicted: true, checks: { state: 'none', failed: [] } })
  vi.spyOn(api, 'previewLink').mockResolvedValue({
    preview: {
      kind: 'pull-request',
      repository: 'daidr/dou-ui',
      number: 472,
      url,
      title: 'Draft icons',
      state: 'open',
      draft: true,
      conflicted: true,
      author: 'daidr',
      additions: 10,
      deletions: 2,
      changedFiles: 1,
      at: '2026-11-22T00:00:00Z',
    },
  })
  const wrapper = mount(PullRequestTag, {
    props: { thread: state.detail!.threads[0], pill: true },
    global,
  })
  try {
    const link = wrapper.get(`a[href="${url}"]`)
    await link.trigger('focus')
    await flushPromises()
    expect(link.classes()).toContain('pr-pill-conflicted')
    expect(link.attributes('aria-description')).toBe(
      `${t('link.pr.draft')} · ${t('link.pr.conflicted')}`,
    )
    const tags = wrapper.findAll('.pr-card-head .dou-tag').map((tag) => tag.text())
    expect(tags).toEqual([t('link.pr.draft'), t('link.pr.conflicted')])
  } finally {
    wrapper.unmount()
  }
})

test('does not show conflict state on merged or closed PRs even if preview is stale', async () => {
  track({ state: 'merged', conflicted: true, checks: { state: 'success', failed: [] } })
  vi.spyOn(api, 'previewLink').mockResolvedValue({
    preview: {
      kind: 'pull-request',
      repository: 'daidr/dou-ui',
      number: 472,
      url,
      title: 'New icons',
      state: 'open',
      draft: false,
      conflicted: true,
      author: 'daidr',
      additions: 10,
      deletions: 2,
      changedFiles: 1,
      at: '2026-11-22T00:00:00Z',
    },
  })
  const wrapper = mount(PullRequestTag, {
    props: { thread: state.detail!.threads[0], pill: true },
    global,
  })
  try {
    const link = wrapper.get(`a[href="${url}"]`)
    await link.trigger('focus')
    await flushPromises()
    expect(link.classes()).not.toContain('pr-pill-conflicted')
    expect(link.find('.pr-icon-primary').exists()).toBe(true)
    expect(link.attributes('aria-description')).toBe(t('link.pr.merged'))
    const tags = wrapper.findAll('.pr-card-head .dou-tag').map((tag) => tag.text())
    expect(tags).toEqual([t('link.pr.merged')])
  } finally {
    wrapper.unmount()
  }
})

test('shows conflict state from plan snapshot before live thread data loads', () => {
  const conflictedNode: PlanNodeView = {
    ...node,
    threads: [{ ...node.threads[0]!, pullRequest: { ...summary, conflicted: true } }],
  }
  const wrapper = mount(PlanNodeDetail, { props: { node: conflictedNode }, global })
  try {
    const link = wrapper.get(`a[href="${url}"]`)
    expect(link.find('.pr-icon-warning').exists()).toBe(true)
    expect(link.attributes('aria-description')).toBe(t('link.pr.conflicted'))
  } finally {
    wrapper.unmount()
  }
})

test('shows conflict state from link preview on untracked PR links', async () => {
  vi.spyOn(api, 'summarizeLinks').mockResolvedValue({
    links: { [url]: { kind: 'pull-request', label: '#472' } },
  })
  vi.spyOn(api, 'previewLink').mockResolvedValue({
    preview: {
      kind: 'pull-request',
      repository: 'daidr/dou-ui',
      number: 472,
      url,
      title: 'Conflicted external PR',
      state: 'open',
      draft: false,
      conflicted: true,
      author: 'daidr',
      additions: 5,
      deletions: 3,
      changedFiles: 2,
      at: '2026-11-22T00:00:00Z',
    },
  })
  const wrapper = mount(MessageLink, {
    props: { href: url, external: true, bare: true, children: h('span', url) },
    global,
  })
  try {
    await flushPromises()
    const link = wrapper.get(`a[href="${url}"]`)
    await link.trigger('focus')
    await flushPromises()
    expect(link.find('.pr-icon-warning').exists()).toBe(true)
    expect(link.attributes('aria-description')).toBe(t('link.pr.conflicted'))
    const tags = wrapper.findAll('.pr-card-head .dou-tag').map((tag) => tag.text())
    expect(tags).toEqual([t('link.pr.conflicted')])
  } finally {
    wrapper.unmount()
  }
})

test('clears conflict state when tracked PR resolves conflicts even if cached preview is stale', async () => {
  track({ state: 'open', conflicted: true, checks: { state: 'success', failed: [] } })
  vi.spyOn(api, 'previewLink').mockResolvedValue({
    preview: {
      kind: 'pull-request',
      repository: 'daidr/dou-ui',
      number: 472,
      url,
      title: 'New icons',
      state: 'open',
      draft: false,
      conflicted: true,
      author: 'daidr',
      additions: 10,
      deletions: 2,
      changedFiles: 1,
      at: '2026-11-22T00:00:00Z',
    },
  })
  const wrapper = mount(PullRequestTag, {
    props: { thread: state.detail!.threads[0], pill: true },
    global,
  })
  try {
    const link = wrapper.get(`a[href="${url}"]`)
    await link.trigger('focus')
    await flushPromises()
    expect(link.classes()).toContain('pr-pill-conflicted')
    expect(wrapper.findAll('.pr-card-head .dou-tag').map((tag) => tag.text())).toEqual([
      t('link.pr.conflicted'),
    ])

    // Sync updates tracked PR after conflicts are resolved while cached preview still has conflicted: true.
    track({ state: 'open', conflicted: undefined, checks: { state: 'success', failed: [] } })
    await wrapper.setProps({ thread: state.detail!.threads[0] })
    expect(link.classes()).not.toContain('pr-pill-conflicted')
    expect(link.find('.pr-icon-success').exists()).toBe(true)
    expect(link.attributes('aria-description')).toBe(t('link.pr.open'))
    expect(wrapper.findAll('.pr-card-head .dou-tag').map((tag) => tag.text())).toEqual([
      t('link.pr.open'),
    ])
  } finally {
    wrapper.unmount()
  }
})
