import type { Database } from 'bun:sqlite'
import { DomainError } from './errors'
import type {
  AnyStoredEvent,
  AttentionOption,
  Author,
  MessageKind,
  ReportKind,
  TodoItem,
} from './events'
import { newId } from './ids'
import { defaultProjections, dropProjectionTables } from './projections'
import { needsUser, threadStage } from './stage'
import { EventStore, type EventListener } from './store'
import type { AttentionView, MessageView, ProjectView, ThreadView, TodoView } from './views'

interface ProjectRow {
  id: string
  name: string
  coordinator_id: string
  created_at: number
  updated_at: number
}

interface ThreadRow {
  id: string
  project_id: string
  title: string
  node_id: string | null
  started: number
  last_report: ReportKind | null
  last_summary: string | null
  created_at: number
  updated_at: number
  open_attentions: number
}

interface AttentionRow {
  id: string
  project_id: string
  thread_id: string | null
  question: string
  options: string
  recommended: number | null
  created_at: number
}

interface MessageRow {
  id: string
  conversation_id: string
  author: Author
  kind: MessageKind
  content: string
  created_at: number
  edited_at: number | null
  edit_reason: string | null
  edited_since_read: number
}

const THREAD_SELECT = `SELECT t.*, (
  SELECT count(*) FROM attentions a WHERE a.thread_id = t.id AND a.answered = 0
) AS open_attentions FROM threads t`

function toThread(row: ThreadRow): ThreadView {
  return {
    id: row.id,
    projectId: row.project_id,
    title: row.title,
    nodeId: row.node_id,
    stage: threadStage({
      openAttentions: row.open_attentions,
      started: row.started === 1,
      lastReport: row.last_report,
    }),
    lastReport: row.last_report,
    lastSummary: row.last_summary,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  }
}

function toAttention(row: AttentionRow): AttentionView {
  return {
    id: row.id,
    projectId: row.project_id,
    threadId: row.thread_id,
    question: row.question,
    options: JSON.parse(row.options),
    recommended: row.recommended,
    createdAt: row.created_at,
  }
}

function toMessage(row: MessageRow): MessageView {
  return {
    id: row.id,
    conversationId: row.conversation_id,
    author: row.author,
    kind: row.kind,
    content: row.content,
    createdAt: row.created_at,
    editedAt: row.edited_at,
    editReason: row.edit_reason,
    editedSinceRead: row.edited_since_read === 1,
  }
}

export interface AriadneOptions {
  /** SQLite file path; `:memory:` for tests. */
  path: string
  now?: () => number
}

/**
 * The domain facade: commands validate and append events, queries read projections.
 * Nothing else writes to the database.
 */
export class Ariadne {
  readonly store: EventStore
  private readonly db: Database

  constructor(options: AriadneOptions) {
    this.store = new EventStore({
      path: options.path,
      projections: defaultProjections,
      ...(options.now ? { now: options.now } : {}),
    })
    this.db = this.store.db
  }

  subscribe(listener: EventListener): () => void {
    return this.store.subscribe(listener)
  }

  events(options: { afterSeq?: number; projectId?: string; limit?: number } = {}) {
    return this.store.read(options)
  }

  rebuildProjections(): void {
    this.store.rebuild(dropProjectionTables)
  }

  close(): void {
    this.store.close()
  }

  // ---- queries ----

  listProjects(): ProjectView[] {
    return this.db
      .query<ProjectRow, []>('SELECT * FROM projects ORDER BY updated_at DESC')
      .all()
      .map((row) => this.toProject(row))
  }

  getProject(projectId: string): ProjectView {
    const row = this.db
      .query<ProjectRow, [string]>('SELECT * FROM projects WHERE id = ?')
      .get(projectId)
    if (!row) throw new DomainError('error.notFound', { what: projectId })
    return this.toProject(row)
  }

  listThreads(projectId: string): ThreadView[] {
    return this.db
      .query<ThreadRow, [string]>(`${THREAD_SELECT} WHERE t.project_id = ? ORDER BY t.id`)
      .all(projectId)
      .map(toThread)
  }

  getThread(threadId: string): ThreadView {
    const row = this.db.query<ThreadRow, [string]>(`${THREAD_SELECT} WHERE t.id = ?`).get(threadId)
    if (!row) throw new DomainError('error.notFound', { what: threadId })
    return toThread(row)
  }

  listOpenAttentions(projectId: string): AttentionView[] {
    return this.db
      .query<AttentionRow, [string]>(
        'SELECT * FROM attentions WHERE project_id = ? AND answered = 0 ORDER BY id',
      )
      .all(projectId)
      .map(toAttention)
  }

  listMessages(conversationId: string): MessageView[] {
    return this.db
      .query<MessageRow, [string]>('SELECT * FROM messages WHERE conversation_id = ? ORDER BY id')
      .all(conversationId)
      .map(toMessage)
  }

  getTodo(conversationId: string): TodoView {
    const row = this.db
      .query<{ items: string; updated_at: number }, [string]>(
        'SELECT items, updated_at FROM todos WHERE conversation_id = ?',
      )
      .get(conversationId)
    return {
      conversationId,
      items: row ? JSON.parse(row.items) : [],
      updatedAt: row?.updated_at ?? 0,
    }
  }

  // ---- commands ----

  createProject(name: string): ProjectView {
    const trimmed = name.trim()
    if (!trimmed) throw new DomainError('error.invalidBody', { reason: 'name' })
    const projectId = newId('prj')
    this.store.append([
      { type: 'project.created', projectId, data: { name: trimmed, coordinatorId: newId('crd') } },
    ])
    return this.getProject(projectId)
  }

  renameProject(projectId: string, name: string): ProjectView {
    this.getProject(projectId)
    const trimmed = name.trim()
    if (!trimmed) throw new DomainError('error.invalidBody', { reason: 'name' })
    this.store.append([{ type: 'project.renamed', projectId, data: { name: trimmed } }])
    return this.getProject(projectId)
  }

  createThread(projectId: string, input: { title: string; nodeId?: string | null }): ThreadView {
    this.getProject(projectId)
    const threadId = newId('thr')
    this.store.append([
      {
        type: 'thread.created',
        projectId,
        data: { threadId, title: input.title.trim(), nodeId: input.nodeId ?? null },
      },
    ])
    return this.getThread(threadId)
  }

  startThread(threadId: string): ThreadView {
    const thread = this.getThread(threadId)
    this.store.append([{ type: 'thread.started', projectId: thread.projectId, data: { threadId } }])
    return this.getThread(threadId)
  }

  reportThread(threadId: string, report: ReportKind, summary: string): ThreadView {
    const thread = this.getThread(threadId)
    this.store.append([
      { type: 'thread.reported', projectId: thread.projectId, data: { threadId, report, summary } },
    ])
    return this.getThread(threadId)
  }

  openAttention(
    projectId: string,
    input: {
      threadId?: string | null
      question: string
      options: AttentionOption[]
      recommended?: number | null
    },
  ): AttentionView {
    this.getProject(projectId)
    if (input.threadId) this.assertThreadIn(projectId, input.threadId)
    const recommended = input.recommended ?? null
    if (recommended !== null && !input.options[recommended]) {
      throw new DomainError('error.invalidBody', { reason: 'recommended' })
    }
    const attentionId = newId('att')
    this.store.append([
      {
        type: 'attention.opened',
        projectId,
        data: {
          attentionId,
          threadId: input.threadId ?? null,
          question: input.question,
          options: input.options,
          recommended,
        },
      },
    ])
    const view = this.listOpenAttentions(projectId).find((a) => a.id === attentionId)
    if (!view) throw new Error('attention projection missing')
    return view
  }

  answerAttention(attentionId: string, answer: { choice?: number | null; text?: string | null }) {
    const row = this.db
      .query<AttentionRow & { answered: number }, [string]>('SELECT * FROM attentions WHERE id = ?')
      .get(attentionId)
    if (!row || row.answered === 1) throw new DomainError('error.notFound', { what: attentionId })
    const choice = answer.choice ?? null
    if (choice !== null && !toAttention(row).options[choice]) {
      throw new DomainError('error.invalidBody', { reason: 'choice' })
    }
    this.store.append([
      {
        type: 'attention.answered',
        projectId: row.project_id,
        data: { attentionId, choice, text: answer.text ?? null },
      },
    ])
  }

  postMessage(
    projectId: string,
    input: { conversationId: string; author: Author; content: string; kind?: MessageKind },
  ): MessageView {
    this.assertConversationIn(projectId, input.conversationId)
    const kind = input.kind ?? 'chat'
    // Only the coordinator relays into a thread.
    if (kind === 'relay' && input.author !== 'coordinator') {
      throw new DomainError('error.invalidBody', { reason: 'kind' })
    }
    const messageId = newId('msg')
    this.store.append([
      {
        type: 'message.posted',
        projectId,
        data: {
          messageId,
          conversationId: input.conversationId,
          author: input.author,
          kind,
          content: input.content,
        },
      },
    ])
    return this.getMessage(messageId)
  }

  /** Only a message's own author may edit it. */
  editMessage(messageId: string, editor: Author, content: string, reason: string): MessageView {
    const message = this.getMessage(messageId)
    if (message.author !== editor || message.kind === 'relay') {
      throw new DomainError('error.invalidBody', { reason: 'editor' })
    }
    const projectId = this.projectOfMessage(messageId)
    this.store.append([{ type: 'message.edited', projectId, data: { messageId, content, reason } }])
    return this.getMessage(messageId)
  }

  markRead(conversationId: string, upTo: string): void {
    const projectId = this.projectOfMessage(upTo)
    this.store.append([{ type: 'message.read', projectId, data: { conversationId, upTo } }])
  }

  updateTodo(projectId: string, conversationId: string, items: TodoItem[]): TodoView {
    this.assertConversationIn(projectId, conversationId)
    this.store.append([{ type: 'todo.updated', projectId, data: { conversationId, items } }])
    return this.getTodo(conversationId)
  }

  // ---- helpers ----

  private getMessage(messageId: string): MessageView {
    const row = this.db
      .query<MessageRow, [string]>('SELECT * FROM messages WHERE id = ?')
      .get(messageId)
    if (!row) throw new DomainError('error.notFound', { what: messageId })
    return toMessage(row)
  }

  private projectOfMessage(messageId: string): string {
    const row = this.db
      .query<{ project_id: string }, [string]>('SELECT project_id FROM messages WHERE id = ?')
      .get(messageId)
    if (!row) throw new DomainError('error.notFound', { what: messageId })
    return row.project_id
  }

  private assertThreadIn(projectId: string, threadId: string): void {
    if (this.getThread(threadId).projectId !== projectId) {
      throw new DomainError('error.notFound', { what: threadId })
    }
  }

  /** A conversation is the project's coordinator or one of its threads. */
  private assertConversationIn(projectId: string, conversationId: string): void {
    const project = this.getProject(projectId)
    if (project.coordinatorId === conversationId) return
    this.assertThreadIn(projectId, conversationId)
  }

  private toProject(row: ProjectRow): ProjectView {
    const openAttentions =
      this.db
        .query<{ n: number }, [string]>(
          'SELECT count(*) AS n FROM attentions WHERE project_id = ? AND answered = 0 AND thread_id IS NULL',
        )
        .get(row.id)?.n ?? 0
    const waitingThreads = this.listThreads(row.id).filter((t) => needsUser(t.stage)).length
    return {
      id: row.id,
      name: row.name,
      coordinatorId: row.coordinator_id,
      createdAt: row.created_at,
      updatedAt: row.updated_at,
      needsMe: openAttentions + waitingThreads,
    }
  }
}

export type { AnyStoredEvent }
