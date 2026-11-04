import type { Database } from 'bun:sqlite'
import { DomainError } from './errors'
import type { LocalizedMessage } from '@ariadne/i18n'
import type {
  AnyNewEvent,
  AnyStoredEvent,
  ArtifactKind,
  AttentionOption,
  Author,
  MessageKind,
  PlanAuthor,
  PlanOp,
  ProjectSource,
  ReportKind,
  ThreadPullRequest,
  TodoItem,
  TokenUsage,
  ToolCallRecord,
  TurnOutcome,
} from './events'
import { checkArtifactContent, MAX_ARTIFACT_JSON } from './artifact-content'
import { readFile, saveFile, setupFiles, storeInlineImages, type StoredFile } from './files'
import { newId } from './ids'
import { applyOps, describePlanOps, needsConfirmation, previewPlanOps } from './plan'
import {
  defaultProjections,
  dropProjectionTables,
  loadPlanGraph,
  MAX_CHECK_ATTEMPTS,
} from './projections'
import { needsUser, threadStage, type ThreadStage } from './stage'
import { EventStore, type EventListener } from './store'
import type {
  ArtifactPreview,
  ArtifactSummary,
  ArtifactVersionView,
  ArtifactView,
  MemoryView,
  NoticeView,
  AttentionView,
  ConversationRole,
  ConversationView,
  MessageView,
  NeedsMeItem,
  PlanNodeStatus,
  PlanNodeView,
  PlanProposalView,
  PlanRevisionView,
  PlanView,
  ProjectView,
  ThreadView,
  TodoView,
  TranscriptEntry,
} from './views'

/**
 * Bump when a projection's tables or meaning change: on open, a database written with an
 * older version folds the whole log again. The log itself never changes.
 */
export const PROJECTION_VERSION = 15

/** Memory is read into every model request, so it stays short. */
export const MAX_MEMORIES = 50
export const MAX_MEMORY = 500

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

/**
 * What the model is told about a call whose result was never recorded. Its side effects
 * may already have happened, so it must check before calling again.
 */
export function unknownResult(reason: string): string {
  return `The result of this call is unknown: the turn ended (${reason}) before it was recorded. It may already have taken effect; check before calling it again.`
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
  source: string | null
  slots: number | null
  created_at: number
  updated_at: number
}

interface ArtifactRow {
  id: string
  project_id: string
  kind: ArtifactKind
  title: string
  node_id: string | null
  version: number
  preview: string
  created_by: string
  updated_by: string
  created_at: number
  updated_at: number
}

/** Every column but the content, which lists do not need. */
const ARTIFACT_COLUMNS =
  'id, project_id, kind, title, node_id, version, preview, created_by, updated_by, created_at, updated_at'

function toArtifactSummary(row: ArtifactRow): ArtifactSummary {
  return {
    preview: JSON.parse(row.preview) as ArtifactPreview,
    id: row.id,
    projectId: row.project_id,
    kind: row.kind,
    title: row.title,
    nodeId: row.node_id,
    version: row.version,
    createdBy: row.created_by,
    updatedBy: row.updated_by,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  }
}

/** A plan node's status from the stages of the threads working on it. */
function nodeStatus(all: ThreadStage[]): PlanNodeStatus {
  // A stopped thread does not move its node either way; a node whose threads are all
  // stopped shows as paused, not as never started.
  const stages = all.filter((stage) => stage !== 'stopped')
  if (!all.length) return 'none'
  if (!stages.length) return 'paused'
  if (stages.some((stage) => stage === 'blocked')) return 'error'
  if (stages.some((stage) => stage === 'answer' || stage === 'review')) return 'wait'
  if (stages.every((stage) => stage === 'done')) return 'finish'
  return 'process'
}

/**
 * How a change that needs the user's yes is put to them. `question` receives what the
 * change does, one line per op, so the user sees exactly what they agree to.
 */
export interface PlanConfirmation {
  question: (changes: LocalizedMessage[]) => string
  apply: AttentionOption
  keep: AttentionOption
}

/** What `revisePlan` did: applied at once, or waiting on the user's yes. */
export type PlanChange =
  | { status: 'applied'; revisionId: string }
  | { status: 'proposed'; proposalId: string; attentionId: string }

interface ThreadRow {
  id: string
  project_id: string
  title: string
  node_id: string | null
  started: number
  last_report: ReportKind | null
  last_summary: string | null
  workspace_label: string | null
  workspace_branch: string | null
  pull_request: string | null
  check_failures: number
  stopped_reason: string | null
  stopped_by: string | null
  stopped_at: number | null
  created_at: number
  updated_at: number
  open_attentions: number
  turn_id: string | null
  pending_seq: number | null
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
  original_content: string | null
  edited_since_read: number
}

const THREAD_SELECT = `SELECT t.*, c.turn_id, c.pending_seq, c.last_outcome, (
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
      turnPending: row.turn_id === null && (row.pending_seq ?? 0) > 0,
      turnFailed: row.turn_id === null && row.last_outcome === 'failed',
      checksGaveUp: row.check_failures >= MAX_CHECK_ATTEMPTS,
      stopped: row.stopped_reason !== null,
    }),
    lastReport: row.last_report,
    lastSummary: row.last_summary,
    workspace:
      row.workspace_label === null
        ? null
        : { label: row.workspace_label, branch: row.workspace_branch },
    pullRequest: row.pull_request ? (JSON.parse(row.pull_request) as ThreadPullRequest) : null,
    stopped:
      row.stopped_reason === null
        ? null
        : { reason: row.stopped_reason, by: row.stopped_by ?? 'user', at: row.stopped_at ?? 0 },
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
    originalContent: row.original_content,
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
  private readonly now: () => number

  constructor(options: AriadneOptions) {
    this.now = options.now ?? Date.now
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
    setupFiles(this.db)
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

  /** Conversations with unseen input and no turn running, the longest waiting first. */
  listPendingConversations(): ConversationView[] {
    return this.db
      .query<ConversationRow, []>(
        `SELECT * FROM conversations WHERE pending_seq > 0 AND turn_id IS NULL
           AND id NOT IN (SELECT id FROM threads WHERE stopped_reason IS NOT NULL)
         ORDER BY pending_since`,
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
        case 'summary':
          entries.push({ seq: row.seq, kind: 'summary', ...payload })
          break
      }
    }
    return entries
  }

  /**
   * Replace a conversation's history up to `upTo` with a summary, during one of its turns.
   * The log keeps everything; only what the model reads gets shorter.
   */
  compactConversation(
    conversationId: string,
    turnId: string,
    compaction: { summary: string; upTo: number; usage: TokenUsage | null },
  ): void {
    const projectId = this.runningTurn(conversationId, turnId)
    const summary = compaction.summary.trim()
    if (!summary) throw new DomainError('error.invalidBody', { reason: 'summary' })
    const data = { conversationId, summary, upTo: compaction.upTo, usage: compaction.usage }
    this.store.append([{ type: 'conversation.compacted', projectId, data }])
  }

  /** Summarising failed: the user sees it in the conversation, the turn goes on. */
  recordCompactionFailure(conversationId: string, turnId: string, detail: string): void {
    const projectId = this.runningTurn(conversationId, turnId)
    this.store.append([
      { type: 'conversation.compaction_failed', projectId, data: { conversationId, detail } },
    ])
  }

  /** The newest instruction file recorded for a conversation, or `null`. */
  getInstructions(
    conversationId: string,
  ): { path: string; hash: string; content: string | null } | null {
    const row = this.db
      .query<{ instructions: string | null }, [string]>(
        'SELECT instructions FROM conversations WHERE id = ?',
      )
      .get(conversationId)
    return row?.instructions ? JSON.parse(row.instructions) : null
  }

  /**
   * Record the instruction file as the workspace holds it, when it differs from what was
   * recorded last. Returns whether anything was recorded.
   */
  recordInstructions(
    conversationId: string,
    turnId: string,
    file: { path: string; hash: string; content: string | null },
  ): boolean {
    const projectId = this.runningTurn(conversationId, turnId)
    const before = this.getInstructions(conversationId)
    if (before ? before.hash === file.hash : file.content === null) return false
    this.store.append([
      {
        type: 'conversation.instructions',
        projectId,
        data: { conversationId, ...file, changed: before !== null },
      },
    ])
    return true
  }

  /** The model request is tried again after `delayMs`; the user sees each retry. */
  recordModelRetry(
    conversationId: string,
    turnId: string,
    retry: { attempt: number; delayMs: number; detail: string },
  ): void {
    const projectId = this.runningTurn(conversationId, turnId)
    this.store.append([
      { type: 'conversation.model_retry', projectId, data: { conversationId, ...retry } },
    ])
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

  /** Cap how many turns of the project run at once (1–16), or lift the cap with `null`. */
  setProjectSlots(projectId: string, slots: number | null): ProjectView {
    const project = this.getProject(projectId)
    if (slots !== null && !(Number.isInteger(slots) && slots >= 1 && slots <= 16)) {
      throw new DomainError('error.invalidBody', { reason: 'slots' })
    }
    if (project.slots === slots) return project
    this.store.append([{ type: 'project.slots.set', projectId, data: { slots } }])
    return this.getProject(projectId)
  }

  createThread(projectId: string, input: { title: string; nodeId?: string | null }): ThreadView {
    this.getProject(projectId)
    const title = input.title.trim()
    if (!title) throw new DomainError('error.invalidBody', { reason: 'title' })
    if (input.nodeId && !loadPlanGraph(this.db, projectId).nodes.has(input.nodeId)) {
      throw new DomainError('error.notFound', { what: input.nodeId })
    }
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

  /**
   * Hold a thread: its running turn is ended by the scheduler, and no new one starts until
   * it is resumed. Input that arrives meanwhile waits for then.
   */
  stopThread(threadId: string, input: { reason: string; by: string }): ThreadView {
    const thread = this.getThread(threadId)
    const reason = input.reason.trim()
    if (!reason) throw new DomainError('error.invalidBody', { reason: 'reason' })
    // Stopping again changes nothing, except the user taking over the coordinator's stop:
    // then only the user can lift it.
    if (thread.stopped && !(input.by === 'user' && thread.stopped.by !== 'user')) return thread
    this.store.append([
      {
        type: 'thread.stopped',
        projectId: thread.projectId,
        data: { threadId, reason, by: input.by },
      },
    ])
    return this.getThread(threadId)
  }

  resumeThread(threadId: string, by: string): ThreadView {
    const thread = this.getThread(threadId)
    if (!thread.stopped) return thread
    this.store.append([
      {
        type: 'thread.resumed',
        projectId: thread.projectId,
        data: { threadId, by, reason: thread.stopped.reason },
      },
    ])
    return this.getThread(threadId)
  }

  reportThread(threadId: string, report: ReportKind, summary: string): ThreadView {
    const thread = this.getThread(threadId)
    this.store.append([
      { type: 'thread.reported', projectId: thread.projectId, data: { threadId, report, summary } },
    ])
    return this.getThread(threadId)
  }

  /**
   * Where the user stands on a standing approval for the project, as seen by one thread.
   * A yes holds for every thread of the project. A no holds only for the threads that
   * waited on that card, so other work may still ask. An answer in words alone (no option
   * picked) decides nothing. `null` when there is nothing to go by and the thread may ask.
   */
  approvalFor(
    projectId: string,
    threadId: string,
    purpose: string,
  ): 'approved' | 'declined' | 'pending' | null {
    const approved = this.db
      .query<{ id: string }, [string, string]>(
        'SELECT id FROM attentions WHERE project_id = ? AND purpose = ? AND answered = 1 AND choice = 0 LIMIT 1',
      )
      .get(projectId, purpose)
    if (approved) return 'approved'
    if (this.openApproval(projectId, purpose)) return 'pending'
    const mine = this.db
      .query<{ choice: number }, [string, string, string, string]>(
        `SELECT a.choice FROM attentions a
         WHERE a.project_id = ? AND a.purpose = ? AND a.answered = 1 AND a.choice IS NOT NULL
           AND (a.thread_id = ? OR EXISTS (
             SELECT 1 FROM attention_waiters w WHERE w.attention_id = a.id AND w.thread_id = ?))
         ORDER BY a.created_at DESC, a.id DESC LIMIT 1`,
      )
      .get(projectId, purpose, threadId, threadId)
    return mine ? 'declined' : null
  }

  private openApproval(projectId: string, purpose: string): string | null {
    return (
      this.db
        .query<{ id: string }, [string, string]>(
          `SELECT id FROM attentions WHERE project_id = ? AND purpose = ? AND answered = 0
           ORDER BY created_at, id LIMIT 1`,
        )
        .get(projectId, purpose)?.id ?? null
    )
  }

  /**
   * Ask the user for a standing approval, once for the whole project: when a card for it
   * is already open, the thread joins it and hears the same answer. Returns where the
   * approval stands afterwards, as `approvalFor` would; `asked` when a new card was opened.
   */
  requestApproval(
    projectId: string,
    threadId: string,
    purpose: string,
    card: () => { question: string; options: AttentionOption[] },
  ): 'approved' | 'declined' | 'pending' | 'asked' {
    const state = this.approvalFor(projectId, threadId, purpose)
    if (state === 'approved' || state === 'declined') return state
    this.assertThreadIn(projectId, threadId)
    const open = this.openApproval(projectId, purpose)
    if (open) {
      const waiting = this.db
        .query<{ one: number }, [string, string, string, string]>(
          `SELECT 1 AS one FROM attentions WHERE id = ? AND thread_id = ?
           UNION SELECT 1 FROM attention_waiters WHERE attention_id = ? AND thread_id = ?`,
        )
        .get(open, threadId, open, threadId)
      if (waiting) return 'pending'
      this.store.append([
        { type: 'attention.joined', projectId, data: { attentionId: open, threadId } },
      ])
      return 'pending'
    }
    this.openAttention(projectId, { threadId, ...card(), purpose })
    return 'asked'
  }

  openAttention(
    projectId: string,
    input: {
      threadId?: string | null
      question: string
      options: AttentionOption[]
      recommended?: number | null
      purpose?: string | null
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
          ...(input.purpose ? { purpose: input.purpose } : {}),
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
    const events: Parameters<EventStore['append']>[0] = [
      {
        type: 'attention.answered',
        projectId: row.project_id,
        data: { attentionId, choice, text: answer.text ?? null },
      },
    ]
    // The first option of a plan proposal applies it, if it still fits the plan as it is now.
    const proposal = this.db
      .query<{ id: string; reason: string; ops: string; revert_of: string | null }, [string]>(
        "SELECT id, reason, ops, revert_of FROM plan_proposals WHERE attention_id = ? AND status = 'open'",
      )
      .get(attentionId)
    if (proposal && choice === 0) {
      const ops = JSON.parse(proposal.ops) as PlanOp[]
      try {
        applyOps(loadPlanGraph(this.db, row.project_id), ops)
        events.push({
          type: 'plan.revised',
          projectId: row.project_id,
          data: {
            revisionId: newId('rev'),
            author: 'coordinator',
            reason: proposal.reason,
            ops,
            revertOf: proposal.revert_of,
            proposalId: proposal.id,
          },
        })
      } catch (error) {
        if (!(error instanceof DomainError)) throw error
        // The plan moved on since it was proposed. Say so: the user sees it did not take
        // effect, and the coordinator is woken to propose again.
        events.push({
          type: 'plan.proposal.stale',
          projectId: row.project_id,
          data: { proposalId: proposal.id, reason: String(error.params?.reason ?? error.code) },
        })
      }
    }
    events.unshift(...this.resumedByUser(row.thread_id))
    this.store.append(events)
  }

  /**
   * The user speaking to a stopped thread (a message, an answer) lets it go on, the same
   * as pressing 继续: the thread and the coordinator are told why it had stopped.
   */
  private resumedByUser(threadId: string | null): Parameters<EventStore['append']>[0] {
    if (!threadId) return []
    const row = this.db
      .query<{ project_id: string; stopped_reason: string | null }, [string]>(
        'SELECT project_id, stopped_reason FROM threads WHERE id = ?',
      )
      .get(threadId)
    if (!row?.stopped_reason) return []
    return [
      {
        type: 'thread.resumed',
        projectId: row.project_id,
        data: { threadId, by: 'user', reason: row.stopped_reason },
      },
    ]
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
      ...(input.author === 'user' ? this.resumedByUser(input.conversationId) : []),
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

  /**
   * Only a message's own author may edit it: the same kind of author, writing in the same
   * conversation. One thread cannot edit another thread's messages.
   */
  editMessage(
    messageId: string,
    editor: { author: Author; conversationId: string },
    content: string,
    reason: string,
  ): MessageView {
    const message = this.getMessage(messageId)
    if (
      message.author !== editor.author ||
      message.conversationId !== editor.conversationId ||
      message.kind === 'relay'
    ) {
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

  // ---- memory ----

  listMemories(projectId: string): MemoryView[] {
    this.getProject(projectId)
    return this.db
      .query<
        {
          id: string
          project_id: string
          text: string
          author: string
          created_at: number
          updated_at: number
        },
        [string]
      >('SELECT * FROM memories WHERE project_id = ? ORDER BY created_at, id')
      .all(projectId)
      .map((row) => ({
        id: row.id,
        projectId: row.project_id,
        text: row.text,
        author: row.author,
        createdAt: row.created_at,
        updatedAt: row.updated_at,
      }))
  }

  /** Save a new memory, or change the one `memoryId` names. */
  saveMemory(
    projectId: string,
    input: { memoryId?: string; text: string; author: string; undoes?: string },
  ): MemoryView {
    // One line each: an entry cannot start a section of the prompts it is shown in.
    const text = input.text.replace(/[\s\u0085]+/g, ' ').trim()
    if (!text) throw new DomainError('error.invalidBody', { reason: 'text' })
    if (text.length > MAX_MEMORY) {
      throw new DomainError('error.invalidBody', { reason: `text longer than ${MAX_MEMORY}` })
    }
    const existing = this.listMemories(projectId)
    if (input.memoryId && !existing.some((m) => m.id === input.memoryId)) {
      throw new DomainError('error.notFound', { what: input.memoryId })
    }
    if (!input.memoryId && existing.length >= MAX_MEMORIES) {
      throw new DomainError('error.memoryFull', { max: MAX_MEMORIES })
    }
    const memoryId = input.memoryId ?? newId('mem')
    const same = existing.find((m) => m.id === memoryId)
    if (same?.text === text) return same
    const data = {
      memoryId,
      text,
      author: input.author,
      ...(input.undoes ? { undoes: input.undoes } : {}),
    }
    this.store.append([{ type: 'memory.saved', projectId, data }])
    return this.listMemories(projectId).find((m) => m.id === memoryId)!
  }

  removeMemory(projectId: string, memoryId: string, author: string, undoes?: string): void {
    if (!this.listMemories(projectId).some((m) => m.id === memoryId)) {
      throw new DomainError('error.notFound', { what: memoryId })
    }
    const data = { memoryId, author, ...(undoes ? { undoes } : {}) }
    this.store.append([{ type: 'memory.removed', projectId, data }])
  }

  /** What happened in a conversation besides its messages, oldest first. */
  listNotices(conversationId: string): NoticeView[] {
    return this.db
      .query<
        {
          id: string
          conversation_id: string
          kind: string
          payload: string
          undone: number
          created_at: number
        },
        [string]
      >('SELECT * FROM notices WHERE conversation_id = ? ORDER BY created_at, id')
      .all(conversationId)
      .map((row) => {
        const base = { id: row.id, conversationId: row.conversation_id, createdAt: row.created_at }
        const payload = JSON.parse(row.payload)
        return (
          row.kind === 'memory'
            ? { ...base, kind: 'memory', ...payload, undone: row.undone === 1 }
            : { ...base, kind: row.kind, ...payload }
        ) as NoticeView
      })
  }

  /**
   * The user takes back an agent's change to the project memory: an added entry goes, a
   * changed one gets its old text back, a removed one returns.
   */
  undoMemoryNotice(projectId: string, noticeId: string): void {
    const row = this.db
      .query<
        { conversation_id: string; kind: string; payload: string; undone: number },
        [string, string]
      >(
        'SELECT conversation_id, kind, payload, undone FROM notices WHERE id = ? AND project_id = ?',
      )
      .get(noticeId, projectId)
    if (!row || row.kind !== 'memory') throw new DomainError('error.notFound', { what: noticeId })
    if (row.undone) return
    const change = JSON.parse(row.payload) as {
      action: 'saved' | 'removed'
      memoryId: string
      previous: string | null
    }
    const exists = this.listMemories(projectId).some((m) => m.id === change.memoryId)
    if (change.action === 'saved' && change.previous === null) {
      if (exists) this.removeMemory(projectId, change.memoryId, 'user', noticeId)
      else this.markUndone(projectId, noticeId)
      return
    }
    const text = change.previous ?? ''
    this.saveMemory(projectId, {
      text,
      author: 'user',
      undoes: noticeId,
      ...(exists ? { memoryId: change.memoryId } : {}),
    })
    // Saving the text it already has writes nothing; the notice is still settled.
    this.markUndone(projectId, noticeId)
  }

  private markUndone(projectId: string, noticeId: string): void {
    const row = this.db
      .query<{ undone: number }, [string]>('SELECT undone FROM notices WHERE id = ?')
      .get(noticeId)
    if (row?.undone) return
    // A no-op removal would fail; an empty save cannot exist. Record the undo by itself.
    this.store.append([{ type: 'notice.dismissed', projectId, data: { noticeId } }])
  }

  updateTodo(projectId: string, conversationId: string, items: TodoItem[]): TodoView {
    this.assertConversationIn(projectId, conversationId)
    this.store.append([{ type: 'todo.updated', projectId, data: { conversationId, items } }])
    return this.getTodo(conversationId)
  }

  setProjectSource(projectId: string, source: ProjectSource | null): ProjectView {
    this.getProject(projectId)
    this.store.append([{ type: 'project.source.set', projectId, data: { source } }])
    return this.getProject(projectId)
  }

  /** Record that a thread's workspace is ready, so the user can see where it works. */
  recordWorkspace(threadId: string, workspace: { label: string; branch: string | null }): void {
    const thread = this.getThread(threadId)
    if (
      thread.workspace?.label === workspace.label &&
      thread.workspace.branch === workspace.branch
    ) {
      return
    }
    this.store.append([
      { type: 'workspace.opened', projectId: thread.projectId, data: { threadId, ...workspace } },
    ])
  }

  /**
   * Record what the repository says about a thread's pull request. Nothing is written
   * when nothing changed; a new failure of the newest commit's checks wakes the thread,
   * and merging or closing tells the coordinator.
   */
  syncPullRequest(threadId: string, pullRequest: ThreadPullRequest): void {
    const thread = this.getThread(threadId)
    const before = thread.pullRequest
    if (JSON.stringify(before) === JSON.stringify(pullRequest)) return
    const projectId = thread.projectId
    const events: AnyNewEvent[] = [
      { type: 'pull_request.synced', projectId, data: { threadId, ...pullRequest } },
    ]
    const failedBefore =
      before?.headSha === pullRequest.headSha && before.checks.state === 'failure'
    if (pullRequest.checks.state === 'failure' && !failedBefore) {
      const row = this.db
        .query<{ check_failures: number }, [string]>(
          'SELECT check_failures FROM threads WHERE id = ?',
        )
        .get(threadId)
      events.push({
        type: 'checks.failed',
        projectId,
        data: {
          threadId,
          number: pullRequest.number,
          headSha: pullRequest.headSha,
          failed: pullRequest.checks.failed,
          attempt: (row?.check_failures ?? 0) + 1,
        },
      })
    }
    if (pullRequest.state !== 'open' && before?.state === 'open') {
      events.push({
        type: 'pull_request.closed',
        projectId,
        data: { threadId, number: pullRequest.number, merged: pullRequest.state === 'merged' },
      })
    }
    this.store.append(events)
  }

  // ---- plan ----

  /** The user has read the notice that an agreed proposal did not take effect. */
  dismissPlanProposal(projectId: string, proposalId: string): void {
    const row = this.db
      .query<{ status: string }, [string, string]>(
        'SELECT status FROM plan_proposals WHERE id = ? AND project_id = ?',
      )
      .get(proposalId, projectId)
    if (!row) throw new DomainError('error.notFound', { what: proposalId })
    if (row.status !== 'stale') return
    this.store.append([{ type: 'plan.proposal.dismissed', projectId, data: { proposalId } }])
  }

  getPlan(projectId: string): PlanView {
    this.getProject(projectId)
    const graph = loadPlanGraph(this.db, projectId)
    const changedIn = new Map(
      this.db
        .query<{ id: string; changed_in: string | null }, [string]>(
          'SELECT id, changed_in FROM plan_nodes WHERE project_id = ?',
        )
        .all(projectId)
        .map((row) => [row.id, row.changed_in]),
    )
    const threads = this.listThreads(projectId)
    const dependsOn = new Map<string, string[]>()
    for (const key of graph.edges) {
      const [from = '', to = ''] = key.split('>')
      dependsOn.set(to, [...(dependsOn.get(to) ?? []), from])
    }
    const nodes = graph.order.map((id): PlanNodeView => {
      const node = graph.nodes.get(id) as NonNullable<ReturnType<typeof graph.nodes.get>>
      const onNode = threads.filter((thread) => thread.nodeId === id)
      return {
        id,
        ...node,
        dependsOn: dependsOn.get(id) ?? [],
        status: nodeStatus(onNode.map((thread) => thread.stage)),
        threads: onNode.map((thread) => ({
          id: thread.id,
          title: thread.title,
          stage: thread.stage,
          pullRequest: thread.pullRequest && {
            number: thread.pullRequest.number,
            url: thread.pullRequest.url,
            state: thread.pullRequest.state,
            checks: thread.pullRequest.checks,
          },
        })),
        changedIn: changedIn.get(id) ?? null,
      }
    })
    const revisions = this.db
      .query<
        {
          id: string
          author: PlanAuthor
          reason: string
          ops: string
          revert_of: string | null
          reverted_by: string | null
          created_at: number
        },
        [string]
      >('SELECT * FROM plan_revisions WHERE project_id = ? ORDER BY seq DESC LIMIT 50')
      .all(projectId)
      .map((row): PlanRevisionView => ({
        id: row.id,
        author: row.author,
        reason: row.reason,
        ops: JSON.parse(row.ops),
        revertOf: row.revert_of,
        revertedBy: row.reverted_by,
        createdAt: row.created_at,
      }))
    const proposals = this.db
      .query<
        {
          id: string
          attention_id: string
          reason: string
          ops: string
          status: PlanProposalView['status']
          stale_reason: string | null
          created_at: number
        },
        [string]
      >(
        "SELECT * FROM plan_proposals WHERE project_id = ? AND status IN ('open', 'stale') ORDER BY id",
      )
      .all(projectId)
      .map((row): PlanProposalView => {
        const ops = JSON.parse(row.ops) as PlanOp[]
        return {
          id: row.id,
          attentionId: row.attention_id,
          reason: row.reason,
          ops,
          changes: this.describePlanChange(projectId, ops),
          preview: row.status === 'open' ? previewPlanOps(graph, ops) : null,
          status: row.status,
          staleReason: row.stale_reason,
          createdAt: row.created_at,
        }
      })
    return { projectId, nodes, revisions, proposals }
  }

  /**
   * Change the plan. Small changes apply at once and can be reverted. When the coordinator
   * removes nodes, changes acceptance or touches a milestone, the change waits on the user:
   * `confirmation` words the question, and its first option applies the change.
   */
  revisePlan(
    projectId: string,
    input: { author: PlanAuthor; reason: string; ops: PlanOp[]; confirmation?: PlanConfirmation },
  ): PlanChange {
    this.getProject(projectId)
    const reason = input.reason.trim()
    if (!reason) throw new DomainError('error.invalidBody', { reason: 'reason' })
    if (!input.ops.length) throw new DomainError('error.invalidBody', { reason: 'ops' })
    return this.changePlan(projectId, input.author, reason, input.ops, null, input.confirmation)
  }

  /** What a change would do, one line per op, as the user would read it. */
  describePlanChange(projectId: string, ops: PlanOp[]): LocalizedMessage[] {
    const threads = this.listThreads(projectId)
    return describePlanOps(
      loadPlanGraph(this.db, projectId),
      ops,
      (nodeId) => threads.filter((thread) => thread.nodeId === nodeId).length,
    )
  }

  /**
   * Undo a revision, as long as nothing since then conflicts with undoing it. Undoing is a
   * change like any other: when the coordinator's undo would remove nodes or change
   * acceptance or milestones, it waits on the user too.
   */
  revertPlanRevision(
    projectId: string,
    revisionId: string,
    author: PlanAuthor,
    reason: string,
    confirmation?: PlanConfirmation,
  ): PlanChange {
    const row = this.db
      .query<{ inverse: string; reverted_by: string | null; reason: string }, [string, string]>(
        'SELECT inverse, reverted_by, reason FROM plan_revisions WHERE id = ? AND project_id = ?',
      )
      .get(revisionId, projectId)
    if (!row) throw new DomainError('error.notFound', { what: revisionId })
    if (row.reverted_by) throw new DomainError('error.planReverted')
    const ops = JSON.parse(row.inverse) as PlanOp[]
    return this.changePlan(
      projectId,
      author,
      reason.trim() || row.reason,
      ops,
      revisionId,
      confirmation,
    )
  }

  private changePlan(
    projectId: string,
    author: PlanAuthor,
    reason: string,
    ops: PlanOp[],
    revertOf: string | null,
    confirmation: PlanConfirmation | undefined,
  ): PlanChange {
    const graph = loadPlanGraph(this.db, projectId)
    applyOps(graph, ops)
    if (author === 'coordinator' && needsConfirmation(graph, ops)) {
      if (!confirmation) throw new DomainError('error.invalidBody', { reason: 'confirmation' })
      const proposalId = newId('prp')
      const attentionId = newId('att')
      this.store.append([
        {
          type: 'attention.opened',
          projectId,
          data: {
            attentionId,
            threadId: null,
            question: confirmation.question(this.describePlanChange(projectId, ops)),
            options: [confirmation.apply, confirmation.keep],
            recommended: 0,
          },
        },
        {
          type: 'plan.proposed',
          projectId,
          data: { proposalId, attentionId, reason, ops, revertOf },
        },
      ])
      return { status: 'proposed', proposalId, attentionId }
    }
    const revisionId = newId('rev')
    this.store.append([
      {
        type: 'plan.revised',
        projectId,
        data: { revisionId, author, reason, ops, revertOf, proposalId: null },
      },
    ])
    return { status: 'applied', revisionId }
  }

  // ---- artifacts ----

  listArtifacts(projectId: string): ArtifactSummary[] {
    return this.db
      .query<ArtifactRow, [string]>(
        `SELECT ${ARTIFACT_COLUMNS} FROM artifacts WHERE project_id = ? ORDER BY updated_at DESC, id DESC`,
      )
      .all(projectId)
      .map(toArtifactSummary)
  }

  /** An artifact at its newest version, or at `version`. */
  getArtifact(artifactId: string, version?: number): ArtifactView {
    const row = this.db
      .query<ArtifactRow & { content: string }, [string]>('SELECT * FROM artifacts WHERE id = ?')
      .get(artifactId)
    if (!row) throw new DomainError('error.notFound', { what: artifactId })
    if (version === undefined || version === row.version) {
      return { ...toArtifactSummary(row), content: row.content }
    }
    const old = this.db
      .query<
        { title: string; content: string; author: string; created_at: number },
        [string, number]
      >(
        'SELECT title, content, author, created_at FROM artifact_versions WHERE artifact_id = ? AND version = ?',
      )
      .get(artifactId, version)
    if (!old) throw new DomainError('error.notFound', { what: `${artifactId}@${version}` })
    return {
      ...toArtifactSummary(row),
      title: old.title,
      version,
      content: old.content,
      updatedBy: old.author,
      updatedAt: old.created_at,
    }
  }

  listArtifactVersions(artifactId: string): ArtifactVersionView[] {
    this.getArtifact(artifactId)
    return this.db
      .query<
        { version: number; title: string; author: string; reason: string; created_at: number },
        [string]
      >(
        'SELECT version, title, author, reason, created_at FROM artifact_versions WHERE artifact_id = ? ORDER BY version DESC',
      )
      .all(artifactId)
      .map((row) => ({
        version: row.version,
        title: row.title,
        author: row.author,
        reason: row.reason,
        createdAt: row.created_at,
      }))
  }

  // ---- files ----

  /** Keep a file people added to the project, such as an image for a whiteboard. */
  saveFile(projectId: string, input: { mime: string; data: Uint8Array }): StoredFile {
    this.getProject(projectId)
    return saveFile(this.db, projectId, input, this.now())
  }

  getFile(fileId: string): { mime: string; data: Uint8Array } {
    const file = readFile(this.db, fileId)
    if (!file) throw new DomainError('error.notFound', { what: fileId })
    return file
  }

  /** A whiteboard's inline images moved into files, before its content is checked. */
  private storeImages(projectId: string, kind: ArtifactKind, content: string): string {
    // Inline images make the content larger than the check allows; give them room.
    if (kind !== 'whiteboard' || content.length > MAX_ARTIFACT_JSON * 8) return content
    return storeInlineImages(content, (file) => this.saveFile(projectId, file))
  }

  createArtifact(
    projectId: string,
    input: {
      kind: ArtifactKind
      title: string
      nodeId?: string | null
      author: string
      content: string
    },
  ): ArtifactView {
    this.getProject(projectId)
    const title = input.title.trim()
    if (!title) throw new DomainError('error.invalidBody', { reason: 'title' })
    // Images are kept only for content that passes; a refused save leaves no files behind.
    const content = this.db.transaction(() => {
      const stored = this.storeImages(projectId, input.kind, input.content)
      checkArtifactContent(input.kind, stored)
      if (input.nodeId && !loadPlanGraph(this.db, projectId).nodes.has(input.nodeId)) {
        throw new DomainError('error.notFound', { what: input.nodeId })
      }
      return stored
    })()
    const artifactId = newId('art')
    this.store.append([
      {
        type: 'artifact.created',
        projectId,
        data: {
          artifactId,
          kind: input.kind,
          title,
          nodeId: input.nodeId ?? null,
          author: input.author,
          content,
        },
      },
    ])
    return this.getArtifact(artifactId)
  }

  /**
   * Save a new version. With `baseVersion`, refuse when someone saved in between, so a
   * user's edit never silently overwrites the agent's (or the other way round).
   */
  reviseArtifact(
    artifactId: string,
    input: {
      title?: string
      content: string
      author: string
      reason: string
      baseVersion?: number
    },
  ): ArtifactView {
    const current = this.getArtifact(artifactId)
    if (input.baseVersion !== undefined && input.baseVersion !== current.version) {
      throw new DomainError('error.artifactConflict', { version: current.version })
    }
    const title = input.title?.trim() || current.title
    const content = this.db.transaction(() => {
      const stored = this.storeImages(current.projectId, current.kind, input.content)
      checkArtifactContent(current.kind, stored)
      return stored
    })()
    if (title === current.title && content === current.content) return current
    this.store.append([
      {
        type: 'artifact.revised',
        projectId: current.projectId,
        data: {
          artifactId,
          version: current.version + 1,
          title,
          content,
          author: input.author,
          reason: input.reason,
        },
      },
    ])
    return this.getArtifact(artifactId)
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
          content: unknownResult(outcome),
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
      source: row.source ? (JSON.parse(row.source) as ProjectSource) : null,
      slots: row.slots,
    }
  }
}

export type { AnyStoredEvent }
