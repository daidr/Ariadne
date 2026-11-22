// @vitest-environment happy-dom
import type { PlanNodeView, PlanProposalView, PlanView } from '@ariadne/core'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { state } from '../store'
import PlanMain from './PlanMain.vue'
import PlanPanel from './PlanPanel.vue'
import { planUi, setShowCompleted } from './ui'

const MockPlanGraph = defineComponent({
  name: 'PlanGraph',
  props: {
    plan: { type: Object, default: null },
    selected: { type: String, default: null },
    compact: { type: Boolean, default: false },
  },
  emits: ['update:selected'],
  setup(props, { emit }) {
    return () =>
      h(
        'div',
        { class: 'mock-plan-graph', 'data-selected': props.selected ?? '' },
        (props.plan?.nodes ?? []).map((node: PlanNodeView) =>
          h(
            'button',
            {
              type: 'button',
              class: 'mock-select-node',
              'data-node-id': node.id,
              onClick: () => emit('update:selected', node.id),
            },
            node.title,
          ),
        ),
      )
  },
})

const wrappers: VueWrapper[] = []

function makeNode(id: string, over: Partial<PlanNodeView> = {}): PlanNodeView {
  return {
    id,
    kind: 'task',
    title: `Node ${id}`,
    description: `Desc ${id}`,
    acceptance: '',
    group: '',
    dependsOn: [],
    status: 'done',
    threads: [],
    issues: [],
    gate: null,
    changedIn: null,
    ...over,
  }
}

function makePlan(
  projectId: string,
  nodes: PlanNodeView[],
  proposals: PlanProposalView[] = [],
): PlanView {
  return {
    projectId,
    nodes,
    revisions: [],
    proposals,
  }
}

function makeProposal(id: string, over: Partial<PlanProposalView> = {}): PlanProposalView {
  return {
    id,
    attentionId: `att_${id}`,
    reason: 'Adjust plan',
    ops: [],
    changes: [],
    status: 'open',
    staleReason: null,
    createdAt: 1000,
    preview: {
      nodes: [
        {
          id: 'n1',
          kind: 'task',
          title: 'Node n1',
          description: '',
          acceptance: '',
          group: '',
          dependsOn: [],
          mark: null,
        },
      ],
      edges: [],
    },
    ...over,
  }
}

beforeEach(() => {
  state.plan = null
  state.detail = null
  state.artifacts = []
  planUi.previewId = null
  for (const key of Object.keys(planUi.showCompleted)) delete planUi.showCompleted[key]
  for (const key of Object.keys(planUi.collapsedGroups)) delete planUi.collapsedGroups[key]
})

afterEach(() => {
  while (wrappers.length) wrappers.pop()?.unmount()
})

describe.each([
  ['PlanMain', PlanMain],
  ['PlanPanel', PlanPanel],
] as const)('%s all-completed edge cases', (_name, Component) => {
  test('clears selected detail when hiding all completed nodes', async () => {
    state.plan = makePlan('prj_done', [makeNode('a'), makeNode('b')])
    setShowCompleted('prj_done', true)

    const wrapper = mount(Component, { global: { stubs: { PlanGraph: MockPlanGraph } } })
    wrappers.push(wrapper)
    await flushPromises()

    expect(wrapper.find('.mock-plan-graph').exists()).toBe(true)
    await wrapper.find('.mock-select-node[data-node-id="a"]').trigger('click')
    await nextTick()
    expect(wrapper.find('.node-detail').exists()).toBe(true)
    expect(wrapper.find('.node-detail').text()).toContain('Node a')

    setShowCompleted('prj_done', false)
    await flushPromises()

    expect(wrapper.find('.mock-plan-graph').exists()).toBe(false)
    expect(wrapper.find('.node-detail').exists()).toBe(false)

    // Re-showing completed nodes does not resurrect the previously hidden selection.
    setShowCompleted('prj_done', true)
    await flushPromises()
    expect(wrapper.find('.mock-plan-graph').exists()).toBe(true)
    expect(wrapper.find('.node-detail').exists()).toBe(false)
  })

  test('keeps all-completed empty state when previewId is from another project or has null preview', async () => {
    const staleProposal = makeProposal('prop_stale', { preview: null })
    const validProposal = makeProposal('prop_valid')
    state.plan = makePlan('prj_done', [makeNode('a')], [staleProposal, validProposal])

    // Cross-project previewId that does not belong to this plan.
    planUi.previewId = 'prop_other_project'
    const wrapper = mount(Component, { global: { stubs: { PlanGraph: MockPlanGraph } } })
    wrappers.push(wrapper)
    await flushPromises()

    expect(wrapper.find('.mock-plan-graph').exists()).toBe(false)

    // Matching open proposal whose server preview is null (unfit/stale).
    planUi.previewId = 'prop_stale'
    await flushPromises()
    expect(wrapper.find('.mock-plan-graph').exists()).toBe(false)

    // Valid open proposal with a usable preview mounts the graph.
    planUi.previewId = 'prop_valid'
    await flushPromises()
    expect(wrapper.find('.mock-plan-graph').exists()).toBe(true)
  })
})
