import type { PlanNodeView, PlanRevisionView, PlanView } from '@ariadne/core'
import { describe, expect, test } from 'vitest'
import {
  freshChanges,
  groupIdOf,
  layoutPlan,
  NODE_WIDTH,
  nodeHeight,
  partitionPlanNodes,
  startedAhead,
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
    gate: null,
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

const plan = (nodes: PlanNodeView[], revisions: PlanRevisionView[] = []): PlanView => ({
  projectId: 'p',
  nodes,
  revisions,
  proposals: [],
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
    const fresh = freshChanges(plan([node('a'), node('b'), node('c')], revisions), 'r1')
    expect(fresh.revisions.map((r) => r.id)).toEqual(['r5', 'r2'])
    expect([...fresh.nodes].toSorted()).toEqual([
      ['a', 'r5'],
      ['b', 'r5'],
      ['c', 'r2'],
    ])
    expect(freshChanges(plan([], revisions), 'r5').revisions).toEqual([])
    // The one seen fell out of the latest revisions the plan lists.
    const lost = freshChanges(plan([], revisions), 'r0')
    expect(lost.truncated).toBe(true)
    expect(lost.revisions.map((r) => r.id)).toEqual(['r5', 'r2', 'r1'])
  })
})

describe('partitionPlanNodes', () => {
  test('hides lone done nodes, all-done groups, and all-done connected subgraphs', () => {
    const nodes = [
      node('lone-done', [], { status: 'done' }),
      node('lone-active', [], { status: 'running' }),
      node('chain-1', [], { status: 'done' }),
      node('chain-2', ['chain-1'], { status: 'done' }),
      node('partial-1', [], { status: 'done' }),
      node('partial-2', ['partial-1'], { status: 'running' }),
      node('grp-done-1', [], { group: '已完成专题', status: 'done' }),
      node('grp-done-2', [], { group: '已完成专题', status: 'done' }),
      node('grp-open-1', [], { group: '进行中专题', status: 'done' }),
      node('grp-open-2', [], { group: '进行中专题', status: 'none' }),
    ]
    const { active, completedIds } = partitionPlanNodes(nodes)
    expect([...completedIds].toSorted()).toEqual([
      'chain-1',
      'chain-2',
      'grp-done-1',
      'grp-done-2',
      'lone-done',
    ])
    expect(active.map((n) => n.id)).toEqual([
      'lone-active',
      'partial-1',
      'partial-2',
      'grp-open-1',
      'grp-open-2',
    ])
  })

  test('keeps an all-done group visible when a downstream active node depends on it', () => {
    const nodes = [
      node('g1', [], { group: '前置阶段', status: 'done' }),
      node('g2', ['g1'], { group: '前置阶段', status: 'done' }),
      node('next', ['g2'], { status: 'running' }),
    ]
    const { active, completedIds } = partitionPlanNodes(nodes)
    expect(completedIds.size).toBe(0)
    expect(active.map((n) => n.id)).toEqual(['g1', 'g2', 'next'])
  })
})

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
    const at = layoutPlan(nodes).nodes
    expect(at.get('b')!.y).toBeGreaterThan(at.get('a')!.y)
    expect(at.get('c')!.y).toBeGreaterThan(at.get('b')!.y)
    expect(at.get('d')!.y).toBeGreaterThan(at.get('a')!.y)
    expect(overlaps(nodes, at)).toBe(false)
  })

  test('a source waits on the level just above what needs it', () => {
    // `late` feeds only `c`, three levels down: it sits right above `c`, not in the top row.
    const nodes = [node('a'), node('b', ['a']), node('late'), node('c', ['b', 'late'])]
    const at = layoutPlan(nodes).nodes
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
    const at = layoutPlan(nodes).nodes
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
    const at = layoutPlan(nodes).nodes
    const xs = [...at.values()].map((p) => p.x)
    const ys = [...at.values()].map((p) => p.y)
    const width = Math.max(...xs) - Math.min(...xs) + NODE_WIDTH
    const height = Math.max(...ys) - Math.min(...ys)
    expect(width).toBeLessThan(height * 3)
    expect(overlaps(nodes, at)).toBe(false)
  })

  test('a long edge runs between the nodes in its way', () => {
    // a → b → c → d, and a → d skips two levels.
    const nodes = [node('a'), node('b', ['a']), node('c', ['b']), node('d', ['c', 'a'])]
    const doc = toFlowDocument(plan(nodes))
    const long = doc.edges.find((edge) => edge.id === 'a>d')!
    expect(long.points?.length).toBeGreaterThan(0)
    expect(doc.edges.find((edge) => edge.id === 'a>b')!.points).toBeUndefined()
  })

  test('grouped nodes sit inside their container and cross-group dependencies flow top to bottom', () => {
    const nodes = [
      node('p1', [], { group: 'Protobuf 迁移' }),
      node('p2', [], { group: 'Protobuf 迁移' }),
      node('p3', ['p1', 'p2'], { group: 'Protobuf 迁移' }),
      node('k1', ['p3'], { group: '插件化改造' }),
      node('k2', ['k1'], { group: '插件化改造' }),
      node('solo'),
    ]
    const layout = layoutPlan(nodes)
    expect(overlaps(nodes, layout.nodes)).toBe(false)
    expect(layout.groups.map((g) => g.label)).toEqual(['Protobuf 迁移', '插件化改造'])

    const protoGroup = layout.groups[0]!
    const pluginGroup = layout.groups[1]!
    // Every member lies strictly inside its group container.
    for (const id of protoGroup.nodeIds) {
      const pos = layout.nodes.get(id)!
      const n = nodes.find((item) => item.id === id)!
      expect(pos.x).toBeGreaterThanOrEqual(protoGroup.x)
      expect(pos.y).toBeGreaterThanOrEqual(protoGroup.y)
      expect(pos.x + NODE_WIDTH).toBeLessThanOrEqual(protoGroup.x + protoGroup.width)
      expect(pos.y + nodeHeight(n)).toBeLessThanOrEqual(protoGroup.y + protoGroup.height)
    }
    // Downstream group sits below upstream group.
    expect(pluginGroup.y).toBeGreaterThanOrEqual(protoGroup.y + protoGroup.height)
  })

  test('toFlowDocument hides completed items by default and reveals them with showCompleted', () => {
    const nodes = [
      node('done-1', [], { group: '旧专题', status: 'done' }),
      node('done-2', [], { group: '旧专题', status: 'done' }),
      node('active-1', [], { group: '当前专题', status: 'done' }),
      node('active-2', [], { group: '当前专题', status: 'running' }),
      node('solo-done', [], { status: 'done' }),
    ]
    const activeDoc = toFlowDocument(plan(nodes))
    expect(activeDoc.nodes.map((n) => n.id)).toEqual([
      groupIdOf('当前专题'),
      'active-1',
      'active-2',
    ])
    expect(activeDoc.nodes.find((n) => n.id === 'active-1')?.parent).toBe(groupIdOf('当前专题'))
    expect(activeDoc.nodes.find((n) => n.id === groupIdOf('当前专题'))?.data).toMatchObject({
      label: '当前专题',
      doneCount: 1,
      totalCount: 2,
      allDone: false,
      archived: false,
    })

    const fullDoc = toFlowDocument(plan(nodes), {
      showCompleted: true,
      collapsedGroups: new Set([groupIdOf('旧专题')]),
    })
    expect(fullDoc.nodes.map((n) => n.id)).toEqual([
      groupIdOf('旧专题'),
      groupIdOf('当前专题'),
      'done-1',
      'done-2',
      'active-1',
      'active-2',
      'solo-done',
    ])
    expect(fullDoc.nodes.find((n) => n.id === groupIdOf('旧专题'))?.data).toMatchObject({
      label: '旧专题',
      collapsed: true,
      doneCount: 2,
      totalCount: 2,
      allDone: true,
      archived: true,
    })
    expect(fullDoc.nodes.find((n) => n.id === 'solo-done')?.data?.archived).toBe(true)
    expect(fullDoc.nodes.find((n) => n.id === 'active-1')?.data?.archived).toBe(false)
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

const thread = (
  stage: PlanNodeView['threads'][number]['stage'],
  lastHeadline: string | null,
): PlanNodeView['threads'][number] => ({
  id: `t-${stage}`,
  title: 'T',
  stage,
  stoppedBy: null,
  lastHeadline,
  pullRequest: null,
})

describe('nodeHeight', () => {
  test('makes room for the line a thread waiting on the user shows', () => {
    const plain = nodeHeight(node('a', [], { threads: [thread('working', '请审阅')] }))
    const waiting = nodeHeight(node('a', [], { threads: [thread('review', '请审阅')] }))
    const silent = nodeHeight(node('a', [], { threads: [thread('blocked', null)] }))
    expect(waiting).toBeGreaterThan(plain)
    expect(silent).toBe(plain)
  })
})

describe('startedAhead', () => {
  test('work started before a task it depends on is done is flagged, through the milestones and decisions between', () => {
    const nodes = [
      node('a', [], { status: 'running' }),
      node('m', ['a'], { kind: 'milestone' }),
      node('d', [], { kind: 'decision' }),
      node('b', ['m', 'd'], { status: 'running' }),
      node('c', ['d'], { status: 'running' }),
    ]
    expect(startedAhead(nodes)).toEqual(new Map([['b', ['A']]]))
    const doc = toFlowDocument(plan(nodes))
    const skipped = doc.edges.filter((edge) => edge.data?.skipped).map((edge) => edge.id)
    expect(skipped).toEqual(['m>b'])
  })
})
