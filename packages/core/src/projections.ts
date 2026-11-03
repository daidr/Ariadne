import type { Database } from 'bun:sqlite'
import type { AnyStoredEvent } from './events'
import type { Projection } from './store'

/** Every projection table, so a rebuild can drop them. */
export const projectionTables = [
  'projects',
  'threads',
  'attentions',
  'messages',
  'todos',
  'conversations',
  'transcript',
] as const

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

/**
 * One agent per conversation: the project's coordinator or a thread. Tracks whether new
 * input waits for a turn (`pending_seq`), the running turn, and how the last one ended.
 */
export const conversationsProjection: Projection = {
  name: 'conversations',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS conversations (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      role TEXT NOT NULL,
      pending_seq INTEGER NOT NULL DEFAULT 0,
      pending_since INTEGER NOT NULL DEFAULT 0,
      turn_id TEXT,
      turn_started_seq INTEGER,
      turn_started_at INTEGER,
      model TEXT,
      last_outcome TEXT,
      last_error TEXT,
      input_tokens INTEGER NOT NULL DEFAULT 0,
      output_tokens INTEGER NOT NULL DEFAULT 0
    )`)
  },
  apply(db, event) {
    switch (event.type) {
      case 'project.created':
        db.run("INSERT INTO conversations (id, project_id, role) VALUES (?, ?, 'coordinator')", [
          event.data.coordinatorId,
          event.projectId,
        ])
        break
      case 'thread.created':
        db.run("INSERT INTO conversations (id, project_id, role) VALUES (?, ?, 'thread')", [
          event.data.threadId,
          event.projectId,
        ])
        break
      case 'turn.started':
        db.run(
          `UPDATE conversations SET turn_id = ?, turn_started_at = ?, turn_started_seq = ?, model = ?
           WHERE id = ?`,
          [event.data.turnId, event.at, event.seq, event.data.model, event.data.conversationId],
        )
        break
      case 'turn.step':
        db.run(
          // Input up to `sawSeq` is handled; anything newer still waits, and is at least that new.
          `UPDATE conversations SET
             pending_since = CASE WHEN pending_seq <= ?1 THEN 0 ELSE max(pending_since, ?1 + 1) END,
             pending_seq = CASE WHEN pending_seq <= ?1 THEN 0 ELSE pending_seq END,
             input_tokens = input_tokens + ?2, output_tokens = output_tokens + ?3
           WHERE id = ?4`,
          [
            event.data.sawSeq,
            event.data.usage?.input ?? 0,
            event.data.usage?.output ?? 0,
            event.data.conversationId,
          ],
        )
        break
      case 'turn.finished': {
        const { outcome, error, conversationId } = event.data
        const result = [outcome, error ? JSON.stringify(error) : null, conversationId]
        const end = `turn_id = NULL, turn_started_at = NULL, last_outcome = ?, last_error = ?`
        if (outcome === 'interrupted') {
          // Resumes when the server starts again.
          db.run(
            `UPDATE conversations SET ${end}, pending_seq = ?,
               pending_since = CASE WHEN pending_since = 0 THEN ? ELSE pending_since END
             WHERE id = ?`,
            [outcome, result[1] ?? null, event.seq, event.seq, conversationId],
          )
        } else if (outcome === 'completed') {
          db.run(`UPDATE conversations SET ${end} WHERE id = ?`, result)
        } else {
          // A failed or stopped turn does not retry what started it, but input that arrived
          // during the turn was never seen and still gets its turn.
          db.run(
            `UPDATE conversations SET ${end},
               pending_since = CASE WHEN pending_seq > turn_started_seq
                 THEN max(pending_since, turn_started_seq + 1) ELSE 0 END,
               pending_seq = CASE WHEN pending_seq > turn_started_seq THEN pending_seq ELSE 0 END
             WHERE id = ?`,
            result,
          )
        }
        break
      }
      case 'turn.requested':
        db.run(
          `UPDATE conversations SET pending_seq = ?1, last_error = NULL,
             pending_since = CASE WHEN pending_since = 0 THEN ?1 ELSE pending_since END
           WHERE id = ?2`,
          [event.seq, event.data.conversationId],
        )
        break
    }
  },
}

function coordinatorOf(db: Database, projectId: string): string | null {
  return (
    db
      .query<{ coordinator_id: string }, [string]>(
        'SELECT coordinator_id FROM projects WHERE id = ?',
      )
      .get(projectId)?.coordinator_id ?? null
  )
}

function addEntry(
  db: Database,
  event: AnyStoredEvent,
  entry: { conversationId: string; kind: TranscriptKind; ref?: string; payload?: unknown },
  wake: boolean,
) {
  db.run(
    'INSERT INTO transcript (conversation_id, seq, kind, ref, payload) VALUES (?, ?, ?, ?, ?)',
    [
      entry.conversationId,
      event.seq,
      entry.kind,
      entry.ref ?? null,
      entry.payload === undefined ? null : JSON.stringify(entry.payload),
    ],
  )
  if (wake) {
    db.run(
      `UPDATE conversations SET pending_seq = ?1,
         pending_since = CASE WHEN pending_since = 0 THEN ?1 ELSE pending_since END
       WHERE id = ?2`,
      [event.seq, entry.conversationId],
    )
  }
}

export type TranscriptKind = 'message' | 'note' | 'step' | 'tool'

/**
 * What each conversation's agent has seen, in order: the model's input is rebuilt from
 * here on every step. Messages are referenced, so an edit shows up in later turns.
 */
export const transcriptProjection: Projection = {
  name: 'transcript',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS transcript (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      conversation_id TEXT NOT NULL,
      seq INTEGER NOT NULL,
      kind TEXT NOT NULL,
      ref TEXT,
      payload TEXT
    )`)
    db.run('CREATE INDEX IF NOT EXISTS transcript_conversation ON transcript (conversation_id, id)')
  },
  apply(db, event) {
    switch (event.type) {
      case 'message.posted': {
        const { conversationId, author, kind, messageId, content } = event.data
        if (author === 'user' || kind === 'relay') {
          addEntry(db, event, { conversationId, kind: 'message', ref: messageId }, true)
        }
        // The coordinator keeps an eye on what the user tells a thread, without a turn.
        const coordinator = coordinatorOf(db, event.projectId)
        if (author === 'user' && coordinator && coordinator !== conversationId) {
          addEntry(
            db,
            event,
            {
              conversationId: coordinator,
              kind: 'note',
              payload: { type: 'user-in-thread', threadId: conversationId, content },
            },
            false,
          )
        }
        break
      }
      case 'thread.reported': {
        const coordinator = coordinatorOf(db, event.projectId)
        if (!coordinator) break
        addEntry(
          db,
          event,
          { conversationId: coordinator, kind: 'note', payload: { type: 'report', ...event.data } },
          event.data.report !== 'progress',
        )
        break
      }
      case 'attention.answered': {
        const row = db
          .query<{ thread_id: string | null; question: string; options: string }, [string]>(
            'SELECT thread_id, question, options FROM attentions WHERE id = ?',
          )
          .get(event.data.attentionId)
        const asker = row?.thread_id ?? coordinatorOf(db, event.projectId)
        if (!row || !asker) break
        const options = JSON.parse(row.options) as { label: string }[]
        const choice = event.data.choice === null ? null : options[event.data.choice]?.label
        addEntry(
          db,
          event,
          {
            conversationId: asker,
            kind: 'note',
            payload: {
              type: 'answer',
              attentionId: event.data.attentionId,
              question: row.question,
              choice: choice ?? null,
              text: event.data.text,
            },
          },
          true,
        )
        break
      }
      case 'turn.step':
        addEntry(
          db,
          event,
          {
            conversationId: event.data.conversationId,
            kind: 'step',
            payload: {
              text: event.data.text,
              toolCalls: event.data.toolCalls,
              sawSeq: event.data.sawSeq,
            },
          },
          false,
        )
        break
      case 'tool.finished':
        addEntry(
          db,
          event,
          {
            conversationId: event.data.conversationId,
            kind: 'tool',
            payload: {
              toolCallId: event.data.toolCallId,
              name: event.data.name,
              content: event.data.content,
              isError: event.data.isError,
            },
          },
          false,
        )
        break
    }
  },
}

export const defaultProjections: Projection[] = [
  projectsProjection,
  threadsProjection,
  attentionsProjection,
  messagesProjection,
  todosProjection,
  conversationsProjection,
  transcriptProjection,
]

export function dropProjectionTables(db: Database): void {
  for (const table of projectionTables) db.run(`DROP TABLE IF EXISTS ${table}`)
}

export type { AnyStoredEvent }
