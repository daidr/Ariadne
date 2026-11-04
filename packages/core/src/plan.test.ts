import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { Ariadne } from './ariadne'
import { confirmBlock } from './blocks'
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
  card: (changes: { key: string }[]) =>
    confirmBlock({
      title: '协调器想修改计划',
      detail: changes.map((c) => c.key).join('\n'),
      approveText: '同意',
      denyText: '保持',
    }),
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

    app.answerAttention(change.attentionId, { type: 'confirm', approved: true })
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
    app.answerAttention(change.attentionId, { type: 'confirm', approved: false })
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
    app.answerAttention(undo.attentionId, { type: 'confirm', approved: true })
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
    app.answerAttention(change.attentionId, { type: 'confirm', approved: true })
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

  test('undoing shows what goes with it, stops threads on removed nodes and wakes the coordinator', () => {
    const project = app.getProject(projectId)
    app.revisePlan(projectId, { author: 'coordinator', reason: '起步', ops: [add('a', '调研')] })
    const split = app.revisePlan(projectId, {
      author: 'coordinator',
      reason: '拆出实现',
      ops: [add('b', '实现')],
    })
    if (split.status !== 'applied') throw new Error('expected applied')
    // A later change links to the node the undo removes.
    app.revisePlan(projectId, {
      author: 'coordinator',
      reason: '加依赖',
      ops: [{ op: 'link', from: 'a', to: 'b' }],
    })
    const working = app.createThread(projectId, { title: '写实现', nodeId: 'b' })
    const finished = app.createThread(projectId, { title: '早做完的', nodeId: 'b' })
    app.reportThread(finished.id, { kind: 'done', summary: '完成', headline: '完成' })

    expect(app.previewPlanUndo(projectId, split.revisionId)).toEqual({
      changes: [
        { key: 'plan.op.remove', params: { title: '实现' } },
        { key: 'plan.op.unlink', params: { from: '调研', to: '实现' } },
      ],
      threads: [{ id: working.id, title: '写实现' }],
      blocked: null,
    })

    app.revertPlanRevision(projectId, split.revisionId, 'user', '撤销：拆出实现')
    expect(app.getPlan(projectId).nodes.map((n) => n.id)).toEqual(['a'])
    expect(app.getThread(working.id).stopped).toMatchObject({
      by: 'user',
      reason: '撤销：拆出实现',
    })
    expect(app.getThread(finished.id).stopped).toBeNull()
    expect(app.listTranscript(project.coordinatorId).slice(-2)).toMatchObject([
      { kind: 'note', note: { type: 'plan-changed-by-user', revertOf: split.revisionId } },
      { kind: 'note', note: { type: 'thread-stopped-by-user', threadId: working.id } },
    ])
    expect(app.getConversation(project.coordinatorId).pendingSeq).toBeGreaterThan(0)

    // Restoring undoes the undo: the change stands again and can be undone again.
    const undoId = app.getPlan(projectId).revisions[0]?.id ?? ''
    expect(app.previewPlanUndo(projectId, undoId).changes).toEqual([
      { key: 'plan.op.add', params: { title: '实现' } },
      { key: 'plan.op.link', params: { from: '调研', to: '实现' } },
    ])
    app.revertPlanRevision(projectId, undoId, 'user', '恢复：拆出实现')
    const plan = app.getPlan(projectId)
    expect(plan.nodes.map((n) => [n.id, n.dependsOn])).toEqual([
      ['a', []],
      ['b', ['a']],
    ])
    expect(plan.revisions.find((r) => r.id === split.revisionId)?.revertedBy).toBeNull()
    expect(plan.revisions.find((r) => r.id === undoId)?.revertedBy).toBe(plan.revisions[0]?.id)
    expect(app.previewPlanUndo(projectId, split.revisionId).blocked).toBeNull()
    // The stopped thread waits for the user to let it go on.
    expect(app.getThread(working.id).stopped).not.toBeNull()
  })

  test('an undo later changes are in the way of says why in words', () => {
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
    expect(app.previewPlanUndo(projectId, rename.revisionId).blocked).toEqual({
      key: 'error.planUndoChanged',
      params: { title: '用户的名字' },
    })
    app.revisePlan(projectId, { author: 'user', reason: '删掉', ops: [{ op: 'remove', id: 'a' }] })
    expect(app.previewPlanUndo(projectId, rename.revisionId).blocked).toEqual({
      key: 'error.planUndoGone',
      params: { title: '新' },
    })
    expect(() => app.revertPlanRevision(projectId, rename.revisionId, 'user', '')).toThrow(
      expect.objectContaining({ code: 'error.planUndoGone' }),
    )
  })

  test('a restore is undone through the original change, so its state stays right', () => {
    app.revisePlan(projectId, { author: 'coordinator', reason: '起步', ops: [add('a', 'a')] })
    const rename = app.revisePlan(projectId, {
      author: 'coordinator',
      reason: '改名',
      ops: [{ op: 'update', id: 'a', title: 'A2' }],
    })
    if (rename.status !== 'applied') throw new Error('expected applied')
    const undo = app.revertPlanRevision(projectId, rename.revisionId, 'user', '撤销：改名')
    if (undo.status !== 'applied') throw new Error('expected applied')
    const restore = app.revertPlanRevision(projectId, undo.revisionId, 'user', '恢复：改名')
    if (restore.status !== 'applied') throw new Error('expected applied')
    // Undoing the restore itself is refused, for the user and the coordinator alike.
    expect(() =>
      app.revertPlanRevision(projectId, restore.revisionId, 'coordinator', '再撤', confirmation),
    ).toThrow(expect.objectContaining({ code: 'error.planUndoRestore' }))
    expect(app.previewPlanUndo.bind(app, projectId, restore.revisionId)).toThrow(
      expect.objectContaining({ code: 'error.planUndoRestore' }),
    )
    // Undoing the original again works, and the change shows as undone and restorable.
    const again = app.revertPlanRevision(projectId, rename.revisionId, 'coordinator', '再撤')
    if (again.status !== 'applied') throw new Error('expected applied')
    let plan = app.getPlan(projectId)
    expect(plan.nodes[0]?.title).toBe('a')
    expect(plan.revisions.find((r) => r.id === rename.revisionId)?.revertedBy).toBe(
      again.revisionId,
    )
    app.revertPlanRevision(projectId, again.revisionId, 'user', '恢复：改名')
    plan = app.getPlan(projectId)
    expect(plan.nodes[0]?.title).toBe('A2')
    expect(plan.revisions.find((r) => r.id === rename.revisionId)?.revertedBy).toBeNull()
  })

  test('the user removing a node takes over the coordinator stopping its thread', () => {
    app.revisePlan(projectId, { author: 'coordinator', reason: '起步', ops: [add('a', '调研')] })
    const thread = app.createThread(projectId, { title: '调研', nodeId: 'a' })
    app.stopThread(thread.id, { by: 'coordinator', reason: '先等等' })
    const first = app.getPlan(projectId).revisions[0]?.id ?? ''
    expect(app.previewPlanUndo(projectId, first).threads).toEqual([
      { id: thread.id, title: '调研' },
    ])
    expect(app.getPlan(projectId).nodes[0]?.threads[0]?.stoppedBy).toBe('coordinator')
    app.revisePlan(projectId, {
      author: 'user',
      reason: '删除「调研」',
      ops: [{ op: 'remove', id: 'a' }],
    })
    expect(app.getThread(thread.id).stopped).toMatchObject({ by: 'user', reason: '删除「调研」' })
  })

  test('the user removing a node stops the threads on it', () => {
    app.revisePlan(projectId, { author: 'coordinator', reason: '起步', ops: [add('a')] })
    const thread = app.createThread(projectId, { title: '调研', nodeId: 'a' })
    app.revisePlan(projectId, {
      author: 'user',
      reason: '删除「a」',
      ops: [{ op: 'remove', id: 'a' }],
    })
    expect(app.getThread(thread.id).stopped).toMatchObject({ by: 'user', reason: '删除「a」' })
  })

  test('node status follows its threads', () => {
    app.revisePlan(projectId, { author: 'coordinator', reason: '起步', ops: [add('a')] })
    const thread = app.createThread(projectId, { title: '调研', nodeId: 'a' })
    app.startThread(thread.id)
    expect(app.getPlan(projectId).nodes[0]?.status).toBe('running')
    app.reportThread(thread.id, {
      kind: 'blocked',
      summary: '调用 GitHub 接口返回 403',
      headline: '缺少仓库权限',
    })
    expect(app.getPlan(projectId).nodes[0]?.status).toBe('error')
    expect(app.getPlan(projectId).nodes[0]?.threads[0]?.lastHeadline).toBe('缺少仓库权限')
    app.reportThread(thread.id, { kind: 'done', summary: '完成', headline: '完成' })
    expect(app.getPlan(projectId).nodes[0]?.status).toBe('done')
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
    app.reportThread(upstream.id, {
      kind: 'done',
      summary: '接口定为 REST',
      headline: '接口定为 REST',
    })
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

const coordinatorOf = () => app.getProject(projectId).coordinatorId
const doc = (author: string, content = '# 方案\n\n开业当天全场八折。\n') =>
  app.createArtifact(projectId, { kind: 'document', title: '方案', content, author })

describe('user edits and comments on artifacts', () => {
  test("an edit of the user's own document goes to the coordinator, with the changed lines", () => {
    const own = doc('user')
    app.reviseArtifact(own.id, {
      content: '# 方案\n\n开业当天全场九折。\n',
      author: 'user',
      reason: '',
    })
    const coordinator = coordinatorOf()
    expect(app.listTranscript(coordinator).at(-1)).toMatchObject({
      kind: 'note',
      note: {
        type: 'artifact-edited',
        artifactId: own.id,
        version: 2,
        diff: '…\n \n-开业当天全场八折。\n+开业当天全场九折。\n ',
      },
    })
    // Not woken: it reads the edit with whatever it does next.
    expect(app.getConversation(coordinator).pendingSeq).toBe(0)
    expect(app.listNotices(coordinator)).toMatchObject([
      { kind: 'artifact-edited', artifactId: own.id, title: '方案', version: 2 },
    ])
  })

  test("a stopped thread's document is the coordinator's to hear about", () => {
    const thread = app.createThread(projectId, { title: '写' })
    const written = doc(thread.id)
    app.stopThread(thread.id, { reason: '先停', by: 'user' })
    app.reviseArtifact(written.id, { content: 'b', author: 'user', reason: '' })
    expect(app.listNotices(thread.id)).toEqual([])
    expect(app.listNotices(coordinatorOf()).map((n) => n.kind)).toEqual(['artifact-edited'])
  })

  test("the user's comment wakes the writer, whose reply shows where it wrote it", () => {
    const thread = app.createThread(projectId, { title: '写' })
    const written = doc(thread.id)
    app.commentOnArtifact(written.id, 'user', {
      type: 'create',
      threadId: 'c1',
      commentId: 'm1',
      quote: '全场八折',
      body: '能和会员价叠加吗？',
    })
    expect(app.listTranscript(thread.id).at(-1)).toMatchObject({
      kind: 'note',
      note: { type: 'artifact-comment', threadId: 'c1', action: 'create', quote: '全场八折' },
    })
    expect(app.getConversation(thread.id).pendingSeq).toBeGreaterThan(0)
    expect(app.getArtifact(written.id)).toMatchObject({ openComments: 1 })

    app.commentOnArtifact(written.id, thread.id, {
      type: 'reply',
      threadId: 'c1',
      commentId: 'm2',
      body: '不叠加',
    })
    app.commentOnArtifact(written.id, thread.id, { type: 'resolve', threadId: 'c1' })
    const [comments] = app.listArtifactComments(written.id)
    expect(comments).toMatchObject({
      quote: '全场八折',
      comments: [
        { id: 'm1', author: 'user', body: '能和会员价叠加吗？' },
        { id: 'm2', author: thread.id, body: '不叠加' },
      ],
      resolved: { by: thread.id },
    })
    expect(app.getArtifact(written.id).openComments).toBe(0)
    expect(app.listNotices(thread.id).map((n) => n.kind)).toEqual([
      'artifact-comment',
      'artifact-comment',
    ])
    // Its own reply does not come back to it.
    expect(
      app
        .listTranscript(thread.id)
        .filter((e) => e.kind === 'note' && e.note.type === 'artifact-comment'),
    ).toHaveLength(1)
  })

  test('only an author changes a comment, and the first goes only with its thread', () => {
    const own = doc('user')
    const thread = app.createThread(projectId, { title: '写' })
    app.commentOnArtifact(own.id, 'user', {
      type: 'create',
      threadId: 'c1',
      commentId: 'm1',
      quote: '八折',
      body: '?',
    })
    expect(() =>
      app.commentOnArtifact(own.id, thread.id, {
        type: 'edit',
        threadId: 'c1',
        commentId: 'm1',
        body: '改',
      }),
    ).toThrow(DomainError)
    expect(() =>
      app.commentOnArtifact(own.id, 'user', { type: 'delete', threadId: 'c1', commentId: 'm1' }),
    ).toThrow(DomainError)
    expect(() =>
      app.commentOnArtifact(own.id, thread.id, { type: 'remove', threadId: 'c1' }),
    ).toThrow(DomainError)
    app.commentOnArtifact(own.id, 'user', { type: 'remove', threadId: 'c1' })
    expect(app.listArtifactComments(own.id)).toEqual([])
  })

  test('a save carries the commented text along as it now reads', () => {
    const own = doc('user')
    app.commentOnArtifact(own.id, 'user', {
      type: 'create',
      threadId: 'c1',
      commentId: 'm1',
      quote: '全场八折',
      body: '?',
    })
    app.reviseArtifact(own.id, {
      content: '# 方案\n\n开业当天全场八五折。\n',
      author: 'user',
      reason: '',
      quotes: { c1: '全场八五折', unknown: 'x' },
    })
    expect(app.listArtifactComments(own.id)[0]?.quote).toBe('全场八五折')
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
