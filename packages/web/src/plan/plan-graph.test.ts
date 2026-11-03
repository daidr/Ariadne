import type { PlanNodeView, PlanRevisionView, PlanView } from '@ariadne/core'
import type { FlowDocument } from '@dou-ui/flow'
import { describe, expect, test } from 'vitest'
import { editOf, reasonOf } from './edit'
import { freshChanges, previewOps, toFlowDocument } from './layout'

const node = (id: string, dependsOn: string[] = [], extra: Partial<PlanNodeView> = {}) =>
  ({
    id,
    kind: 'task',
    title: id.toUpperCase(),
    description: '',
    acceptance: '',
    dependsOn,
    status: 'none',
    threads: [],
    changedIn: null,
    ...extra,
  }) satisfies PlanNodeView

const revision = (id: string, extra: Partial<PlanRevisionView> = {}): PlanRevisionView => ({
  id,
  author: 'coordinator',
  reason: id,
  ops: [],
  revertOf: null,
  revertedBy: null,
  createdAt: 0,
  ...extra,
})

const title = (id: string) => id.toUpperCase()

const plan = (nodes: PlanNodeView[], revisions: PlanRevisionView[] = []): PlanView => ({
  projectId: 'p',
  nodes,
  revisions,
  proposals: [],
})

describe('editOf', () => {
  const before: FlowDocument = toFlowDocument(plan([node('a'), node('b', ['a']), node('c')]))

  test('a drawn edge makes its target depend on its source', () => {
    const edge = { id: 'e1', source: { node: 'b', port: 'out' }, target: { node: 'c', port: 'in' } }
    expect(editOf([{ type: 'addEdge', edge }], before, 'New').ops).toEqual([
      { op: 'link', from: 'b', to: 'c' },
    ])
  })

  test('removing a node drops its edges with it, without unlinking them again', () => {
    const { ops } = editOf(
      [
        { type: 'removeEdge', id: 'a>b' },
        { type: 'removeNode', id: 'a' },
      ],
      before,
      'New',
    )
    expect(ops).toEqual([{ op: 'remove', id: 'a' }])
    expect(editOf([{ type: 'removeEdge', id: 'a>b' }], before, 'New').ops).toEqual([
      { op: 'unlink', from: 'a', to: 'b' },
    ])
  })

  test('an edge moved to another node swaps the dependency', () => {
    const { ops } = editOf(
      [{ type: 'updateEdge', id: 'a>b', patch: { target: { node: 'c', port: 'in' } } }],
      before,
      'New',
    )
    expect(ops).toEqual([
      { op: 'unlink', from: 'a', to: 'b' },
      { op: 'link', from: 'a', to: 'c' },
    ])
  })

  test('moves are remembered, not sent; a pasted node is added with a title', () => {
    const edit = editOf(
      [
        { type: 'updateNode', id: 'a', patch: { x: 40 } },
        { type: 'addNode', node: { id: 'n', type: 'plan', x: 5, y: 6, data: { title: ' ' } } },
      ],
      before,
      'New',
    )
    expect(edit.ops).toEqual([
      { op: 'add', id: 'n', kind: 'task', title: 'New', description: '', acceptance: '' },
    ])
    expect(edit.moved).toEqual({ a: { x: 40, y: 0 }, n: { x: 5, y: 6 } })
  })

  test('the reason names a single change and counts several', () => {
    expect(reasonOf([{ op: 'link', from: 'a', to: 'b' }], title)).toEqual({
      key: 'plan.reason.link',
      params: { from: 'A', to: 'B' },
    })
    expect(
      reasonOf(
        [
          { op: 'remove', id: 'a' },
          { op: 'remove', id: 'b' },
        ],
        title,
      ).key,
    ).toBe('plan.reason.many')
  })
})

describe('previewOps', () => {
  const current = plan([node('a'), node('b', ['a'])])

  test('marks what a change adds, removes and changes, keeping removed parts visible', () => {
    const preview = previewOps(current, [
      { op: 'add', id: 'c', kind: 'task', title: 'C', description: '', acceptance: '' },
      { op: 'link', from: 'a', to: 'c' },
      { op: 'remove', id: 'b' },
      { op: 'update', id: 'a', acceptance: 'done when tested' },
    ])
    expect(preview.problem).toBeNull()
    expect(Object.fromEntries(preview.marks)).toEqual({ a: 'changed', b: 'removed', c: 'added' })
    expect(preview.nodes.map((n) => n.id)).toEqual(['a', 'b', 'c'])
    expect(preview.edges).toEqual([
      { from: 'a', to: 'b', mark: 'removed' },
      { from: 'a', to: 'c', mark: 'added' },
    ])
    const doc = toFlowDocument(current, { preview })
    expect(doc.nodes.map((n) => [n.id, n.data?.mark])).toEqual([
      ['a', 'changed'],
      ['b', 'removed'],
      ['c', 'added'],
    ])
    expect(doc.edges.find((e) => e.id === 'a>c')?.animated).toBe(true)
  })

  test('says why a change no longer applies, and the plan is shown as it is', () => {
    const preview = previewOps(current, [{ op: 'remove', id: 'gone' }])
    expect(preview.problem).toMatch(/gone/)
    expect(toFlowDocument(current, { preview }).nodes.map((n) => n.data?.mark)).toEqual([
      null,
      null,
    ])
  })
})

describe('freshChanges', () => {
  test('only standing coordinator changes newer than the last one seen', () => {
    const revisions = [
      revision('r5', { ops: [{ op: 'link', from: 'a', to: 'b' }] }),
      revision('r4', { author: 'user', ops: [{ op: 'remove', id: 'x' }] }),
      revision('r3', { revertedBy: 'r6', ops: [{ op: 'remove', id: 'y' }] }),
      revision('r2', { ops: [{ op: 'update', id: 'c', title: 'C2' }] }),
      revision('r1', { ops: [{ op: 'remove', id: 'z' }] }),
    ]
    const fresh = freshChanges(plan([], revisions), 'r1')
    expect(fresh.revisions.map((r) => r.id)).toEqual(['r5', 'r2'])
    expect([...fresh.nodes].toSorted()).toEqual(['a', 'b', 'c'])
    expect(freshChanges(plan([], revisions), 'r5').revisions).toEqual([])
  })

  test('saved positions win over the layout', () => {
    const doc = toFlowDocument(plan([node('a'), node('b', ['a'])]), {
      positions: { b: { x: 9, y: 9 } },
      fresh: new Set(['b']),
    })
    expect(doc.nodes.map((n) => [n.x, n.y, n.data?.fresh])).toEqual([
      [0, 0, false],
      [9, 9, true],
    ])
  })
})
