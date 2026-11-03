import type { Database } from 'bun:sqlite'
import type { AnyStoredEvent } from './events'
import type { Projection } from './store'

/** Every projection table, so a rebuild can drop them. */
export const projectionTables = ['projects', 'threads', 'attentions', 'messages', 'todos'] as const

export const projectsProjection: Projection = {
  name: 'projects',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS projects (
      id TEXT PRIMARY KEY,
      name TEXT NOT NULL,
      coordinator_id TEXT NOT NULL,
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL
    )`)
  },
  apply(db, event) {
    switch (event.type) {
      case 'project.created':
        db.run(
          'INSERT INTO projects (id, name, coordinator_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?)',
          [event.projectId, event.data.name, event.data.coordinatorId, event.at, event.at],
        )
        break
      case 'project.renamed':
        db.run('UPDATE projects SET name = ?, updated_at = ? WHERE id = ?', [
          event.data.name,
          event.at,
          event.projectId,
        ])
        break
      default:
        // Any event of a project counts as activity.
        db.run('UPDATE projects SET updated_at = ? WHERE id = ?', [event.at, event.projectId])
    }
  },
}

export const threadsProjection: Projection = {
  name: 'threads',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS threads (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      title TEXT NOT NULL,
      node_id TEXT,
      started INTEGER NOT NULL DEFAULT 0,
      last_report TEXT,
      last_summary TEXT,
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL
    )`)
    db.run('CREATE INDEX IF NOT EXISTS threads_project ON threads (project_id, id)')
  },
  apply(db, event) {
    switch (event.type) {
      case 'thread.created':
        db.run(
          'INSERT INTO threads (id, project_id, title, node_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)',
          [
            event.data.threadId,
            event.projectId,
            event.data.title,
            event.data.nodeId,
            event.at,
            event.at,
          ],
        )
        break
      case 'thread.started':
        db.run('UPDATE threads SET started = 1, updated_at = ? WHERE id = ?', [
          event.at,
          event.data.threadId,
        ])
        break
      case 'thread.reported':
        db.run(
          'UPDATE threads SET last_report = ?, last_summary = ?, updated_at = ? WHERE id = ?',
          [event.data.report, event.data.summary, event.at, event.data.threadId],
        )
        break
    }
  },
}

export const attentionsProjection: Projection = {
  name: 'attentions',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS attentions (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      thread_id TEXT,
      question TEXT NOT NULL,
      options TEXT NOT NULL,
      recommended INTEGER,
      answered INTEGER NOT NULL DEFAULT 0,
      choice INTEGER,
      answer_text TEXT,
      created_at INTEGER NOT NULL
    )`)
    db.run('CREATE INDEX IF NOT EXISTS attentions_project ON attentions (project_id, answered)')
  },
  apply(db, event) {
    switch (event.type) {
      case 'attention.opened':
        db.run(
          'INSERT INTO attentions (id, project_id, thread_id, question, options, recommended, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)',
          [
            event.data.attentionId,
            event.projectId,
            event.data.threadId,
            event.data.question,
            JSON.stringify(event.data.options),
            event.data.recommended,
            event.at,
          ],
        )
        break
      case 'attention.answered':
        db.run('UPDATE attentions SET answered = 1, choice = ?, answer_text = ? WHERE id = ?', [
          event.data.choice,
          event.data.text,
          event.data.attentionId,
        ])
        break
    }
  },
}

export const messagesProjection: Projection = {
  name: 'messages',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS messages (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      conversation_id TEXT NOT NULL,
      author TEXT NOT NULL,
      kind TEXT NOT NULL,
      content TEXT NOT NULL,
      created_at INTEGER NOT NULL,
      edited_at INTEGER,
      edit_reason TEXT,
      read_at INTEGER,
      edited_since_read INTEGER NOT NULL DEFAULT 0
    )`)
    db.run('CREATE INDEX IF NOT EXISTS messages_conversation ON messages (conversation_id, id)')
  },
  apply(db, event) {
    switch (event.type) {
      case 'message.posted':
        db.run(
          'INSERT INTO messages (id, project_id, conversation_id, author, kind, content, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)',
          [
            event.data.messageId,
            event.projectId,
            event.data.conversationId,
            event.data.author,
            event.data.kind,
            event.data.content,
            event.at,
          ],
        )
        break
      case 'message.edited':
        db.run(
          `UPDATE messages SET content = ?, edited_at = ?, edit_reason = ?,
             edited_since_read = CASE WHEN read_at IS NULL THEN 0 ELSE 1 END
           WHERE id = ?`,
          [event.data.content, event.at, event.data.reason, event.data.messageId],
        )
        break
      case 'message.read':
        // Ids are time ordered, so `id <= upTo` is every message the user has seen.
        db.run(
          `UPDATE messages SET read_at = coalesce(read_at, ?), edited_since_read = 0
           WHERE conversation_id = ? AND id <= ? AND author != 'user'`,
          [event.at, event.data.conversationId, event.data.upTo],
        )
        break
    }
  },
}

export const todosProjection: Projection = {
  name: 'todos',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS todos (
      conversation_id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      items TEXT NOT NULL,
      updated_at INTEGER NOT NULL
    )`)
  },
  apply(db, event) {
    if (event.type !== 'todo.updated') return
    db.run(
      `INSERT INTO todos (conversation_id, project_id, items, updated_at) VALUES (?, ?, ?, ?)
       ON CONFLICT (conversation_id) DO UPDATE SET items = excluded.items, updated_at = excluded.updated_at`,
      [event.data.conversationId, event.projectId, JSON.stringify(event.data.items), event.at],
    )
  },
}

export const defaultProjections: Projection[] = [
  projectsProjection,
  threadsProjection,
  attentionsProjection,
  messagesProjection,
  todosProjection,
]

export function dropProjectionTables(db: Database): void {
  for (const table of projectionTables) db.run(`DROP TABLE IF EXISTS ${table}`)
}

export type { AnyStoredEvent }
