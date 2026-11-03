import type { Database } from 'bun:sqlite'
import type { AnyStoredEvent, PlanNodeFields, PlanOp } from './events'
import { applyOps, edgesOf, emptyGraph, type PlanGraph } from './plan'
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
  'plan_nodes',
  'plan_edges',
  'plan_revisions',
  'plan_proposals',
  'artifacts',
  'artifact_versions',
] as const

export const projectsProjection: Projection = {
  name: 'projects',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS projects (
      id TEXT PRIMARY KEY,
      name TEXT NOT NULL,
      coordinator_id TEXT NOT NULL,
      source TEXT,
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
      workspace_label TEXT,
      workspace_branch TEXT,
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
      case 'workspace.opened':
        db.run(
          'UPDATE threads SET workspace_label = ?, workspace_branch = ?, updated_at = ? WHERE id = ?',
          [event.data.label, event.data.branch, event.at, event.data.threadId],
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
    .query<{ title: string }, [string]>('SELECT title FROM plan_nodes WHERE id = ?')
    .get(source.node_id)
  const downstream = db
    .query<{ id: string }, [string, string]>(
      `SELECT t.id FROM threads t JOIN plan_edges e ON e.to_id = t.node_id
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
          false,
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
    if (!next.nodes.has(id)) db.run('DELETE FROM plan_nodes WHERE id = ?', [id])
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
         WHERE id = ?`,
        [
          node.title,
          node.description,
          node.acceptance,
          position,
          changed ? 1 : 0,
          revisionId,
          changed ? 1 : 0,
          at,
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
    db.run(`CREATE TABLE IF NOT EXISTS plan_nodes (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      kind TEXT NOT NULL,
      title TEXT NOT NULL,
      description TEXT NOT NULL,
      acceptance TEXT NOT NULL,
      position INTEGER NOT NULL,
      changed_in TEXT,
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL
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
  apply(db, event) {
    switch (event.type) {
      case 'plan.revised': {
        // A proposal that failed to apply stays visible until the plan changes again.
        db.run(
          "UPDATE plan_proposals SET status = 'expired' WHERE project_id = ? AND status = 'stale'",
          [event.projectId],
        )
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
        }
        if (event.data.proposalId) {
          db.run("UPDATE plan_proposals SET status = 'applied' WHERE id = ?", [
            event.data.proposalId,
          ])
        }
        break
      }
      case 'plan.proposed':
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
        // Any answer closes the proposal; the first option's `plan.revised` marks it applied.
        db.run(
          `UPDATE plan_proposals SET status = CASE WHEN ? = 0 THEN 'accepted' ELSE 'rejected' END
           WHERE attention_id = ? AND status = 'open'`,
          [event.data.choice, event.data.attentionId],
        )
        break
    }
  },
}

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
  apply(db, event) {
    switch (event.type) {
      case 'artifact.created': {
        const { artifactId, kind, title, nodeId, author, content } = event.data
        db.run(
          `INSERT INTO artifacts (id, project_id, kind, title, node_id, version, content, created_by, updated_by, last_agent, created_at, updated_at)
           VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)`,
          [
            artifactId,
            event.projectId,
            kind,
            title,
            nodeId,
            content,
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
             last_agent = CASE WHEN ? = 'user' THEN last_agent ELSE ? END
           WHERE id = ?`,
          [title, version, content, author, event.at, author, author, artifactId],
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

export const defaultProjections: Projection[] = [
  projectsProjection,
  threadsProjection,
  attentionsProjection,
  messagesProjection,
  todosProjection,
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
