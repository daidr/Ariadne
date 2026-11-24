import type { FlowDocument } from '@dou-ui/flow'
import { describe, expect, test } from 'vitest'
import {
  areNodesOutsideViewport,
  collectAutoRecommendedOptionIds,
  optionGroupId,
  optionPickMode,
  summarizeDecisionGroups,
} from './decision-groups'

function sampleBoard(): FlowDocument {
  return {
    version: 1,
    nodes: [
      {
        id: 'grp-r1',
        type: 'group',
        x: 0,
        y: 0,
        width: 800,
        height: 300,
        data: { label: 'Round 1', round: 1, pick: 'one', confirmed: true },
      },
      {
        id: 'opt-r1-a',
        type: 'option',
        parent: 'grp-r1',
        x: 20,
        y: 50,
        width: 300,
        height: 200,
        data: { title: 'R1 A', group: 'grp-r1', chosen: true },
      },
      {
        id: 'grp-layout',
        type: 'group',
        x: 0,
        y: 360,
        width: 800,
        height: 300,
        data: { label: 'Layout', round: 2, pick: 'one' },
      },
      {
        id: 'opt-l1',
        type: 'option',
        parent: 'grp-layout',
        x: 20,
        y: 410,
        width: 300,
        height: 200,
        data: { title: 'Sidebar', group: 'grp-layout', recommended: true },
      },
      {
        id: 'opt-l2',
        type: 'option',
        parent: 'grp-layout',
        x: 360,
        y: 410,
        width: 300,
        height: 200,
        data: { title: 'Topbar', group: 'grp-layout' },
      },
      {
        id: 'grp-tags',
        type: 'group',
        x: 0,
        y: 720,
        width: 800,
        height: 300,
        data: { label: 'Features', round: 2, pick: 'many' },
      },
      {
        id: 'opt-t1',
        type: 'option',
        parent: 'grp-tags',
        x: 20,
        y: 770,
        width: 300,
        height: 200,
        data: { title: 'Search', group: 'grp-tags', recommended: true },
      },
      {
        id: 'opt-t2',
        type: 'option',
        parent: 'grp-tags',
        x: 360,
        y: 770,
        width: 300,
        height: 200,
        data: { title: 'Export', group: 'grp-tags', recommended: true },
      },
      {
        id: 'grp-open',
        type: 'group',
        x: 0,
        y: 1080,
        width: 800,
        height: 300,
        data: { label: 'Naming', round: 2, pick: 'one' },
      },
      {
        id: 'opt-n1',
        type: 'option',
        parent: 'grp-open',
        x: 20,
        y: 1130,
        width: 300,
        height: 200,
        data: { title: 'Alpha', group: 'grp-open' },
      },
    ],
    edges: [],
  }
}

describe('decision-groups helpers', () => {
  test('resolves group id and inherits pick mode from group', () => {
    const doc = sampleBoard()
    const byId = new Map(doc.nodes.map((n) => [n.id, n]))
    expect(optionGroupId(byId.get('opt-l1')!, byId)).toBe('grp-layout')
    expect(optionPickMode(byId.get('opt-l1')!, byId)).toBe('one')
    expect(optionPickMode(byId.get('opt-t1')!, byId)).toBe('many')
  })

  test('summarizes active decision groups while ignoring confirmed rounds', () => {
    const doc = sampleBoard()
    expect(summarizeDecisionGroups(doc)).toEqual({
      totalGroups: 3,
      chooseGroups: 3,
      verifyGroups: 0,
      pickedGroups: 0,
      recommendedFallbackGroups: 2,
      remainingGroups: 1,
      verifyItemsTotal: 0,
      verifyApproved: 0,
      verifyChangesRequested: 0,
      verifyPending: 0,
      hasActiveDecisionGroups: true,
    })

    // Picking one option in grp-layout updates pickedGroups and reduces recommendedFallbackGroups.
    const l2 = doc.nodes.find((n) => n.id === 'opt-l2')!
    l2.data = { ...l2.data, chosen: true }
    expect(summarizeDecisionGroups(doc)).toEqual({
      totalGroups: 3,
      chooseGroups: 3,
      verifyGroups: 0,
      pickedGroups: 1,
      recommendedFallbackGroups: 1,
      remainingGroups: 1,
      verifyItemsTotal: 0,
      verifyApproved: 0,
      verifyChangesRequested: 0,
      verifyPending: 0,
      hasActiveDecisionGroups: true,
    })
  })

  test('summarizes verify groups separately and skips them for auto-recommended picks', () => {
    const doc: FlowDocument = {
      version: 1,
      nodes: [
        {
          id: 'grp-v1',
          type: 'group',
          x: 0,
          y: 0,
          width: 800,
          height: 360,
          data: { label: 'Homepage Verify', round: 1, mode: 'verify' },
        },
        {
          id: 'v-a',
          type: 'option',
          parent: 'grp-v1',
          x: 20,
          y: 50,
          width: 300,
          height: 220,
          data: { title: 'Hero section', group: 'grp-v1', verdict: 'approved' },
        },
        {
          id: 'v-b',
          type: 'option',
          parent: 'grp-v1',
          x: 360,
          y: 50,
          width: 300,
          height: 220,
          data: {
            title: 'Footer alignment',
            group: 'grp-v1',
            verdict: 'changes_requested',
            recommended: true,
          },
        },
        {
          id: 'v-c',
          type: 'option',
          parent: 'grp-v1',
          x: 700,
          y: 50,
          width: 300,
          height: 220,
          data: { title: 'Mobile nav', group: 'grp-v1' },
        },
      ],
      edges: [],
    }
    expect(summarizeDecisionGroups(doc)).toEqual({
      totalGroups: 1,
      chooseGroups: 0,
      verifyGroups: 1,
      pickedGroups: 0,
      recommendedFallbackGroups: 0,
      remainingGroups: 0,
      verifyItemsTotal: 3,
      verifyApproved: 1,
      verifyChangesRequested: 1,
      verifyPending: 1,
      hasActiveDecisionGroups: true,
    })
    expect(collectAutoRecommendedOptionIds(doc)).toEqual([])
  })

  test('collects auto-recommended options for unpicked active groups respecting pick mode', () => {
    const doc = sampleBoard()
    // Single-select grp-layout takes 1 recommended; multi-select grp-tags takes both; grp-open has none.
    expect(collectAutoRecommendedOptionIds(doc)).toEqual(['opt-l1', 'opt-t1', 'opt-t2'])

    // Once grp-layout has a manual pick, only grp-tags recommended options are collected.
    const l2 = doc.nodes.find((n) => n.id === 'opt-l2')!
    l2.data = { ...l2.data, chosen: true }
    expect(collectAutoRecommendedOptionIds(doc)).toEqual(['opt-t1', 'opt-t2'])
  })

  test('detects when newly added nodes fall outside the current viewport', () => {
    const doc = sampleBoard()
    const grpOpen = doc.nodes.find((n) => n.id === 'grp-open')!
    const grpR1 = doc.nodes.find((n) => n.id === 'grp-r1')!
    const viewport = { x: 0, y: 0, zoom: 1 }
    const canvas = { width: 900, height: 600 }

    expect(areNodesOutsideViewport([grpR1], viewport, canvas)).toBe(false)
    expect(areNodesOutsideViewport([grpOpen], viewport, canvas)).toBe(true)
  })
})
