import type { PlanNodeView, PlanRevisionView, PlanView } from '@ariadne/core'
import type { FlowDocument } from '@dou-ui/flow'
import { describe, expect, test } from 'vitest'
import { editOf, mergeIntoForm, reasonOf } from './edit'
import {
  freshChanges,
  layoutPlan,
  NODE_WIDTH,
  nodeHeight,
  offsetsOf,
  toFlowDocument,
} from './layout'

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
    issues: [],
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
const translate = (key: string, params?: Record<string, unknown>) =>
  `${key}${params ? JSON.stringify(params) : ''}`

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

  test('the reason names each change, then counts the rest', () => {
    expect(reasonOf([{ op: 'link', from: 'a', to: 'b' }], title, translate)).toBe(
      'plan.reason.link{"from":"A","to":"B"}',
    )
    const swap = reasonOf(
      [
        { op: 'unlink', from: 'a', to: 'b' },
        { op: 'link', from: 'a', to: 'c' },
      ],
      title,
      translate,
    )
    expect(swap).toBe(
      'plan.reason.unlink{"from":"A","to":"B"}plan.reason.joinplan.reason.link{"from":"A","to":"C"}',
    )
    const many = reasonOf(
      ['a', 'b', 'c', 'd', 'e'].map((id) => ({ op: 'remove' as const, id })),
      title,
      translate,
    )
    expect(many).toMatch(/^plan\.reason\.more\{"first":".*plan\.reason\.remove.*","count":5\}$/)
  })
})

describe('previews', () => {
  const current = plan([node('a', [], { threads: [] }), node('b', ['a'])])

  test('draw the preview from the server, keeping the status of nodes that exist now', () => {
    const preview = {
      nodes: [
        { ...node('a'), mark: 'changed' as const },
        { ...node('b', ['a']), mark: 'removed' as const },
        { ...node('c', ['a']), mark: 'added' as const },
      ],
      edges: [
        { from: 'a', to: 'b', mark: 'removed' as const },
        { from: 'a', to: 'c', mark: 'added' as const },
      ],
    }
    const doc = toFlowDocument(current, { preview })
    expect(doc.nodes.map((n) => [n.id, n.data?.mark])).toEqual([
      ['a', 'changed'],
      ['b', 'removed'],
      ['c', 'added'],
    ])
    expect(doc.edges.find((e) => e.id === 'a>c')?.animated).toBe(true)
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
    // The one seen fell out of the latest revisions the plan lists.
    const lost = freshChanges(plan([], revisions), 'r0')
    expect(lost.truncated).toBe(true)
    expect(lost.revisions.map((r) => r.id)).toEqual(['r5', 'r2', 'r1'])
  })

  test('a moved node keeps its offset from the layout', () => {
    const auto = toFlowDocument(plan([node('a'), node('b', ['a'])]))
    const b = auto.nodes.find((n) => n.id === 'b')!
    const doc = toFlowDocument(plan([node('a'), node('b', ['a'])]), {
      offsets: { b: { x: 9, y: 9 } },
      fresh: new Set(['b']),
    })
    expect(doc.nodes.map((n) => [n.x, n.y, n.data?.fresh])).toEqual([
      [0, 0, false],
      [b.x + 9, b.y + 9, true],
    ])
  })

  test('a moved node goes along when its part of the plan is moved by another', () => {
    // Two parts side by side; `y` is moved a little. Then the first part grows, which
    // pushes the second part over: `y` must stay beside `x`, as the user put it.
    const before = plan([node('a'), node('b', ['a']), node('x'), node('y', ['x'])])
    const start = toFlowDocument(before)
    const moved = { y: { x: nodeIn(start, 'y').x + 40, y: nodeIn(start, 'y').y + 16 } }
    const offsets = offsetsOf(before, moved)
    const grown = plan([
      node('a'),
      node('b', ['a']),
      node('c', ['a']),
      node('d', ['a']),
      node('e', ['b', 'c', 'd']),
      node('x'),
      node('y', ['x']),
    ])
    const after = toFlowDocument(grown, { offsets })
    const plain = toFlowDocument(grown)
    expect(where(plain, 'x')).not.toEqual(where(start, 'x'))
    expect(nodeIn(after, 'y').x - nodeIn(after, 'x').x).toBe(moved.y.x - nodeIn(start, 'x').x)
    expect(nodeIn(after, 'y').y - nodeIn(after, 'x').y).toBe(moved.y.y - nodeIn(start, 'x').y)
  })

  test('a node added on the graph stays where it was dropped', () => {
    const grown = plan([node('a'), node('b', ['a']), node('n')])
    const offsets = offsetsOf(grown, { n: { x: 500, y: 40 } })
    const doc = toFlowDocument(grown, { offsets })
    expect(doc.nodes.find((n) => n.id === 'n')).toMatchObject({ x: 500, y: 40 })
  })
})

const fields = (name: string, description: string, acceptance = '') => ({
  title: name,
  description,
  acceptance,
})

describe('mergeIntoForm', () => {
  test('takes newer values of fields the user left alone, keeps theirs where they typed', () => {
    const base = fields('实现', '')
    const form = fields('实现计划图', '')
    const next = mergeIntoForm(form, base, fields('实现', '协调器写的说明', '要有测试'))
    // The description the coordinator wrote comes in; the user's title stays.
    expect(form).toEqual(fields('实现计划图', '协调器写的说明', '要有测试'))
    expect(next).toEqual(fields('实现', '协调器写的说明', '要有测试'))
  })

  test('a field both sides changed keeps the user text and its old starting point', () => {
    const form = fields('甲', '我的说明')
    const next = mergeIntoForm(form, fields('甲', ''), fields('甲', '协调器的说明'))
    expect(form.description).toBe('我的说明')
    // Saving from here sends only the user's field, expecting the newer value to be kept
    // out of it until they choose.
    expect(next.description).toBe('')
  })
})

const nodeIn = (doc: FlowDocument, id: string) => doc.nodes.find((n) => n.id === id)!
const where = (doc: FlowDocument, id: string) => ({ x: nodeIn(doc, id).x, y: nodeIn(doc, id).y })

const overlaps = (nodes: PlanNodeView[], at: Map<string, { x: number; y: number }>) =>
  nodes.some((a, i) =>
    nodes.slice(i + 1).some((b) => {
      const p = at.get(a.id)!
      const q = at.get(b.id)!
      return (
        Math.abs(p.x - q.x) < NODE_WIDTH &&
        (p.y < q.y ? q.y - p.y < nodeHeight(a) : p.y - q.y < nodeHeight(b))
      )
    }),
  )

describe('layoutPlan', () => {
  test('a node sits below what it depends on', () => {
    const nodes = [node('a'), node('b', ['a']), node('c', ['b']), node('d', ['a'])]
    const at = layoutPlan(nodes)
    expect(at.get('b')!.y).toBeGreaterThan(at.get('a')!.y)
    expect(at.get('c')!.y).toBeGreaterThan(at.get('b')!.y)
    expect(at.get('d')!.y).toBeGreaterThan(at.get('a')!.y)
    expect(overlaps(nodes, at)).toBe(false)
  })

  test('a source waits on the level just above what needs it', () => {
    // `late` feeds only `c`, three levels down: it sits right above `c`, not in the top row.
    const nodes = [node('a'), node('b', ['a']), node('late'), node('c', ['b', 'late'])]
    const at = layoutPlan(nodes)
    expect(at.get('late')!.y).toBe(at.get('b')!.y)
  })

  test('unrelated parts and lone nodes are kept apart', () => {
    const nodes = [
      node('a'),
      node('b', ['a']),
      node('x'),
      node('y', ['x']),
      node('lone1'),
      node('lone2'),
    ]
    const at = layoutPlan(nodes)
    expect(overlaps(nodes, at)).toBe(false)
    const right = (ids: string[]) => Math.max(...ids.map((id) => at.get(id)!.x)) + NODE_WIDTH
    const left = (ids: string[]) => Math.min(...ids.map((id) => at.get(id)!.x))
    const bottom = (ids: string[]) =>
      Math.max(...ids.map((id) => at.get(id)!.y + nodeHeight(nodes.find((n) => n.id === id)!)))
    const top = (ids: string[]) => Math.min(...ids.map((id) => at.get(id)!.y))
    const apart = (p: string[], q: string[]) =>
      right(p) <= left(q) || right(q) <= left(p) || bottom(p) <= top(q) || bottom(q) <= top(p)
    expect(apart(['a', 'b'], ['x', 'y'])).toBe(true)
    expect(apart(['a', 'b'], ['lone1', 'lone2'])).toBe(true)
    expect(apart(['x', 'y'], ['lone1', 'lone2'])).toBe(true)
  })

  test('a wide plan wraps instead of running off to one side', () => {
    const nodes = Array.from({ length: 12 }, (_, i) => [
      node(`s${i}`),
      node(`t${i}`, [`s${i}`]),
    ]).flat()
    const at = layoutPlan(nodes)
    const xs = [...at.values()].map((p) => p.x)
    const ys = [...at.values()].map((p) => p.y)
    const width = Math.max(...xs) - Math.min(...xs) + NODE_WIDTH
    const height = Math.max(...ys) - Math.min(...ys)
    expect(width).toBeLessThan(height * 3)
    expect(overlaps(nodes, at)).toBe(false)
  })

  test('a previewed change is laid out with its own edges', () => {
    const doc = toFlowDocument(plan([node('a'), node('b')]), {
      preview: {
        nodes: [
          { ...node('a'), mark: null },
          { ...node('b', ['a']), mark: 'changed' },
        ],
        edges: [{ from: 'a', to: 'b', mark: 'added' }],
      },
    })
    const y = (id: string) => doc.nodes.find((n) => n.id === id)!.y
    expect(y('b')).toBeGreaterThan(y('a'))
  })
})
