import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { Ariadne } from './ariadne'
import { DomainError } from './errors'
import type { PlanOp } from './events'
import { applyOps, emptyGraph, needsConfirmation, parsePlanOps, previewPlanOps } from './plan'

let app: Ariadne
let projectId: string
let clock = 1_000

beforeEach(() => {
  clock = 1_000
  app = new Ariadne({ path: ':memory:', now: () => ++clock })
  projectId = app.createProject('P').id
})

afterEach(() => app.close())

const add = (id: string, title = id, kind: 'task' | 'milestone' = 'task'): PlanOp => ({
  op: 'add',
  id,
  kind,
  title,
  description: '',
  acceptance: '',
})

const confirmation = {
  question: (changes: { key: string }[]) =>
    ['协调器想修改计划', ...changes.map((c) => c.key)].join('\n'),
  apply: { label: '同意', consequence: '修改' },
  keep: { label: '保持', consequence: '不变' },
}

describe('plan graph ops', () => {
  test('refuses cycles, self links and missing nodes', () => {
    const { graph } = applyOps(emptyGraph(), [
      add('a'),
      add('b'),
      { op: 'link', from: 'a', to: 'b' },
    ])
    expect(() => applyOps(graph, [{ op: 'link', from: 'b', to: 'a' }])).toThrow(DomainError)
    expect(() => applyOps(graph, [{ op: 'link', from: 'a', to: 'a' }])).toThrow(DomainError)
    expect(() => applyOps(graph, [{ op: 'remove', id: 'x' }])).toThrow(DomainError)
  })

  test('the inverse of a change restores the graph', () => {
    const start = applyOps(emptyGraph(), [
      add('a'),
      add('b'),
      add('c'),
      { op: 'link', from: 'a', to: 'b' },
      { op: 'link', from: 'b', to: 'c' },
    ]).graph
    const changed = applyOps(start, [
      { op: 'remove', id: 'b' },
      { op: 'update', id: 'a', title: 'A' },
      add('d'),
      { op: 'link', from: 'a', to: 'd' },
    ])
    const restored = applyOps(changed.graph, changed.inverse).graph
    expect(restored.nodes).toEqual(start.nodes)
    expect(restored.edges).toEqual(start.edges)
  })

  test('removing, re-accepting and milestones need the user', () => {
    const { graph } = applyOps(emptyGraph(), [add('a'), add('m', 'M', 'milestone')])
    expect(needsConfirmation(graph, [add('b'), { op: 'update', id: 'a', title: 'x' }])).toBe(false)
    expect(needsConfirmation(graph, [{ op: 'remove', id: 'a' }])).toBe(true)
    expect(needsConfirmation(graph, [{ op: 'update', id: 'a', acceptance: '测试通过' }])).toBe(true)
    expect(needsConfirmation(graph, [{ op: 'update', id: 'm', title: 'M2' }])).toBe(true)
    expect(needsConfirmation(graph, [add('n', 'N', 'milestone')])).toBe(true)
  })
})

describe('plan revisions', () => {
  test('small changes apply at once and can be reverted', () => {
    const change = app.revisePlan(projectId, {
      author: 'coordinator',
      reason: '拆分任务',
      ops: [add('a', '调研'), add('b', '实现'), { op: 'link', from: 'a', to: 'b' }],
      confirmation,
    })
    expect(change.status).toBe('applied')
    const plan = app.getPlan(projectId)
    expect(plan.nodes.map((n) => [n.title, n.dependsOn, n.status])).toEqual([
      ['调研', [], 'none'],
      ['实现', ['a'], 'none'],
    ])
    expect(plan.nodes[0]?.changedIn).toBe(change.status === 'applied' ? change.revisionId : null)

    const revisionId = change.status === 'applied' ? change.revisionId : ''
    app.revertPlanRevision(projectId, revisionId, 'user', '不需要')
    const after = app.getPlan(projectId)
    expect(after.nodes).toEqual([])
    expect(after.revisions[1]?.revertedBy).toBe(after.revisions[0]?.id)
    expect(() => app.revertPlanRevision(projectId, revisionId, 'user', '')).toThrow(DomainError)
  })

  test('the coordinator removing a node waits for the user, then applies', () => {
    app.revisePlan(projectId, { author: 'coordinator', reason: '起步', ops: [add('a'), add('b')] })
    const change = app.revisePlan(projectId, {
      author: 'coordinator',
      reason: '合并任务',
      ops: [{ op: 'remove', id: 'b' }],
      confirmation,
    })
    expect(change.status).toBe('proposed')
    if (change.status !== 'proposed') return
    expect(app.getPlan(projectId).nodes).toHaveLength(2)
    expect(app.getPlan(projectId).proposals.map((p) => p.reason)).toEqual(['合并任务'])
    expect(app.listNeedsMe(projectId)).toHaveLength(1)

    app.answerAttention(change.attentionId, { choice: 0 })
    const plan = app.getPlan(projectId)
    expect(plan.nodes.map((n) => n.id)).toEqual(['a'])
    expect(plan.proposals).toEqual([])
    expect(plan.revisions[0]?.reason).toBe('合并任务')
  })

  test('keeping the plan as it is rejects the proposal', () => {
    app.revisePlan(projectId, { author: 'coordinator', reason: '起步', ops: [add('a')] })
    const change = app.revisePlan(projectId, {
      author: 'coordinator',
      reason: '删掉',
      ops: [{ op: 'remove', id: 'a' }],
      confirmation,
    })
    if (change.status !== 'proposed') throw new Error('expected a proposal')
    app.answerAttention(change.attentionId, { choice: 1 })
    expect(app.getPlan(projectId).nodes).toHaveLength(1)
    expect(app.getPlan(projectId).proposals).toEqual([])
  })

  test('an edit of several fields lists each of them', () => {
    app.revisePlan(projectId, { author: 'coordinator', reason: '起步', ops: [add('a', '旧')] })
    const change = app.revisePlan(projectId, {
      author: 'coordinator',
      reason: '调整',
      ops: [{ op: 'update', id: 'a', title: '新', acceptance: '测试通过' }],
      confirmation,
    })
    if (change.status !== 'proposed') throw new Error('expected a proposal')
    expect(app.getPlan(projectId).proposals[0]?.changes.map((c) => c.key)).toEqual([
      'plan.op.rename',
      'plan.op.acceptance',
    ])
  })

  test('the confirmation spells out each change, with the threads a removal unhooks', () => {
    app.revisePlan(projectId, { author: 'coordinator', reason: '起步', ops: [add('a', '调研')] })
    app.createThread(projectId, { title: 't', nodeId: 'a' })
    const change = app.revisePlan(projectId, {
      author: 'coordinator',
      reason: '合并',
      ops: [{ op: 'remove', id: 'a' }],
      confirmation,
    })
    if (change.status !== 'proposed') throw new Error('expected a proposal')
    const [proposal] = app.getPlan(projectId).proposals
    expect(proposal?.changes).toEqual([
      { key: 'plan.op.removeWithThreads', params: { title: '调研', count: 1 } },
    ])
    expect(JSON.stringify(app.listNeedsMe(projectId)[0])).toContain('plan.op.removeWithThreads')
  })

  test('a coordinator undo that removes nodes waits for the user too', () => {
    const first = app.revisePlan(projectId, {
      author: 'coordinator',
      reason: '起步',
      ops: [add('a')],
    })
    if (first.status !== 'applied') throw new Error('expected applied')
    const undo = app.revertPlanRevision(
      projectId,
      first.revisionId,
      'coordinator',
      '撤销',
      confirmation,
    )
    expect(undo.status).toBe('proposed')
    expect(app.getPlan(projectId).nodes).toHaveLength(1)
    if (undo.status !== 'proposed') return
    app.answerAttention(undo.attentionId, { choice: 0 })
    const plan = app.getPlan(projectId)
    expect(plan.nodes).toEqual([])
    expect(plan.revisions.find((r) => r.id === first.revisionId)?.revertedBy).toBe(
      plan.revisions[0]?.id,
    )
  })

  test('an agreed proposal that no longer applies stays visible and wakes the coordinator', () => {
    const project = app.getProject(projectId)
    app.revisePlan(projectId, { author: 'coordinator', reason: '起步', ops: [add('a'), add('b')] })
    const change = app.revisePlan(projectId, {
      author: 'coordinator',
      reason: '删掉 b',
      ops: [{ op: 'remove', id: 'b' }],
      confirmation,
    })
    if (change.status !== 'proposed') throw new Error('expected a proposal')
    app.revisePlan(projectId, {
      author: 'user',
      reason: '用户改',
      ops: [{ op: 'remove', id: 'b' }],
    })
    app.answerAttention(change.attentionId, { choice: 0 })
    const plan = app.getPlan(projectId)
    expect(plan.nodes.map((n) => n.id)).toEqual(['a'])
    expect(plan.proposals).toMatchObject([{ id: change.proposalId, status: 'stale' }])
    expect(plan.proposals[0]?.staleReason).toBeTruthy()
    expect(app.listTranscript(project.coordinatorId).at(-1)).toMatchObject({
      kind: 'note',
      note: { type: 'proposal-stale', proposalId: change.proposalId, reason: '删掉 b' },
    })
    expect(app.getConversation(project.coordinatorId).pendingSeq).toBeGreaterThan(0)
    // An unrelated change keeps the notice; the user dismissing it clears it.
    app.revisePlan(projectId, { author: 'coordinator', reason: '加一个', ops: [add('c')] })
    expect(app.getPlan(projectId).proposals).toHaveLength(1)
    app.dismissPlanProposal(projectId, change.proposalId)
    expect(app.getPlan(projectId).proposals).toEqual([])
  })

  test('undoing an edit refuses when the field changed again since', () => {
    app.revisePlan(projectId, { author: 'coordinator', reason: '起步', ops: [add('a', '旧')] })
    const rename = app.revisePlan(projectId, {
      author: 'coordinator',
      reason: '改名',
      ops: [{ op: 'update', id: 'a', title: '新' }],
    })
    if (rename.status !== 'applied') throw new Error('expected applied')
    app.revisePlan(projectId, {
      author: 'user',
      reason: '用户改',
      ops: [{ op: 'update', id: 'a', title: '用户的名字' }],
    })
    expect(() => app.revertPlanRevision(projectId, rename.revisionId, 'user', '')).toThrow(
      DomainError,
    )
    expect(app.getPlan(projectId).nodes[0]?.title).toBe('用户的名字')
  })

  test('node status follows its threads', () => {
    app.revisePlan(projectId, { author: 'coordinator', reason: '起步', ops: [add('a')] })
    const thread = app.createThread(projectId, { title: '调研', nodeId: 'a' })
    app.startThread(thread.id)
    expect(app.getPlan(projectId).nodes[0]?.status).toBe('process')
    app.reportThread(thread.id, 'blocked', '缺权限')
    expect(app.getPlan(projectId).nodes[0]?.status).toBe('error')
    app.reportThread(thread.id, 'done', '完成')
    expect(app.getPlan(projectId).nodes[0]?.status).toBe('finish')
    expect(() => app.createThread(projectId, { title: 'x', nodeId: 'nope' })).toThrow(DomainError)
  })

  test('a finished upstream thread wakes the threads downstream with its summary', () => {
    app.revisePlan(projectId, {
      author: 'coordinator',
      reason: '起步',
      ops: [add('a', '接口设计'), add('b'), { op: 'link', from: 'a', to: 'b' }],
    })
    const upstream = app.createThread(projectId, { title: '设计', nodeId: 'a' })
    const downstream = app.createThread(projectId, { title: '实现', nodeId: 'b' })
    app.reportThread(upstream.id, 'done', '接口定为 REST')
    const transcript = app.listTranscript(downstream.id)
    expect(transcript.at(-1)).toMatchObject({
      kind: 'note',
      note: { type: 'upstream-done', nodeTitle: '接口设计', summary: '接口定为 REST' },
    })
    expect(app.getConversation(downstream.id).pendingSeq).toBeGreaterThan(0)
  })

  test('rebuilding the projections reproduces the plan', () => {
    app.revisePlan(projectId, {
      author: 'coordinator',
      reason: '起步',
      ops: [add('a'), add('b'), { op: 'link', from: 'a', to: 'b' }],
    })
    const before = app.getPlan(projectId)
    app.rebuildProjections()
    expect(app.getPlan(projectId)).toEqual(before)
  })
})

describe('artifacts', () => {
  test('versions a document and refuses a stale save', () => {
    const doc = app.createArtifact(projectId, {
      kind: 'document',
      title: '方案',
      content: '# 方案\n',
      author: 'crd_x',
    })
    expect(doc.version).toBe(1)
    const v2 = app.reviseArtifact(doc.id, {
      content: '# 方案\n\n改了',
      author: 'user',
      reason: '用户编辑',
      baseVersion: 1,
    })
    expect(v2.version).toBe(2)
    expect(() =>
      app.reviseArtifact(doc.id, { content: 'x', author: 'thr_y', reason: '', baseVersion: 1 }),
    ).toThrow(DomainError)
    expect(app.getArtifact(doc.id, 1).content).toBe('# 方案\n')
    expect(app.listArtifactVersions(doc.id).map((v) => v.version)).toEqual([2, 1])
    // Saving the same content again is not a new version.
    expect(
      app.reviseArtifact(doc.id, { content: '# 方案\n\n改了', author: 'user', reason: '' }).version,
    ).toBe(2)
  })

  test("the author sees the user's edit next turn", () => {
    const thread = app.createThread(projectId, { title: '写文档' })
    const doc = app.createArtifact(projectId, {
      kind: 'document',
      title: '方案',
      content: 'a',
      author: thread.id,
    })
    app.reviseArtifact(doc.id, { content: 'b', author: 'user', reason: '' })
    expect(app.listTranscript(thread.id).at(-1)).toMatchObject({
      kind: 'note',
      note: { type: 'artifact-edited', artifactId: doc.id, version: 2 },
    })
  })
})

describe('artifact notes', () => {
  test("a user's edit goes to the agent that last wrote the document", () => {
    const writer = app.createThread(projectId, { title: '写' })
    const editor = app.createThread(projectId, { title: '改' })
    const doc = app.createArtifact(projectId, {
      kind: 'document',
      title: '方案',
      content: 'a',
      author: writer.id,
    })
    app.reviseArtifact(doc.id, { content: 'b', author: editor.id, reason: '' })
    app.reviseArtifact(doc.id, { content: 'c', author: 'user', reason: '' })
    expect(app.listTranscript(editor.id).at(-1)).toMatchObject({
      kind: 'note',
      note: { type: 'artifact-edited', version: 3 },
    })
    expect(app.listTranscript(writer.id).some((e) => e.kind === 'note')).toBe(false)
  })
})

describe('project source', () => {
  test('is stored and cleared', () => {
    const source = { kind: 'local-directory', settings: { path: '/tmp/x' } }
    expect(app.setProjectSource(projectId, source).source).toEqual(source)
    expect(app.setProjectSource(projectId, null).source).toBeNull()
  })

  test('a thread remembers its workspace', () => {
    const thread = app.createThread(projectId, { title: 'x' })
    app.recordWorkspace(thread.id, { label: '/w/x', branch: 'ariadne/x', base: 'main' })
    app.recordWorkspace(thread.id, { label: '/w/x', branch: 'ariadne/x', base: 'main' })
    expect(app.getThread(thread.id).workspace).toEqual({
      label: '/w/x',
      branch: 'ariadne/x',
      base: 'main',
      removed: false,
    })
    expect(app.events({ projectId }).filter((e) => e.type === 'workspace.opened')).toHaveLength(1)
    // A removed worktree comes back as a new record when it is opened again.
    app.recordWorktreeRemoved(thread.id, 'merged')
    expect(app.getThread(thread.id).workspace?.removed).toBe(true)
    app.recordWorkspace(thread.id, { label: '/w/x', branch: 'ariadne/x', base: 'main' })
    expect(app.getThread(thread.id).workspace?.removed).toBe(false)
    expect(app.events({ projectId }).filter((e) => e.type === 'workspace.opened')).toHaveLength(2)
  })
})

describe('triage', () => {
  test('a user message in a thread wakes the coordinator too', () => {
    const project = app.getProject(projectId)
    const thread = app.createThread(projectId, { title: 'x' })
    app.postMessage(projectId, {
      conversationId: thread.id,
      author: 'user',
      content: '顺便改个别的',
    })
    expect(app.getConversation(project.coordinatorId).pendingSeq).toBeGreaterThan(0)
    expect(app.getConversation(thread.id).pendingSeq).toBeGreaterThan(0)
  })
})

describe('parsePlanOps', () => {
  test('keeps known fields of each op and fills an add', () => {
    expect(
      parsePlanOps([
        { op: 'add', id: 'a', title: 'A', extra: 1 },
        { op: 'update', id: 'a', title: 'B', expect: { title: 'A', other: 'x' } },
        { op: 'link', from: 'a', to: 'b' },
        { op: 'unlink', from: 'a', to: 'b' },
        { op: 'remove', id: 'a' },
      ]),
    ).toEqual([
      { op: 'add', id: 'a', kind: 'task', title: 'A', description: '', acceptance: '' },
      { op: 'update', id: 'a', title: 'B', expect: { title: 'A' } },
      { op: 'link', from: 'a', to: 'b' },
      { op: 'unlink', from: 'a', to: 'b' },
      { op: 'remove', id: 'a' },
    ])
  })

  test.each([
    ['not a list', {}],
    ['an empty list', []],
    ['an unknown op', [{ op: 'rename', id: 'a' }]],
    ['a missing id', [{ op: 'remove' }]],
    ['a long id', [{ op: 'remove', id: 'x'.repeat(81) }]],
    ['a bad kind', [{ op: 'add', id: 'a', title: 'A', kind: 'epic' }]],
    ['a non-text field', [{ op: 'update', id: 'a', title: 3 }]],
    ['a bad expect', [{ op: 'update', id: 'a', expect: 'A' }]],
  ])('refuses %s', (_, raw) => {
    expect(() => parsePlanOps(raw)).toThrow(DomainError)
  })
})

describe('previewPlanOps', () => {
  test('marks what a change adds, removes and changes, keeping removed parts', () => {
    const graph = applyOps(emptyGraph(), [
      add('a'),
      add('b'),
      { op: 'link', from: 'a', to: 'b' },
    ]).graph
    const preview = previewPlanOps(graph, [
      add('c'),
      { op: 'link', from: 'a', to: 'c' },
      { op: 'remove', id: 'b' },
      { op: 'update', id: 'a', acceptance: 'done when tested' },
    ])
    expect(preview?.nodes.map((n) => [n.id, n.mark, n.dependsOn])).toEqual([
      ['a', 'changed', []],
      ['b', 'removed', ['a']],
      ['c', 'added', ['a']],
    ])
    expect(preview?.edges).toEqual([
      { from: 'a', to: 'b', mark: 'removed' },
      { from: 'a', to: 'c', mark: 'added' },
    ])
    expect(previewPlanOps(graph, [{ op: 'remove', id: 'gone' }])).toBeNull()
  })

  test('an edit of a field changed since is a conflict naming the node', () => {
    const graph = applyOps(emptyGraph(), [add('a', '甲')]).graph
    try {
      applyOps(graph, [{ op: 'update', id: 'a', title: '乙', expect: { title: '丙' } }])
      expect.unreachable()
    } catch (error) {
      expect(error).toMatchObject({ code: 'error.planConflict', params: { title: '甲' } })
    }
  })
})
