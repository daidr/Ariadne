/** Legacy-only test oracle for the native backend migration; never used at runtime. */
import { Ariadne } from '@ariadne/core'
import { Database } from 'bun:sqlite'
import * as Y from 'yjs'

const [mode, path] = process.argv.slice(2)
if (!path || !['create', 'read'].includes(mode ?? '')) {
  throw new Error(
    'usage: bun packages/agent/src/testing/go-backend-fixture.ts create|read <temporary-database>',
  )
}
const app = new Ariadne({ path, now: () => 1_800_000_000_000 })
try {
  if (mode === 'create') {
    const project = app.createProject('旧数据库，含中文和空格')
    const thread = app.createThread(project.id, { title: '保留现有讨论串' })
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '保留原文 <tag>、emoji 🧵 和换行\n第二行。',
    })
    const artifact = app.createArtifact(project.id, {
      kind: 'document',
      title: '迁移前的文档',
      content: '# 原有产物\n\n这段文字需要保留。',
      author: thread.id,
    })
    app.commentOnArtifact(artifact.id, 'user', {
      type: 'create',
      threadId: 'comment-thread-fixture',
      commentId: 'comment-fixture',
      quote: '这段文字',
      body: '这条评论也要保留。',
    })
    const doc = new Y.Doc()
    try {
      doc.getText('compat').insert(0, '持久化协作状态 🧵')
      app.writeSharedState(artifact.id, {
        version: artifact.version,
        state: Y.encodeStateAsUpdate(doc),
      })
    } finally {
      doc.destroy()
    }
    app.saveFile(
      project.id,
      { mime: 'text/plain', data: new TextEncoder().encode('附件原文\n') },
      { text: true },
    )
  }
  const projects = app.listProjects()
  const db = new Database(path, { readonly: true })
  try {
    console.info(
      JSON.stringify({
        projects,
        events: app.events({ limit: 1000 }),
        exports: projects.map((project) => app.exportProject(project.id)),
        shared: db
          .query(
            'SELECT artifact_id, version, hex(state) AS bytes FROM artifact_shared ORDER BY artifact_id',
          )
          .all(),
        files: db.query('SELECT id, mime, sha256, hex(data) AS bytes FROM files ORDER BY id').all(),
        version: db.query<{ user_version: number }, []>('PRAGMA user_version').get()!.user_version,
      }),
    )
  } finally {
    db.close()
  }
} finally {
  app.close()
}
