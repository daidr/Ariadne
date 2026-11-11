import type { Database } from 'bun:sqlite'
import { DOCUMENT_BOARD, type DashboardContent, type WhiteboardContent } from './artifact-content'
import type { ArtifactKind } from './events'
import type { Projection } from './store'
import type { SearchHit } from './views'

/**
 * The project's words, for finding things again: what was said in its conversations,
 * its threads' titles and latest headlines, and its artifacts. `search_docs` says what each
 * row is; `search_text` holds the words under the same rowid, indexed by trigrams so
 * Chinese, which has no spaces between words, is found by any three characters in a row.
 */
export const searchProjection: Projection = {
  name: 'search',
  setup(db) {
    db.run(`CREATE TABLE IF NOT EXISTS search_docs (
      id INTEGER PRIMARY KEY,
      project_id TEXT NOT NULL,
      kind TEXT NOT NULL,
      ref_id TEXT NOT NULL UNIQUE,
      conversation_id TEXT,
      at INTEGER NOT NULL
    )`)
    db.run('CREATE INDEX IF NOT EXISTS search_docs_project ON search_docs (project_id, kind, at)')
    db.run(
      "CREATE VIRTUAL TABLE IF NOT EXISTS search_text USING fts5(title, body, tokenize = 'trigram')",
    )
  },
  forget(db, projectId) {
    db.run(
      'DELETE FROM search_text WHERE rowid IN (SELECT id FROM search_docs WHERE project_id = ?)',
      [projectId],
    )
    db.run('DELETE FROM search_docs WHERE project_id = ?', [projectId])
  },
  apply(db, event) {
    switch (event.type) {
      case 'message.posted':
        put(db, {
          projectId: event.projectId,
          kind: 'message',
          refId: event.data.messageId,
          conversationId: event.data.conversationId,
          at: event.at,
          title: '',
          body: event.data.content,
        })
        break
      case 'message.edited':
        change(db, event.data.messageId, { body: event.data.content })
        break
      case 'thread.created':
        put(db, {
          projectId: event.projectId,
          kind: 'thread',
          refId: event.data.threadId,
          conversationId: event.data.threadId,
          at: event.at,
          title: event.data.title,
          body: '',
        })
        break
      case 'thread.renamed': {
        const doc = db
          .query<{ id: number }, [string]>('SELECT id FROM search_docs WHERE ref_id = ?')
          .get(event.data.threadId)
        if (doc) {
          db.run('UPDATE search_text SET title = ? WHERE rowid = ?', [event.data.title, doc.id])
        }
        break
      }
      case 'thread.reported':
        change(db, event.data.threadId, { body: event.data.headline, at: event.at })
        break
      case 'artifact.created':
        put(db, {
          projectId: event.projectId,
          kind: 'artifact',
          refId: event.data.artifactId,
          conversationId: null,
          at: event.at,
          title: event.data.title,
          body: searchTextOf(event.data.kind, event.data.content),
        })
        break
      case 'artifact.revised': {
        const kind = db
          .query<{ kind: ArtifactKind }, [string]>('SELECT kind FROM artifacts WHERE id = ?')
          .get(event.data.artifactId)?.kind
        change(db, event.data.artifactId, {
          title: event.data.title,
          body: searchTextOf(kind ?? 'document', event.data.content),
          at: event.at,
        })
        break
      }
    }
  },
}

interface Doc {
  projectId: string
  kind: SearchHit['kind']
  refId: string
  conversationId: string | null
  at: number
  title: string
  body: string
}

function put(db: Database, doc: Doc) {
  const row = db
    .query<{ id: number }, [string, string, string, string | null, number]>(
      `INSERT INTO search_docs (project_id, kind, ref_id, conversation_id, at) VALUES (?, ?, ?, ?, ?)
       RETURNING id`,
    )
    .get(doc.projectId, doc.kind, doc.refId, doc.conversationId, doc.at)
  if (!row) return
  db.run('INSERT INTO search_text (rowid, title, body) VALUES (?, ?, ?)', [
    row.id,
    doc.title,
    doc.body,
  ])
}

function change(
  db: Database,
  refId: string,
  fields: { title?: string; body: string; at?: number },
) {
  const row = db
    .query<{ id: number }, [string]>('SELECT id FROM search_docs WHERE ref_id = ?')
    .get(refId)
  if (!row) return
  if (fields.at !== undefined) {
    db.run('UPDATE search_docs SET at = ? WHERE id = ?', [fields.at, row.id])
  }
  if (fields.title === undefined) {
    db.run('UPDATE search_text SET body = ? WHERE rowid = ?', [fields.body, row.id])
  } else {
    db.run('UPDATE search_text SET title = ?, body = ? WHERE rowid = ?', [
      fields.title,
      fields.body,
      row.id,
    ])
  }
}

/** The words of an artifact a person would look for: its text, not its structure. */
export function searchTextOf(kind: ArtifactKind, content: string): string {
  try {
    if (kind === 'whiteboard') return boardWords(JSON.parse(content) as WhiteboardContent)
    if (kind === 'dashboard') {
      const value = JSON.parse(content) as DashboardContent
      return value.dashboard.widgets
        .flatMap((widget) => [widget.title, widget.props.text, widget.props.content])
        .filter((text): text is string => typeof text === 'string')
        .join('\n')
    }
  } catch {
    return ''
  }
  // A whiteboard in a document is found by its words, not its data.
  return content.replace(DOCUMENT_BOARD, (fence, body: string) => {
    try {
      return boardWords((JSON.parse(body) as { board: WhiteboardContent }).board)
    } catch {
      return fence
    }
  })
}

function boardWords(board: WhiteboardContent): string {
  return board.nodes
    .flatMap((node) => [node.data?.text, node.data?.label])
    .filter((text): text is string => typeof text === 'string')
    .join('\n')
}

/** At most this many words of a query count; the rest are dropped. */
const MAX_TERMS = 8

/** The words of a query, as typed between spaces. */
export function searchTerms(query: string): string[] {
  return [...new Set(query.trim().split(/\s+/).filter(Boolean))].slice(0, MAX_TERMS)
}

/** How many of each kind come back. */
export const SEARCH_LIMITS = { thread: 5, artifact: 5, message: 30 } as const

interface HitRow {
  kind: SearchHit['kind']
  ref_id: string
  conversation_id: string | null
  at: number
  title: string
  body: string
}

/**
 * Find rows of the project holding every term, newest first. Terms of three characters or
 * more use the trigram index; shorter ones (common in Chinese: 分页) fall back to LIKE.
 */
export function searchRows(
  db: Database,
  projectId: string,
  kind: SearchHit['kind'],
  terms: string[],
  limit: number,
): HitRow[] {
  if (!terms.length) return []
  const long = terms.filter((term) => [...term].length >= 3)
  const short = terms.filter((term) => [...term].length < 3)
  const where = ['d.project_id = ?', 'd.kind = ?']
  const params: (string | number)[] = [projectId, kind]
  if (long.length) {
    where.push('search_text MATCH ?')
    params.push(long.map((term) => `"${term.replaceAll('"', '""')}"`).join(' AND '))
  }
  for (const term of short) {
    const like = `%${term.replace(/[\\%_]/g, (c) => `\\${c}`)}%`
    where.push("(t.title LIKE ? ESCAPE '\\' OR t.body LIKE ? ESCAPE '\\')")
    params.push(like, like)
  }
  params.push(limit)
  return db
    .query<HitRow, (string | number)[]>(
      `SELECT d.kind, d.ref_id, d.conversation_id, d.at, t.title, t.body
       FROM search_text t JOIN search_docs d ON d.id = t.rowid
       WHERE ${where.join(' AND ')}
       ORDER BY d.at DESC LIMIT ?`,
    )
    .all(...params)
}

/** Characters shown before the first match, and in all. */
const LEAD = 24
const SNIPPET = 120

/** Markdown as it reads: links by their words, without emphasis, heading or code marks. */
function plain(text: string): string {
  return text
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/^\s{0,3}(?:#{1,6}|>|[-+*]|\d+\.)\s+/gm, '')
    .replace(/(\*\*|__|`+|~~)/g, '')
    .replace(/\s+/g, ' ')
    .trim()
}

/** A line of `text` around the first term it holds, on one line. */
export function snippetOf(text: string, terms: string[]): string {
  const flat = plain(text)
  const lower = flat.toLowerCase()
  const found = terms
    .map((term) => lower.indexOf(term.toLowerCase()))
    .filter((at) => at >= 0)
    .toSorted((a, b) => a - b)[0]
  const start = found === undefined || found <= LEAD ? 0 : found - LEAD
  const end = Math.min(flat.length, start + SNIPPET)
  return `${start > 0 ? '…' : ''}${flat.slice(start, end)}${end < flat.length ? '…' : ''}`
}
