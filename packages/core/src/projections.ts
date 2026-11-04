import type { Database } from 'bun:sqlite'
import { previewOf } from './artifact-content'
import { askTitle, describeAnswer, type AskBlock, type BlockAnswer } from './blocks'
import type {
  AnyStoredEvent,
  ArtifactKind,
  IssueWriteAction,
  PlanNodeFields,
  PlanOp,
} from './events'
import { applyOps, edgesOf, emptyGraph, type PlanGraph } from './plan'
import type { Projection } from './store'

/** Every projection table, so a rebuild can drop them. */
export const projectionTables = [
  'projects',
  'threads',
  'attentions',
  'attention_waiters',
  'messages',
  'todos',
  'memories',
  'notices',
  'conversations',
  'transcript',
  'plan_nodes',
  'plan_edges',
  'plan_revisions',
  'plan_proposals',
  'plan_node_issues',
  'issue_writes',
  'mcp_calls',
  'artifacts',
  'artifact_versions',
  'prompts',
] as const

export const projectsProjection: Projection = {
  name: 'projects',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS projects (
      id TEXT PRIMARY KEY,
      name TEXT NOT NULL,
      coordinator_id TEXT NOT NULL,
      source TEXT,
      slots INTEGER,
      mcp TEXT NOT NULL DEFAULT '[]',
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL
    )`)
  },
  forget(db, projectId) {
    db.run('DELETE FROM projects WHERE id = ?', [projectId])
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
      case 'project.slots.set':
        db.run('UPDATE projects SET slots = ?, updated_at = ? WHERE id = ?', [
          event.data.slots,
          event.at,
          event.projectId,
        ])
        break
      case 'project.mcp.set':
        db.run('UPDATE projects SET mcp = ?, updated_at = ? WHERE id = ?', [
          JSON.stringify(event.data.servers),
          event.at,
          event.projectId,
        ])
        break
      case 'project.source.set':
        db.run('UPDATE projects SET source = ?, updated_at = ? WHERE id = ?', [
          event.data.source ? JSON.stringify(event.data.source) : null,
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
      last_headline TEXT,
      -- Ids of the artifacts it pointed the user to (JSON), shown by their current titles.
      last_artifacts TEXT NOT NULL DEFAULT '[]',
      reported_at INTEGER,
      reported_head TEXT,
      workspace_label TEXT,
      workspace_branch TEXT,
      workspace_base TEXT,
      workspace_removed INTEGER NOT NULL DEFAULT 0,
      merged TEXT,
      pull_request TEXT,
      check_failures INTEGER NOT NULL DEFAULT 0,
      stopped_reason TEXT,
      stopped_by TEXT,
      stopped_at INTEGER,
      model TEXT,
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL
    )`)
    db.run('CREATE INDEX IF NOT EXISTS threads_project ON threads (project_id, id)')
  },
  forget(db, projectId) {
    db.run('DELETE FROM threads WHERE project_id = ?', [projectId])
  },
  apply(db, event) {
    switch (event.type) {
      case 'thread.created':
        db.run(
          'INSERT INTO threads (id, project_id, title, node_id, model, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)',
          [
            event.data.threadId,
            event.projectId,
            event.data.title,
            event.data.nodeId,
            event.data.model,
            event.at,
            event.at,
          ],
        )
        break
      case 'thread.model_set':
        db.run('UPDATE threads SET model = ?, updated_at = ? WHERE id = ?', [
          event.data.model,
          event.at,
          event.data.threadId,
        ])
        break
      case 'thread.started':
        db.run('UPDATE threads SET started = 1, updated_at = ? WHERE id = ?', [
          event.at,
          event.data.threadId,
        ])
        break
      case 'thread.reported':
        db.run(
          `UPDATE threads SET last_report = ?, last_summary = ?, last_headline = ?, last_artifacts = ?,
           reported_at = ?, reported_head = ?, updated_at = ? WHERE id = ?`,
          [
            event.data.report,
            event.data.summary,
            event.data.headline,
            JSON.stringify(event.data.artifacts),
            event.at,
            event.data.head ?? null,
            event.at,
            event.data.threadId,
          ],
        )
        break
      case 'workspace.opened':
        db.run(
          `UPDATE threads SET workspace_label = ?, workspace_branch = ?, workspace_base = ?,
             workspace_removed = 0, updated_at = ? WHERE id = ?`,
          [event.data.label, event.data.branch, event.data.base, event.at, event.data.threadId],
        )
        break
      case 'workspace.removed':
        db.run('UPDATE threads SET workspace_removed = 1, updated_at = ? WHERE id = ?', [
          event.at,
          event.data.threadId,
        ])
        break
      case 'branch.merged': {
        const { threadId, ...merge } = event.data
        db.run('UPDATE threads SET merged = ?, updated_at = ? WHERE id = ?', [
          JSON.stringify({ ...merge, at: event.at }),
          event.at,
          threadId,
        ])
        break
      }
      case 'pull_request.synced': {
        const { threadId, ...pullRequest } = event.data
        // Passing checks end a run of failures.
        db.run(
          `UPDATE threads SET pull_request = ?1, updated_at = ?2,
             check_failures = CASE WHEN ?3 THEN 0 ELSE check_failures END
           WHERE id = ?4`,
          [JSON.stringify(pullRequest), event.at, pullRequest.checks.state === 'success', threadId],
        )
        break
      }
      case 'pull_request.closed':
        // Merged or closed, its checks are no longer the thread's to fix.
        db.run('UPDATE threads SET check_failures = 0 WHERE id = ?', [event.data.threadId])
        break
      case 'checks.failed':
        db.run('UPDATE threads SET check_failures = ? WHERE id = ?', [
          event.data.attempt ?? 1,
          event.data.threadId,
        ])
        break
      case 'message.posted':
        // The user stepping in gives the thread fresh tries (a stopped one is resumed by
        // its own event, appended with the message).
        if (event.data.author === 'user') {
          db.run('UPDATE threads SET check_failures = 0 WHERE id = ?', [event.data.conversationId])
        }
        break
      case 'thread.stopped':
        db.run(
          'UPDATE threads SET stopped_reason = ?, stopped_by = ?, stopped_at = ?, updated_at = ? WHERE id = ?',
          [event.data.reason, event.data.by, event.at, event.at, event.data.threadId],
        )
        break
      case 'thread.resumed':
        db.run(
          `UPDATE threads SET stopped_reason = NULL, stopped_by = NULL, stopped_at = NULL,
             updated_at = ? WHERE id = ?`,
          [event.at, event.data.threadId],
        )
        break
    }
  },
}

/** 1 or 0 for a confirmation's answer, `null` for the others. */
const approvedOf = (answer: BlockAnswer): number | null =>
  answer.type === 'confirm' ? (answer.approved ? 1 : 0) : null

export const attentionsProjection: Projection = {
  name: 'attentions',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS attentions (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      thread_id TEXT,
      conversation_id TEXT,
      message_id TEXT,
      block TEXT NOT NULL,
      closed INTEGER NOT NULL DEFAULT 0,
      answer TEXT,
      withdrawn TEXT,
      approved INTEGER,
      purpose TEXT,
      created_at INTEGER NOT NULL
    )`)
    db.run('CREATE INDEX IF NOT EXISTS attentions_project ON attentions (project_id, closed)')
    db.run('CREATE INDEX IF NOT EXISTS attentions_message ON attentions (message_id)')
    // The threads that hear an attention's answer: the one that asked and any that joined.
    db.run(`CREATE TABLE IF NOT EXISTS attention_waiters (
      attention_id TEXT NOT NULL,
      thread_id TEXT NOT NULL,
      PRIMARY KEY (attention_id, thread_id)
    )`)
    // The yeses to external tool calls that were used, each for one call.
    db.run(`CREATE TABLE IF NOT EXISTS mcp_calls (
      attention_id TEXT PRIMARY KEY,
      thread_id TEXT NOT NULL
    )`)
  },
  forget(db, projectId) {
    for (const table of ['attention_waiters', 'mcp_calls']) {
      db.run(
        `DELETE FROM ${table} WHERE attention_id IN (SELECT id FROM attentions WHERE project_id = ?)`,
        [projectId],
      )
    }
    db.run('DELETE FROM attentions WHERE project_id = ?', [projectId])
  },
  apply(db, event) {
    switch (event.type) {
      case 'attention.opened':
        db.run(
          `INSERT INTO attentions (id, project_id, thread_id, conversation_id, message_id, block,
             purpose, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
          [
            event.data.attentionId,
            event.projectId,
            event.data.threadId,
            event.data.threadId ?? coordinatorOf(db, event.projectId),
            event.data.messageId,
            JSON.stringify(event.data.block),
            event.data.purpose ?? null,
            event.at,
          ],
        )
        break
      case 'attention.joined':
        db.run('INSERT OR IGNORE INTO attention_waiters (attention_id, thread_id) VALUES (?, ?)', [
          event.data.attentionId,
          event.data.threadId,
        ])
        break
      case 'attention.answered':
        // `approved` is kept apart for confirmations, which standing approvals look up.
        db.run('UPDATE attentions SET closed = 1, answer = ?, approved = ? WHERE id = ?', [
          JSON.stringify(event.data.answer),
          approvedOf(event.data.answer),
          event.data.attentionId,
        ])
        break
      case 'attention.withdrawn':
        db.run('UPDATE attentions SET closed = 1, withdrawn = ? WHERE id = ?', [
          event.data.reason,
          event.data.attentionId,
        ])
        break
      case 'mcp.called':
        db.run('INSERT OR IGNORE INTO mcp_calls (attention_id, thread_id) VALUES (?, ?)', [
          event.data.attentionId,
          event.data.threadId,
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
      original_content TEXT,
      read_at INTEGER,
      edited_since_read INTEGER NOT NULL DEFAULT 0,
      blocks TEXT,
      attachments TEXT
    )`)
    db.run('CREATE INDEX IF NOT EXISTS messages_conversation ON messages (conversation_id, id)')
  },
  forget(db, projectId) {
    db.run('DELETE FROM messages WHERE project_id = ?', [projectId])
  },
  apply(db, event) {
    switch (event.type) {
      case 'message.posted':
        db.run(
          `INSERT INTO messages (id, project_id, conversation_id, author, kind, content, blocks,
             attachments, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
          [
            event.data.messageId,
            event.projectId,
            event.data.conversationId,
            event.data.author,
            event.data.kind,
            event.data.content,
            event.data.blocks?.length ? JSON.stringify(event.data.blocks) : null,
            event.data.attachments?.length ? JSON.stringify(event.data.attachments) : null,
            event.at,
          ],
        )
        break
      case 'message.edited':
        db.run(
          `UPDATE messages SET content = ?, edited_at = ?, edit_reason = ?,
             original_content = coalesce(original_content, content),
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
  forget(db, projectId) {
    db.run('DELETE FROM todos WHERE project_id = ?', [projectId])
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

/** The project's memory: its standing preferences and facts. */
/**
 * Notices shown in line with a conversation's messages. Applied before the memories
 * projection, so a change still sees what the entry said before it.
 */
export const noticesProjection: Projection = {
  name: 'notices',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS notices (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      conversation_id TEXT NOT NULL,
      kind TEXT NOT NULL,
      payload TEXT NOT NULL,
      undone INTEGER NOT NULL DEFAULT 0,
      created_at INTEGER NOT NULL
    )`)
    db.run(
      'CREATE INDEX IF NOT EXISTS notices_conversation ON notices (conversation_id, created_at)',
    )
  },
  forget(db, projectId) {
    db.run('DELETE FROM notices WHERE project_id = ?', [projectId])
  },
  apply(db, event) {
    const add = (conversationId: string, kind: string, payload: unknown) =>
      db.run(
        'INSERT INTO notices (id, project_id, conversation_id, kind, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)',
        [
          `ntc_${event.seq}`,
          event.projectId,
          conversationId,
          kind,
          JSON.stringify(payload),
          event.at,
        ],
      )
    switch (event.type) {
      case 'memory.saved':
      case 'memory.removed': {
        const { memoryId, author, undoes } = event.data
        if (undoes) db.run('UPDATE notices SET undone = 1 WHERE id = ?', [undoes])
        // The user's own changes are in front of them already; an agent's are shown where
        // it made them.
        if (author === 'user') break
        const before =
          db
            .query<{ text: string }, [string]>('SELECT text FROM memories WHERE id = ?')
            .get(memoryId)?.text ?? null
        if (event.type === 'memory.saved') {
          add(author, 'memory', {
            action: 'saved',
            memoryId,
            text: event.data.text,
            previous: before,
          })
        } else {
          add(author, 'memory', {
            action: 'removed',
            memoryId,
            text: before ?? '',
            previous: before,
          })
        }
        break
      }
      case 'conversation.compaction_failed':
        add(event.data.conversationId, 'compaction-failed', { detail: event.data.detail })
        break
      case 'plan.issue_linked':
      case 'plan.issue_unlinked': {
        // What a thread reads follows from these links; an agent's link is shown where it
        // made it, so the user can see who pointed which work at which issue.
        if (event.data.by === 'user') break
        const node = db
          .query<{ title: string }, [string, string]>(
            'SELECT title FROM plan_nodes WHERE project_id = ? AND id = ?',
          )
          .get(event.projectId, event.data.nodeId)
        const linked = event.type === 'plan.issue_linked'
        add(event.data.by, 'issue-linked', {
          nodeId: event.data.nodeId,
          nodeTitle: node?.title ?? event.data.nodeId,
          tracker: linked ? event.data.issue.tracker : event.data.tracker,
          number: linked ? event.data.issue.number : event.data.number,
          linked,
        })
        break
      }
      case 'issue.written': {
        // A change made outside Ariadne, in the user's name: shown where the thread made it.
        const { threadId, tracker, number, action, text, url } = event.data
        add(threadId, 'issue-written', { tracker, number, action, text, url })
        break
      }
      case 'conversation.instructions':
        // The user sees what the thread follows, and when it moves.
        add(event.data.conversationId, 'instructions', {
          path: event.data.path,
          content: event.data.content,
          changed: event.data.changed,
          uncommitted: event.data.uncommitted ?? false,
        })
        break
      // What happened to the thread's branch and worktree, where the user follows the thread.
      case 'branch.merged':
        add(event.data.threadId, 'merged', {
          base: event.data.base,
          method: event.data.method,
          sha: event.data.sha,
        })
        break
      case 'branch.conflicted':
        add(event.data.threadId, 'merge-conflict', {
          base: event.data.base,
          files: event.data.files,
        })
        break
      case 'workspace.removed':
        add(event.data.threadId, 'worktree-removed', { reason: event.data.reason })
        break
      case 'conversation.model_retry':
        add(event.data.conversationId, 'model-retry', {
          attempt: event.data.attempt,
          delayMs: event.data.delayMs,
          detail: event.data.detail,
        })
        break
      case 'notice.dismissed':
        db.run('UPDATE notices SET undone = 1 WHERE id = ?', [event.data.noticeId])
        break
    }
  },
}

export const memoriesProjection: Projection = {
  name: 'memories',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS memories (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      text TEXT NOT NULL,
      author TEXT NOT NULL,
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL
    )`)
    db.run('CREATE INDEX IF NOT EXISTS memories_project ON memories (project_id, created_at)')
  },
  forget(db, projectId) {
    db.run('DELETE FROM memories WHERE project_id = ?', [projectId])
  },
  apply(db, event) {
    if (event.type === 'memory.saved') {
      db.run(
        `INSERT INTO memories (id, project_id, text, author, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
         ON CONFLICT (id) DO UPDATE SET text = excluded.text, author = excluded.author, updated_at = excluded.updated_at`,
        [
          event.data.memoryId,
          event.projectId,
          event.data.text,
          event.data.author,
          event.at,
          event.at,
        ],
      )
    } else if (event.type === 'memory.removed') {
      db.run('DELETE FROM memories WHERE id = ?', [event.data.memoryId])
    }
  },
}

/** Longer status lines are cut: it sits on one line above the input. */
const MAX_ACTIVITY = 60

/**
 * The line a model wrote beside its tool calls says what it is doing; it is never posted,
 * so it becomes the conversation's status while the turn runs. Only its first line counts.
 */
export function activityOf(text: string): string | null {
  const line = text.trim().split('\n', 1)[0]!.trim()
  if (!line) return null
  const chars = [...line]
  return chars.length > MAX_ACTIVITY ? `${chars.slice(0, MAX_ACTIVITY).join('')}…` : line
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
      output_tokens INTEGER NOT NULL DEFAULT 0,
      cached_tokens INTEGER NOT NULL DEFAULT 0,
      cache_write_tokens INTEGER NOT NULL DEFAULT 0,
      reasoning_tokens INTEGER NOT NULL DEFAULT 0,
      context_tokens INTEGER NOT NULL DEFAULT 0,
      instructions TEXT,
      activity TEXT
    )`)
  },
  forget(db, projectId) {
    db.run('DELETE FROM conversations WHERE project_id = ?', [projectId])
  },
  apply(db, event) {
    switch (event.type) {
      case 'conversation.instructions':
        db.run('UPDATE conversations SET instructions = ? WHERE id = ?', [
          JSON.stringify({
            path: event.data.path,
            hash: event.data.hash,
            content: event.data.content,
          }),
          event.data.conversationId,
        ])
        break
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
          `UPDATE conversations SET turn_id = ?, turn_started_at = ?, turn_started_seq = ?, model = ?,
             activity = NULL
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
             input_tokens = input_tokens + ?2, output_tokens = output_tokens + ?3,
             cached_tokens = cached_tokens + ?7, cache_write_tokens = cache_write_tokens + ?8,
             reasoning_tokens = reasoning_tokens + ?9,
             context_tokens = CASE WHEN ?5 THEN ?2 ELSE context_tokens END,
             activity = coalesce(?6, activity)
           WHERE id = ?4`,
          [
            event.data.sawSeq,
            event.data.usage?.input ?? 0,
            event.data.usage?.output ?? 0,
            event.data.conversationId,
            // What the model read last is how full its context is; unknown keeps the last.
            event.data.usage !== null,
            event.data.finish === 'tool-calls' ? activityOf(event.data.text) : null,
            event.data.usage?.cached ?? 0,
            event.data.usage?.cacheWrite ?? 0,
            event.data.usage?.reasoning ?? 0,
          ],
        )
        break
      case 'conversation.compacted':
        db.run(
          `UPDATE conversations SET input_tokens = input_tokens + ?, output_tokens = output_tokens + ?,
             cached_tokens = cached_tokens + ?, cache_write_tokens = cache_write_tokens + ?,
             reasoning_tokens = reasoning_tokens + ?
           WHERE id = ?`,
          [
            event.data.usage?.input ?? 0,
            event.data.usage?.output ?? 0,
            event.data.usage?.cached ?? 0,
            event.data.usage?.cacheWrite ?? 0,
            event.data.usage?.reasoning ?? 0,
            event.data.conversationId,
          ],
        )
        break
      case 'turn.finished': {
        const { outcome, error, conversationId } = event.data
        const result = [outcome, error ? JSON.stringify(error) : null, conversationId]
        const end = `turn_id = NULL, turn_started_at = NULL, activity = NULL, last_outcome = ?, last_error = ?`
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

/** The threads on a card: the one that asked, then those that joined it. */
function cardThreads(db: Database, attentionId: string): string[] {
  const asker = db
    .query<{ thread_id: string | null }, [string]>('SELECT thread_id FROM attentions WHERE id = ?')
    .get(attentionId)?.thread_id
  const waiters = db
    .query<{ thread_id: string }, [string]>(
      'SELECT thread_id FROM attention_waiters WHERE attention_id = ? ORDER BY rowid',
    )
    .all(attentionId)
    .map((w) => w.thread_id)
  return [...new Set([...(asker ? [asker] : []), ...waiters])]
}

function addEntry(
  db: Database,
  event: AnyStoredEvent,
  entry: { conversationId: string; kind: TranscriptKind; ref?: string; payload?: unknown },
  wake: boolean,
) {
  db.run(
    'INSERT INTO transcript (project_id, conversation_id, seq, kind, ref, payload) VALUES (?, ?, ?, ?, ?, ?)',
    [
      event.projectId,
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

/**
 * A thread on a plan node finished: threads working on nodes that depend on it get its
 * summary, so downstream work builds on what upstream produced.
 */
function upstreamDone(
  db: Database,
  event: AnyStoredEvent,
  data: { threadId: string; summary: string },
) {
  const source = db
    .query<{ node_id: string | null; title: string }, [string]>(
      'SELECT node_id, title FROM threads WHERE id = ?',
    )
    .get(data.threadId)
  if (!source?.node_id) return
  const node = db
    .query<{ title: string }, [string, string]>(
      'SELECT title FROM plan_nodes WHERE project_id = ? AND id = ?',
    )
    .get(event.projectId, source.node_id)
  const downstream = db
    .query<{ id: string }, [string, string]>(
      `SELECT t.id FROM threads t
       JOIN plan_edges e ON e.project_id = t.project_id AND e.to_id = t.node_id
       WHERE e.from_id = ? AND t.project_id = ? AND coalesce(t.last_report, '') != 'done'`,
    )
    .all(source.node_id, event.projectId)
  for (const { id } of downstream) {
    addEntry(
      db,
      event,
      {
        conversationId: id,
        kind: 'note',
        payload: {
          type: 'upstream-done',
          threadId: data.threadId,
          nodeTitle: node?.title ?? source.title,
          summary: data.summary,
        },
      },
      true,
    )
  }
}

/** Failed check runs in a row a thread fixes on its own before it must stop and report. */
export const MAX_CHECK_ATTEMPTS = 3

export type TranscriptKind = 'message' | 'note' | 'step' | 'tool' | 'summary'

/**
 * What each conversation's agent has seen, in order: the model's input is rebuilt from
 * here on every step. Messages are referenced, so an edit shows up in later turns.
 */
export const transcriptProjection: Projection = {
  name: 'transcript',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS transcript (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      project_id TEXT NOT NULL,
      conversation_id TEXT NOT NULL,
      seq INTEGER NOT NULL,
      kind TEXT NOT NULL,
      ref TEXT,
      payload TEXT
    )`)
    db.run('CREATE INDEX IF NOT EXISTS transcript_conversation ON transcript (conversation_id, id)')
  },
  forget(db, projectId) {
    db.run('DELETE FROM transcript WHERE project_id = ?', [projectId])
  },
  apply(db, event) {
    switch (event.type) {
      case 'message.posted': {
        const { conversationId, author, kind, messageId, content } = event.data
        if (author === 'user') {
          // The conversation's own questions still waiting: the user may be answering one
          // in words, and the agent may then withdraw it.
          const openAsks = db
            .query<{ id: string; block: string }, [string]>(
              `SELECT id, block FROM attentions
               WHERE conversation_id = ? AND closed = 0 AND message_id IS NOT NULL ORDER BY id`,
            )
            .all(conversationId)
            .map((row) => ({
              attentionId: row.id,
              title: askTitle(JSON.parse(row.block) as AskBlock),
            }))
          addEntry(
            db,
            event,
            {
              conversationId,
              kind: 'message',
              ref: messageId,
              ...(openAsks.length ? { payload: { openAsks } } : {}),
            },
            true,
          )
        } else if (kind === 'relay') {
          addEntry(db, event, { conversationId, kind: 'message', ref: messageId }, true)
        }
        // What the user tells a thread also gets a quick look from the coordinator, which
        // may hand it to a better place. The thread does not wait for that.
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
            true,
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
        if (event.data.report === 'done') upstreamDone(db, event, event.data)
        break
      }
      case 'checks.failed':
        // Past the limit the thread is no longer woken: it has been told to stop and report.
        addEntry(
          db,
          event,
          {
            conversationId: event.data.threadId,
            kind: 'note',
            payload: { type: 'checks-failed', ...event.data },
          },
          (event.data.attempt ?? 1) <= MAX_CHECK_ATTEMPTS,
        )
        break
      case 'conversation.instructions': {
        // The first version needs no note: it is in the instructions from the start. A
        // change is noted where it happened, so the model knows the rules moved.
        if (event.data.changed) {
          const payload = {
            type: 'instructions-changed',
            path: event.data.path,
            removed: event.data.content === null,
          }
          addEntry(
            db,
            event,
            { conversationId: event.data.conversationId, kind: 'note', payload },
            false,
          )
        }
        break
      }
      case 'thread.stopped': {
        // The coordinator hears of a stop the user made, so it does not count on the thread.
        const coordinator = coordinatorOf(db, event.projectId)
        if (coordinator && event.data.by === 'user') {
          const payload = {
            type: 'thread-stopped-by-user',
            threadId: event.data.threadId,
            reason: event.data.reason,
          }
          addEntry(db, event, { conversationId: coordinator, kind: 'note', payload }, false)
        }
        break
      }
      case 'thread.resumed': {
        // The thread learns why it had been held and goes on; the coordinator hears of a
        // resume it did not do itself.
        const payload = { type: 'thread-resumed', ...event.data }
        addEntry(db, event, { conversationId: event.data.threadId, kind: 'note', payload }, true)
        const coordinator = coordinatorOf(db, event.projectId)
        if (coordinator && event.data.by !== coordinator) {
          addEntry(db, event, { conversationId: coordinator, kind: 'note', payload }, false)
        }
        break
      }
      case 'pull_request.closed': {
        const coordinator = coordinatorOf(db, event.projectId)
        const payload = { type: 'pull-request-closed', ...event.data }
        addEntry(db, event, { conversationId: event.data.threadId, kind: 'note', payload }, false)
        if (coordinator) {
          addEntry(db, event, { conversationId: coordinator, kind: 'note', payload }, true)
        }
        break
      }
      case 'branch.merged': {
        // The thread learns it next time it works; the coordinator moves the plan on now.
        const payload = { type: 'branch-merged', ...event.data }
        addEntry(db, event, { conversationId: event.data.threadId, kind: 'note', payload }, false)
        const coordinator = coordinatorOf(db, event.projectId)
        if (coordinator) {
          addEntry(db, event, { conversationId: coordinator, kind: 'note', payload }, true)
        }
        break
      }
      case 'branch.conflicted':
        // Handed back to the thread: it brings the base in and resolves the conflicts.
        addEntry(
          db,
          event,
          {
            conversationId: event.data.threadId,
            kind: 'note',
            payload: { type: 'merge-conflict', ...event.data },
          },
          true,
        )
        break
      case 'artifact.revised': {
        // The agent that last wrote the document learns the user changed it, next turn.
        // The artifacts projection has already applied this edit, which leaves the last
        // agent in place.
        const row = db
          .query<{ last_agent: string | null; title: string }, [string]>(
            'SELECT last_agent, title FROM artifacts WHERE id = ?',
          )
          .get(event.data.artifactId)
        if (!row?.last_agent || event.data.author !== 'user') break
        addEntry(
          db,
          event,
          {
            conversationId: row.last_agent,
            kind: 'note',
            payload: {
              type: 'artifact-edited',
              artifactId: event.data.artifactId,
              title: row.title,
              version: event.data.version,
            },
          },
          false,
        )
        break
      }
      case 'plan.proposal.stale': {
        const coordinator = coordinatorOf(db, event.projectId)
        const proposal = db
          .query<{ reason: string }, [string]>('SELECT reason FROM plan_proposals WHERE id = ?')
          .get(event.data.proposalId)
        if (!coordinator || !proposal) break
        addEntry(
          db,
          event,
          {
            conversationId: coordinator,
            kind: 'note',
            payload: {
              type: 'proposal-stale',
              proposalId: event.data.proposalId,
              reason: proposal.reason,
              staleBecause: event.data.reason,
            },
          },
          true,
        )
        break
      }
      case 'plan.revised': {
        const coordinator = coordinatorOf(db, event.projectId)
        if (!coordinator || event.data.author !== 'user') break
        addEntry(
          db,
          event,
          {
            conversationId: coordinator,
            kind: 'note',
            payload: {
              type: 'plan-changed-by-user',
              revisionId: event.data.revisionId,
              revertOf: event.data.revertOf,
              reason: event.data.reason,
            },
          },
          // An undo says the coordinator's change was not wanted: it looks again now.
          !!event.data.revertOf,
        )
        break
      }
      case 'attention.answered': {
        const row = db
          .query<{ conversation_id: string | null; block: string }, [string]>(
            'SELECT conversation_id, block FROM attentions WHERE id = ?',
          )
          .get(event.data.attentionId)
        const asker = row?.conversation_id
        if (!row || !asker) break
        const block = JSON.parse(row.block) as AskBlock
        const waiters = db
          .query<{ thread_id: string }, [string]>(
            'SELECT thread_id FROM attention_waiters WHERE attention_id = ? ORDER BY rowid',
          )
          .all(event.data.attentionId)
          .map((w) => w.thread_id)
        for (const conversationId of new Set([asker, ...waiters])) {
          addEntry(
            db,
            event,
            {
              conversationId,
              kind: 'note',
              payload: {
                type: 'answer',
                attentionId: event.data.attentionId,
                question: askTitle(block),
                answer: describeAnswer(block, event.data.answer),
              },
            },
            true,
          )
        }
        break
      }
      case 'issue.written':
      case 'issue.write_unsure': {
        // One thread made the change with a yes the card's other threads share: they hear
        // how it went, so none of them makes it again or waits on it for nothing.
        const write = db
          .query<
            { thread_id: string; tracker: string; number: number; action: IssueWriteAction },
            [string]
          >('SELECT thread_id, tracker, number, action FROM issue_writes WHERE attention_id = ?')
          .get(event.data.attentionId)
        // Logs from before who was told was recorded: nobody else heard of it then either.
        if (!write || (event.type === 'issue.write_unsure' && !event.data.threadId)) break
        const told = event.type === 'issue.written' ? write.thread_id : event.data.threadId
        for (const conversationId of cardThreads(db, event.data.attentionId)) {
          // The one that made it, or was told to check, learns it from its own tool result.
          if (conversationId === told) continue
          addEntry(
            db,
            event,
            {
              conversationId,
              kind: 'note',
              payload: {
                type: 'issue-write-shared',
                threadId: write.thread_id,
                tracker: write.tracker,
                number: write.number,
                action: write.action,
                outcome: event.type === 'issue.written' ? 'made' : 'unsure',
                url: event.type === 'issue.written' ? event.data.url : null,
              },
            },
            true,
          )
        }
        break
      }
      case 'conversation.compacted':
        addEntry(
          db,
          event,
          {
            conversationId: event.data.conversationId,
            kind: 'summary',
            payload: {
              summary: event.data.summary,
              upTo: event.data.upTo,
              reasoning: event.data.reasoning,
              call: event.data.call,
            },
          },
          false,
        )
        break
      case 'turn.step':
        addEntry(
          db,
          event,
          {
            conversationId: event.data.conversationId,
            kind: 'step',
            payload: {
              text: event.data.text,
              reasoning: event.data.reasoning,
              call: event.data.input.call,
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
              ...(event.data.files?.length ? { files: event.data.files } : {}),
            },
          },
          false,
        )
        break
    }
  },
}

/** Read a project's plan graph from the projection tables. */
export function loadPlanGraph(db: Database, projectId: string): PlanGraph {
  const graph = emptyGraph()
  const nodes = db
    .query<PlanNodeFields & { id: string }, [string]>(
      'SELECT id, kind, title, description, acceptance FROM plan_nodes WHERE project_id = ? ORDER BY position',
    )
    .all(projectId)
  for (const { id, ...fields } of nodes) {
    graph.nodes.set(id, fields)
    graph.order.push(id)
  }
  const edges = db
    .query<{ from_id: string; to_id: string }, [string]>(
      'SELECT from_id, to_id FROM plan_edges WHERE project_id = ?',
    )
    .all(projectId)
  for (const edge of edges) graph.edges.add(`${edge.from_id}>${edge.to_id}`)
  return graph
}

/** Replace a project's stored graph with `next`, stamping changed nodes with the revision. */
function storePlanGraph(
  db: Database,
  projectId: string,
  before: PlanGraph,
  next: PlanGraph,
  revisionId: string,
  at: number,
) {
  for (const id of before.order) {
    if (!next.nodes.has(id)) {
      db.run('DELETE FROM plan_nodes WHERE project_id = ? AND id = ?', [projectId, id])
    }
  }
  next.order.forEach((id, position) => {
    const node = next.nodes.get(id) as PlanNodeFields
    const old = before.nodes.get(id)
    const changed =
      !old ||
      old.title !== node.title ||
      old.description !== node.description ||
      old.acceptance !== node.acceptance
    if (!old) {
      db.run(
        `INSERT INTO plan_nodes (id, project_id, kind, title, description, acceptance, position, changed_in, created_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        [
          id,
          projectId,
          node.kind,
          node.title,
          node.description,
          node.acceptance,
          position,
          revisionId,
          at,
          at,
        ],
      )
    } else {
      db.run(
        `UPDATE plan_nodes SET title = ?, description = ?, acceptance = ?, position = ?,
           changed_in = CASE WHEN ? THEN ? ELSE changed_in END,
           updated_at = CASE WHEN ? THEN ? ELSE updated_at END
         WHERE project_id = ? AND id = ?`,
        [
          node.title,
          node.description,
          node.acceptance,
          position,
          changed ? 1 : 0,
          revisionId,
          changed ? 1 : 0,
          at,
          projectId,
          id,
        ],
      )
    }
  })
  db.run('DELETE FROM plan_edges WHERE project_id = ?', [projectId])
  for (const { from, to } of edgesOf(next)) {
    db.run('INSERT INTO plan_edges (project_id, from_id, to_id, changed_in) VALUES (?, ?, ?, ?)', [
      projectId,
      from,
      to,
      before.edges.has(`${from}>${to}`) ? null : revisionId,
    ])
  }
}

/**
 * The plan graph and its history. Each revision keeps the ops that undo it, computed
 * against the graph it was applied to, so any revision can be reverted while those ops
 * still apply.
 */
export const planProjection: Projection = {
  name: 'plan',
  setup(db) {
    // Node ids are the plan's own names ("login", "a"): unique within a project only.
    db.run(`CREATE TABLE IF NOT EXISTS plan_nodes (
      id TEXT NOT NULL,
      project_id TEXT NOT NULL,
      kind TEXT NOT NULL,
      title TEXT NOT NULL,
      description TEXT NOT NULL,
      acceptance TEXT NOT NULL,
      position INTEGER NOT NULL,
      changed_in TEXT,
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL,
      PRIMARY KEY (project_id, id)
    )`)
    db.run('CREATE INDEX IF NOT EXISTS plan_nodes_project ON plan_nodes (project_id, position)')
    db.run(`CREATE TABLE IF NOT EXISTS plan_edges (
      project_id TEXT NOT NULL,
      from_id TEXT NOT NULL,
      to_id TEXT NOT NULL,
      changed_in TEXT,
      PRIMARY KEY (project_id, from_id, to_id)
    )`)
    db.run(`CREATE TABLE IF NOT EXISTS plan_revisions (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      author TEXT NOT NULL,
      reason TEXT NOT NULL,
      ops TEXT NOT NULL,
      inverse TEXT NOT NULL,
      revert_of TEXT,
      reverted_by TEXT,
      proposal_id TEXT,
      seq INTEGER NOT NULL,
      created_at INTEGER NOT NULL
    )`)
    db.run('CREATE INDEX IF NOT EXISTS plan_revisions_project ON plan_revisions (project_id, seq)')
    db.run(`CREATE TABLE IF NOT EXISTS plan_node_issues (
      project_id TEXT NOT NULL,
      node_id TEXT NOT NULL,
      tracker TEXT NOT NULL,
      number INTEGER NOT NULL,
      title TEXT NOT NULL,
      url TEXT NOT NULL,
      linked_by TEXT NOT NULL,
      seq INTEGER NOT NULL,
      PRIMARY KEY (project_id, node_id, tracker, number)
    )`)
    // Which yes was used for which change: `writing` until it is known to have gone through.
    // The thread making it, and what it is, so the others who waited on the card hear of it.
    db.run(`CREATE TABLE IF NOT EXISTS issue_writes (
      attention_id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      purpose TEXT NOT NULL,
      thread_id TEXT NOT NULL,
      tracker TEXT NOT NULL,
      number INTEGER NOT NULL,
      action TEXT NOT NULL,
      status TEXT NOT NULL,
      url TEXT,
      written_seq INTEGER
    )`)
    db.run(`CREATE TABLE IF NOT EXISTS plan_proposals (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      attention_id TEXT NOT NULL UNIQUE,
      reason TEXT NOT NULL,
      ops TEXT NOT NULL,
      revert_of TEXT,
      status TEXT NOT NULL,
      stale_reason TEXT,
      created_at INTEGER NOT NULL
    )`)
  },
  forget(db, projectId) {
    for (const table of [
      'plan_nodes',
      'plan_edges',
      'plan_revisions',
      'plan_node_issues',
      'issue_writes',
      'plan_proposals',
    ]) {
      db.run(`DELETE FROM ${table} WHERE project_id = ?`, [projectId])
    }
  },
  apply(db, event) {
    switch (event.type) {
      case 'plan.revised': {
        const before = loadPlanGraph(db, event.projectId)
        // Commands validate before appending; a revision that no longer applies is skipped
        // rather than corrupting the graph.
        let result: { graph: PlanGraph; inverse: PlanOp[] }
        try {
          result = applyOps(before, event.data.ops)
        } catch {
          break
        }
        storePlanGraph(db, event.projectId, before, result.graph, event.data.revisionId, event.at)
        // A removed node takes its issue links with it; adding it back starts without them.
        for (const id of before.nodes.keys()) {
          if (result.graph.nodes.has(id)) continue
          db.run('DELETE FROM plan_node_issues WHERE project_id = ? AND node_id = ?', [
            event.projectId,
            id,
          ])
        }
        db.run(
          `INSERT INTO plan_revisions (id, project_id, author, reason, ops, inverse, revert_of, proposal_id, seq, created_at)
           VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
          [
            event.data.revisionId,
            event.projectId,
            event.data.author,
            event.data.reason,
            JSON.stringify(event.data.ops),
            JSON.stringify(result.inverse),
            event.data.revertOf,
            event.data.proposalId,
            event.seq,
            event.at,
          ],
        )
        if (event.data.revertOf) {
          db.run('UPDATE plan_revisions SET reverted_by = ? WHERE id = ?', [
            event.data.revisionId,
            event.data.revertOf,
          ])
          // Undoing an undo restores the change it undid: that change stands again.
          db.run(
            `UPDATE plan_revisions SET reverted_by = NULL
             WHERE id = (SELECT revert_of FROM plan_revisions WHERE id = ?)`,
            [event.data.revertOf],
          )
        }
        if (event.data.proposalId) {
          db.run("UPDATE plan_proposals SET status = 'applied' WHERE id = ?", [
            event.data.proposalId,
          ])
        }
        break
      }
      case 'plan.issue_linked': {
        const { nodeId, issue } = event.data
        db.run(
          `INSERT INTO plan_node_issues (project_id, node_id, tracker, number, title, url, linked_by, seq)
           VALUES (?, ?, ?, ?, ?, ?, ?, ?)
           ON CONFLICT DO UPDATE SET title = excluded.title, url = excluded.url`,
          [
            event.projectId,
            nodeId,
            issue.tracker,
            issue.number,
            issue.title,
            issue.url,
            event.data.by,
            event.seq,
          ],
        )
        break
      }
      case 'plan.issue_unlinked':
        db.run(
          'DELETE FROM plan_node_issues WHERE project_id = ? AND node_id = ? AND tracker = ? AND number = ?',
          [event.projectId, event.data.nodeId, event.data.tracker, event.data.number],
        )
        break
      case 'issue.writing':
      case 'issue.written': {
        const { attentionId, purpose, threadId, tracker, number, action } = event.data
        const written = event.type === 'issue.written'
        db.run(
          `INSERT INTO issue_writes
             (attention_id, project_id, purpose, thread_id, tracker, number, action, status, url,
              written_seq)
           VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
           ON CONFLICT DO ${
             written
               ? "UPDATE SET status = 'written', url = excluded.url, written_seq = excluded.written_seq"
               : 'NOTHING'
           }`,
          [
            attentionId,
            event.projectId,
            purpose,
            threadId,
            tracker,
            number,
            action,
            written ? 'written' : 'writing',
            written ? event.data.url : null,
            written ? event.seq : null,
          ],
        )
        break
      }
      case 'issue.write_unsure':
        db.run("UPDATE issue_writes SET status = 'unsure' WHERE attention_id = ?", [
          event.data.attentionId,
        ])
        break
      case 'plan.proposal.dismissed':
        db.run("UPDATE plan_proposals SET status = 'expired' WHERE id = ? AND status = 'stale'", [
          event.data.proposalId,
        ])
        break
      case 'plan.proposed':
        // A proposal that failed to apply stays visible until the user dismisses it or the
        // coordinator proposes again.
        db.run(
          "UPDATE plan_proposals SET status = 'expired' WHERE project_id = ? AND status = 'stale'",
          [event.projectId],
        )
        db.run(
          `INSERT INTO plan_proposals (id, project_id, attention_id, reason, ops, revert_of, status, created_at)
           VALUES (?, ?, ?, ?, ?, ?, 'open', ?)`,
          [
            event.data.proposalId,
            event.projectId,
            event.data.attentionId,
            event.data.reason,
            JSON.stringify(event.data.ops),
            event.data.revertOf,
            event.at,
          ],
        )
        break
      case 'plan.proposal.stale':
        db.run("UPDATE plan_proposals SET status = 'stale', stale_reason = ? WHERE id = ?", [
          event.data.reason,
          event.data.proposalId,
        ])
        break
      case 'attention.answered':
        // Any answer closes the proposal; agreeing's `plan.revised` marks it applied.
        db.run(
          `UPDATE plan_proposals SET status = CASE WHEN ? = 1 THEN 'accepted' ELSE 'rejected' END
           WHERE attention_id = ? AND status = 'open'`,
          [approvedOf(event.data.answer), event.data.attentionId],
        )
        break
    }
  },
}

const artifactKind = (db: Database, artifactId: string): ArtifactKind =>
  db
    .query<{ kind: ArtifactKind }, [string]>('SELECT kind FROM artifacts WHERE id = ?')
    .get(artifactId)?.kind ?? 'document'

/** Artifacts and every version of them. The newest version is copied into `artifacts`. */
export const artifactsProjection: Projection = {
  name: 'artifacts',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS artifacts (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      kind TEXT NOT NULL,
      title TEXT NOT NULL,
      node_id TEXT,
      version INTEGER NOT NULL,
      content TEXT NOT NULL,
      -- The summary lists show (JSON), worked out once per version rather than per list.
      preview TEXT NOT NULL,
      created_by TEXT NOT NULL,
      updated_by TEXT NOT NULL,
      -- The agent that wrote it last; it hears about the user's edits.
      last_agent TEXT,
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL
    )`)
    db.run('CREATE INDEX IF NOT EXISTS artifacts_project ON artifacts (project_id, updated_at)')
    db.run(`CREATE TABLE IF NOT EXISTS artifact_versions (
      artifact_id TEXT NOT NULL,
      version INTEGER NOT NULL,
      title TEXT NOT NULL,
      content TEXT NOT NULL,
      author TEXT NOT NULL,
      reason TEXT NOT NULL,
      created_at INTEGER NOT NULL,
      PRIMARY KEY (artifact_id, version)
    )`)
  },
  forget(db, projectId) {
    db.run(
      'DELETE FROM artifact_versions WHERE artifact_id IN (SELECT id FROM artifacts WHERE project_id = ?)',
      [projectId],
    )
    db.run('DELETE FROM artifacts WHERE project_id = ?', [projectId])
  },
  apply(db, event) {
    switch (event.type) {
      case 'artifact.created': {
        const { artifactId, kind, title, nodeId, author, content } = event.data
        db.run(
          `INSERT INTO artifacts (id, project_id, kind, title, node_id, version, content, preview, created_by, updated_by, last_agent, created_at, updated_at)
           VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)`,
          [
            artifactId,
            event.projectId,
            kind,
            title,
            nodeId,
            content,
            JSON.stringify(previewOf(kind, content)),
            author,
            author,
            author === 'user' ? null : author,
            event.at,
            event.at,
          ],
        )
        db.run(
          `INSERT INTO artifact_versions (artifact_id, version, title, content, author, reason, created_at)
           VALUES (?, 1, ?, ?, ?, '', ?)`,
          [artifactId, title, content, author, event.at],
        )
        break
      }
      case 'artifact.revised': {
        const { artifactId, version, title, content, author, reason } = event.data
        db.run(
          `UPDATE artifacts SET title = ?, version = ?, content = ?, updated_by = ?, updated_at = ?,
             preview = ?, last_agent = CASE WHEN ? = 'user' THEN last_agent ELSE ? END
           WHERE id = ?`,
          [
            title,
            version,
            content,
            author,
            event.at,
            JSON.stringify(previewOf(artifactKind(db, artifactId), content)),
            author,
            author,
            artifactId,
          ],
        )
        db.run(
          `INSERT INTO artifact_versions (artifact_id, version, title, content, author, reason, created_at)
           VALUES (?, ?, ?, ?, ?, ?, ?)`,
          [artifactId, version, title, content, author, reason, event.at],
        )
        break
      }
    }
  },
}

/** Model input stored once by its hash, see `prompt.recorded`. */
export const promptsProjection: Projection = {
  name: 'prompts',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS prompts (
      project_id TEXT NOT NULL,
      hash TEXT NOT NULL,
      content TEXT NOT NULL,
      PRIMARY KEY (project_id, hash)
    )`)
  },
  forget(db, projectId) {
    db.run('DELETE FROM prompts WHERE project_id = ?', [projectId])
  },
  apply(db, event) {
    if (event.type !== 'prompt.recorded') return
    db.run('INSERT OR IGNORE INTO prompts (project_id, hash, content) VALUES (?, ?, ?)', [
      event.projectId,
      event.data.hash,
      event.data.content,
    ])
  },
}

export const defaultProjections: Projection[] = [
  promptsProjection,
  projectsProjection,
  threadsProjection,
  attentionsProjection,
  messagesProjection,
  todosProjection,
  noticesProjection,
  memoriesProjection,
  conversationsProjection,
  planProjection,
  artifactsProjection,
  // Last: it reads plan edges and attention rows written above for the same event.
  transcriptProjection,
]

export function dropProjectionTables(db: Database): void {
  for (const table of projectionTables) db.run(`DROP TABLE IF EXISTS ${table}`)
}

export type { AnyStoredEvent }
