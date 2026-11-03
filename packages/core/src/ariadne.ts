import type { Database } from 'bun:sqlite'
import { DomainError } from './errors'
import type { LocalizedMessage } from '@ariadne/i18n'
import type {
  AnyStoredEvent,
  AttentionOption,
  Author,
  MessageKind,
  ReportKind,
  TodoItem,
  TokenUsage,
  ToolCallRecord,
  TurnOutcome,
} from './events'
import { newId } from './ids'
import { defaultProjections, dropProjectionTables } from './projections'
import { needsUser, threadStage } from './stage'
import { EventStore, type EventListener } from './store'
import type {
  AttentionView,
  ConversationRole,
  ConversationView,
  MessageView,
  NeedsMeItem,
  ProjectView,
  ThreadView,
  TodoView,
  TranscriptEntry,
} from './views'

/**
 * Bump when a projection's tables or meaning change: on open, a database written with an
 * older version folds the whole log again. The log itself never changes.
 */
export const PROJECTION_VERSION = 4

interface ConversationRow {
  id: string
  project_id: string
  role: ConversationRole
  pending_seq: number
  turn_id: string | null
  turn_started_at: number | null
  model: string | null
  last_outcome: TurnOutcome | null
  last_error: string | null
  input_tokens: number
  output_tokens: number
}

function toConversation(row: ConversationRow): ConversationView {
  return {
    id: row.id,
    projectId: row.project_id,
    role: row.role,
    pendingSeq: row.pending_seq,
    turn:
      row.turn_id === null
        ? null
        : { id: row.turn_id, startedAt: row.turn_started_at ?? 0, model: row.model },
    lastOutcome: row.last_outcome,
    lastError: row.last_error ? (JSON.parse(row.last_error) as LocalizedMessage) : null,
    usage: { input: row.input_tokens, output: row.output_tokens },
  }
}

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
  turn_id: string | null
  last_outcome: TurnOutcome | null
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

const THREAD_SELECT = `SELECT t.*, c.turn_id, c.last_outcome, (
  SELECT count(*) FROM attentions a WHERE a.thread_id = t.id AND a.answered = 0
) AS open_attentions FROM threads t LEFT JOIN conversations c ON c.id = t.id`

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
      turnRunning: row.turn_id !== null,
      turnFailed: row.turn_id === null && row.last_outcome === 'failed',
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
    const version =
      this.db.query<{ user_version: number }, []>('PRAGMA user_version').get()?.user_version ?? 0
    if (version !== PROJECTION_VERSION) {
      this.rebuildProjections()
      this.db.run(`PRAGMA user_version = ${PROJECTION_VERSION}`)
    }
  }

  subscribe(listener: EventListener): () => void {
    return this.store.subscribe(listener)
  }

  events(options: { afterSeq?: number; projectId?: string; limit?: number } = {}) {
    return this.store.read(options)
  }

  /** Sequence number of the newest event; clients follow the stream from here. */
  lastSeq(): number {
    return this.store.lastSeq()
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

  /** The project's "needs me" list: open attentions first, then threads waiting on the user. */
  listNeedsMe(projectId: string): NeedsMeItem[] {
    const threads = this.listThreads(projectId)
    const byId = new Map(threads.map((thread) => [thread.id, thread]))
    const attentions = this.listOpenAttentions(projectId)
    const asked = new Set(attentions.map((a) => a.threadId))
    return [
      ...attentions.map((attention): NeedsMeItem => ({
        kind: 'attention',
        attention,
        thread: attention.threadId ? (byId.get(attention.threadId) ?? null) : null,
      })),
      ...threads
        .filter((thread) => needsUser(thread.stage) && !asked.has(thread.id))
        .map((thread): NeedsMeItem => ({ kind: 'thread', thread })),
    ]
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

  getConversation(conversationId: string): ConversationView {
    const row = this.db
      .query<ConversationRow, [string]>('SELECT * FROM conversations WHERE id = ?')
      .get(conversationId)
    if (!row) throw new DomainError('error.notFound', { what: conversationId })
    return toConversation(row)
  }

  /** Conversations with unseen input and no turn running, oldest input first. */
  listPendingConversations(): ConversationView[] {
    return this.db
      .query<ConversationRow, []>(
        'SELECT * FROM conversations WHERE pending_seq > 0 AND turn_id IS NULL ORDER BY pending_seq',
      )
      .all()
      .map(toConversation)
  }

  /** Turns still marked running, e.g. after the process stopped mid-turn. */
  listRunningConversations(): ConversationView[] {
    return this.db
      .query<ConversationRow, []>('SELECT * FROM conversations WHERE turn_id IS NOT NULL')
      .all()
      .map(toConversation)
  }

  /** Everything this conversation's agent has seen, oldest first. */
  listTranscript(conversationId: string): TranscriptEntry[] {
    const rows = this.db
      .query<{ seq: number; kind: string; ref: string | null; payload: string | null }, [string]>(
        'SELECT seq, kind, ref, payload FROM transcript WHERE conversation_id = ? ORDER BY id',
      )
      .all(conversationId)
    const entries: TranscriptEntry[] = []
    for (const row of rows) {
      const payload = row.payload ? JSON.parse(row.payload) : null
      switch (row.kind) {
        case 'message': {
          const message = row.ref ? this.findMessage(row.ref) : null
          if (message) entries.push({ seq: row.seq, kind: 'message', message })
          break
        }
        case 'note':
          entries.push({ seq: row.seq, kind: 'note', note: payload })
          break
        case 'step':
          entries.push({ seq: row.seq, kind: 'step', ...payload })
          break
        case 'tool':
          entries.push({ seq: row.seq, kind: 'tool', ...payload })
          break
      }
    }
    return entries
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
    const title = input.title.trim()
    if (!title) throw new DomainError('error.invalidBody', { reason: 'title' })
    const threadId = newId('thr')
    this.store.append([
      {
        type: 'thread.created',
        projectId,
        data: { threadId, title, nodeId: input.nodeId ?? null },
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
    const message = this.getMessage(upTo)
    if (message.conversationId !== conversationId) {
      throw new DomainError('error.notFound', { what: upTo })
    }
    const projectId = this.projectOfMessage(upTo)
    this.store.append([{ type: 'message.read', projectId, data: { conversationId, upTo } }])
  }

  updateTodo(projectId: string, conversationId: string, items: TodoItem[]): TodoView {
    this.assertConversationIn(projectId, conversationId)
    this.store.append([{ type: 'todo.updated', projectId, data: { conversationId, items } }])
    return this.getTodo(conversationId)
  }

  // ---- turns ----

  /** Begin a turn. A thread's first turn also marks the thread started. */
  startTurn(conversationId: string, model: string): string {
    const conversation = this.getConversation(conversationId)
    if (conversation.turn) throw new DomainError('error.turnRunning')
    const turnId = newId('trn')
    const projectId = conversation.projectId
    const events: Parameters<EventStore['append']>[0] = [
      { type: 'turn.started', projectId, data: { conversationId, turnId, model } },
    ]
    const started = this.db
      .query<{ started: number }, [string]>('SELECT started FROM threads WHERE id = ?')
      .get(conversationId)
    if (conversation.role === 'thread' && started?.started === 0) {
      events.unshift({ type: 'thread.started', projectId, data: { threadId: conversationId } })
    }
    this.store.append(events)
    return turnId
  }

  recordStep(
    conversationId: string,
    turnId: string,
    step: { text: string; toolCalls: ToolCallRecord[]; sawSeq: number; usage: TokenUsage | null },
  ): void {
    const projectId = this.runningTurn(conversationId, turnId)
    this.store.append([{ type: 'turn.step', projectId, data: { conversationId, turnId, ...step } }])
  }

  recordToolResult(
    conversationId: string,
    turnId: string,
    result: { toolCallId: string; name: string; content: string; isError: boolean },
  ): void {
    const projectId = this.runningTurn(conversationId, turnId)
    this.store.append([
      { type: 'tool.finished', projectId, data: { conversationId, turnId, ...result } },
    ])
  }

  /**
   * End a turn. Tool calls of the turn that never got a result are closed as errors first,
   * so the transcript stays a valid model input.
   */
  finishTurn(
    conversationId: string,
    turnId: string,
    outcome: TurnOutcome,
    error: LocalizedMessage | null = null,
  ): void {
    const projectId = this.runningTurn(conversationId, turnId)
    const answered = new Set<string>()
    const called: ToolCallRecord[] = []
    for (const entry of this.listTranscript(conversationId)) {
      if (entry.kind === 'step') called.push(...entry.toolCalls)
      if (entry.kind === 'tool') answered.add(entry.toolCallId)
    }
    const dangling = called.filter((call) => !answered.has(call.id))
    this.store.append([
      ...dangling.map((call) => ({
        type: 'tool.finished' as const,
        projectId,
        data: {
          conversationId,
          turnId,
          toolCallId: call.id,
          name: call.name,
          content: `The call did not finish: the turn ended as ${outcome}.`,
          isError: true,
        },
      })),
      { type: 'turn.finished', projectId, data: { conversationId, turnId, outcome, error } },
    ])
  }

  /** Whether the newest unseen input of a conversation is a message the user wrote. */
  pendingIsUserMessage(conversationId: string): boolean {
    const row = this.db
      .query<{ author: string }, [string]>(
        `SELECT m.author FROM conversations c
           JOIN transcript t ON t.conversation_id = c.id AND t.seq = c.pending_seq
           JOIN messages m ON m.id = t.ref
         WHERE c.id = ? AND t.kind = 'message'`,
      )
      .get(conversationId)
    return row?.author === 'user'
  }

  /**
   * Record a turn that could not start, e.g. no model is configured, so the user sees why
   * and can retry. A thread is not marked started by it.
   */
  failBeforeTurn(conversationId: string, error: unknown): void {
    const conversation = this.getConversation(conversationId)
    const turnId = newId('trn')
    const projectId = conversation.projectId
    const localized: LocalizedMessage =
      error instanceof DomainError
        ? error.toLocalized()
        : error === null
          ? { key: 'error.noModel' }
          : { key: 'error.modelRequest', params: { detail: String(error) } }
    this.store.append([
      { type: 'turn.started', projectId, data: { conversationId, turnId, model: '' } },
      {
        type: 'turn.finished',
        projectId,
        data: { conversationId, turnId, outcome: 'failed', error: localized },
      },
    ])
  }

  /** Ask a conversation's agent to try again, e.g. after a failed turn. */
  requestTurn(conversationId: string): void {
    const conversation = this.getConversation(conversationId)
    if (conversation.turn) throw new DomainError('error.turnRunning')
    this.store.append([
      { type: 'turn.requested', projectId: conversation.projectId, data: { conversationId } },
    ])
  }

  // ---- helpers ----

  private runningTurn(conversationId: string, turnId: string): string {
    const conversation = this.getConversation(conversationId)
    if (conversation.turn?.id !== turnId) throw new DomainError('error.turnNotRunning')
    return conversation.projectId
  }

  private findMessage(messageId: string): MessageView | null {
    const row = this.db
      .query<MessageRow, [string]>('SELECT * FROM messages WHERE id = ?')
      .get(messageId)
    return row ? toMessage(row) : null
  }

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
    return {
      id: row.id,
      name: row.name,
      coordinatorId: row.coordinator_id,
      createdAt: row.created_at,
      updatedAt: row.updated_at,
      needsMe: this.listNeedsMe(row.id).length,
    }
  }
}

export type { AnyStoredEvent }
