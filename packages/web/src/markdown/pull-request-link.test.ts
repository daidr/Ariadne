// @vitest-environment happy-dom
import type { PlanNodeView, ThreadPullRequest, ThreadView } from '@ariadne/core'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, expect, test, vi } from 'vitest'
import { h } from 'vue'
import { api, type ProjectDetail } from '../api'
import { t } from '../i18n'
import PlanNode from '../plan/PlanNode.vue'
import PlanNodeDetail from '../plan/PlanNodeDetail.vue'
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
