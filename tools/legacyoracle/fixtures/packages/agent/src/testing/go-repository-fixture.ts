/** Whole-projection replay oracle, using an isolated database supplied by a test. */
import { Ariadne } from '@ariadne/core'
import { projectionTables } from '../../../core/src/projections'
import { confirmBlock } from '../../../core/src/blocks'
import * as Y from 'yjs'

const { mode, path } = (await Bun.stdin.json()) as {
  mode: 'create' | 'read' | 'rebuild'
  path: string
}
const now = 1_800_000_010_000
const app = new Ariadne({ path, now: () => now })
try {
  if (mode === 'create') {
    const p = app.createProject('迁移集成测试', { message: '请开始处理。' })
    const initialMessage = app.listMessages(p.coordinatorId)[0]!
    app.editMessage(
      initialMessage.id,
      { author: 'user', conversationId: p.coordinatorId },
      '请开始处理，并补上测试。',
      '补充要求',
    )
    const other = app.createProject('另一个项目')
    app.revisePlan(p.id, {
      author: 'user',
      reason: '分解工作',
      ops: [
        { op: 'add', id: 'a', kind: 'task', title: '实现', description: '', acceptance: '' },
        { op: 'add', id: 'b', kind: 'task', title: '集成', description: '', acceptance: '' },
        { op: 'link', from: 'a', to: 'b' },
      ],
    })
    const author = app.createThread(p.id, {
      title: '实现功能',
      nodeId: 'a',
      model: 'model_a',
      branch: 'feature',
    })
    const downstream = app.createThread(p.id, { title: '后续集成', nodeId: 'b', auto: true })
    app.startThread(author.id)
    app.recordWorkspace(author.id, { label: 'temporary', branch: 'ariadne/feature', base: 'main' })
    app.postMessage(p.id, {
      conversationId: author.id,
      author: 'coordinator',
      kind: 'relay',
      content: '实现功能并测试。',
    })
    const message = app.postMessage(p.id, {
      conversationId: author.id,
      author: 'thread',
      content: '开始处理。',
    })
    app.markRead(author.id, message.id)
    app.editMessage(
      message.id,
      { author: 'thread', conversationId: author.id },
      '已经开始处理。',
      '补充',
    )
    app.updateTodo(p.id, author.id, [
      { label: '实现', status: 'done' },
      { label: '测试', status: 'doing' },
    ])
    app.saveMemory(p.id, { author: author.id, text: '使用中文回答。' })
    const artifact = app.createArtifact(p.id, {
      kind: 'document',
      title: '实现说明',
      author: author.id,
      content: '# 说明\n\n原文 🧵',
    })
    app.commentOnArtifact(artifact.id, 'user', {
      type: 'create',
      threadId: 'comment_fixture',
      commentId: 'item_fixture',
      quote: '原文',
      body: '补充测试说明。',
    })
    const revised = app.reviseArtifact(artifact.id, {
      author: 'user',
      content: '# 说明\n\n原文 🧵\n用户补充',
      reason: '补充',
      quotes: { comment_fixture: '原文 🧵' },
    })
    const doc = new Y.Doc()
    try {
      doc.getText('compat').insert(0, '共享状态')
      app.writeSharedState(artifact.id, {
        version: revised.version,
        state: Y.encodeStateAsUpdate(doc),
      })
    } finally {
      doc.destroy()
    }
    app.saveFile(
      p.id,
      { mime: 'text/plain', data: new TextEncoder().encode('附件原文') },
      { text: true },
    )
    app.saveFile(
      other.id,
      { mime: 'text/plain', data: new TextEncoder().encode('其他项目') },
      { text: true },
    )
    const turn = app.startTurn(author.id, 'model_a')
    app.recordStep(author.id, turn, {
      text: '准备完成',
      reasoning: [],
      toolCalls: [],
      sawSeq: app.lastSeq(),
      usage: { input: 100, output: 20, cached: 80, cost: { amount: 0.003, currency: 'USD' } },
      input: { system: '规则', tools: '[]', call: '{}', images: true },
      timing: { startedAt: now, firstOutputAt: now },
    })
    const ready = app.reportThread(
      author.id,
      { kind: 'ready', summary: '实现完成', headline: '准备交付', artifacts: [artifact.id] },
      'head_a',
      { reviewable: true, head: 'head_a', reviewerTitle: '审查实现' },
    )
    app.finishTurn(author.id, turn, 'completed')
    app.submitReview(ready.review!.reviewerId, {
      verdict: 'pass',
      summary: '检查通过',
      headline: '检查通过',
      comments: [],
    })
    app.completeThread(author.id)
    app.setProjectDelivery(p.id, { mode: 'external', instructions: '向集成平台提票' })
    app.reportThread(downstream.id, { kind: 'done', summary: '已交付', headline: '已完成' })
    app.setAside(downstream.id, { days: 1, reason: '等待验证', by: 'user' })
    app.linkIssue(
      p.id,
      'a',
      { tracker: 'owner/repo', number: 1, title: '功能提票', url: 'https://example.com/1' },
      'user',
    )
    app.revisePlan(p.id, {
      author: 'user',
      reason: '交付节点',
      ops: [
        {
          op: 'add',
          id: 'm',
          kind: 'milestone',
          title: '交付',
          description: '',
          acceptance: '验收通过',
        },
      ],
    })
    app.askGate(p.id, 'm', confirmBlock({ title: '是否继续交付？' }))
    app.revisePlan(p.id, {
      author: 'coordinator',
      reason: '补充集成验收',
      ops: [{ op: 'update', id: 'b', acceptance: '集成平台通过' }],
      confirmation: { card: () => confirmBlock({ title: '调整验收？' }) },
    })
  }
  if (mode === 'rebuild') app.rebuildProjections()
  const tables = Object.fromEntries(
    projectionTables.map((table) => [
      table,
      app.store.db
        .query(`SELECT ${table === 'search_text' ? 'rowid, ' : ''}* FROM ${table} ORDER BY rowid`)
        .all(),
    ]),
  )
  const projects = app.listProjects()
  const conversations = app.store.db
    .query<{ id: string }, []>('SELECT id FROM conversations ORDER BY id')
    .all()
  const editSeq = app.events().find((event) => event.type === 'message.edited')!.seq
  const reads = conversations.map(({ id }) => {
    const messages = app.listMessages(id)
    const pages = [
      app.pageMessages(id),
      app.pageMessages(id, { limit: 1 }),
      app.pageMessages(id, { since: editSeq - 1 }),
    ]
    if (messages.length) {
      pages.push(
        app.pageMessages(id, { from: messages[0]!.id }),
        app.pageMessages(id, { before: messages.at(-1)!.id, limit: 1 }),
      )
    }
    return {
      conversation: app.getConversation(id),
      messages,
      pages,
      transcript: app.listTranscript(id),
      beforeEdit: app.listTranscript(id, { upTo: editSeq - 1 }),
      afterEdit: app.listTranscript(id, { upTo: editSeq }),
      recent: app.listTranscript(id, { after: editSeq }),
      changed: app.messagesChangedSince(id, editSeq - 1),
      todo: app.getTodo(id),
      notices: app.listNotices(id),
      instructions: app.getInstructions(id),
    }
  })
  const projectReads = projects.map((project) => ({
    plan: app.getPlan(project.id),
    artifacts: app.listArtifacts(project.id),
    search: ['说明', '原文', '用户', ''].map((query) => app.search(project.id, query)),
    needs: app.listNeedsMe(project.id),
    attentions: app.listOpenAttentions(project.id),
    gates: { mayAsk: app.mayAskGate(project.id, 'm'), stale: app.staleGates(project.id) },
    threads: app
      .listThreads(project.id)
      .map((thread) => ({ id: thread.id, reviews: app.listReviews(thread.id) })),
    artifactReads: app.listArtifacts(project.id).map((artifact) => ({
      current: app.getArtifact(artifact.id),
      old: app.getArtifact(artifact.id, 1),
      versions: app.listArtifactVersions(artifact.id),
      comments: app.listArtifactComments(artifact.id),
      feedback: app.listArtifactFeedback(artifact.id),
    })),
  }))
  console.info(
    JSON.stringify({
      tables,
      projects,
      details: projects.map((p) => app.getProjectDetail(p.id)),
      events: app.events({ limit: 10_000 }),
      reads,
      pending: app.listPendingConversations(),
      running: app.listRunningConversations(),
      projectReads,
      now,
      files: app.store.db
        .query('SELECT id, project_id, mime, sha256, hex(data) AS bytes FROM files ORDER BY id')
        .all(),
      shared: app.store.db
        .query(
          'SELECT artifact_id, version, hex(state) AS bytes FROM artifact_shared ORDER BY artifact_id',
        )
        .all(),
    }),
  )
} finally {
  app.close()
}
