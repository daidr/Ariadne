import type { Database } from 'bun:sqlite'
import { createHash } from 'node:crypto'
import { DomainError } from './errors'
import {
  parseAnswer,
  parseBlock,
  isAskBlock,
  type AskBlock,
  type BlockAnswer,
  type ConfirmBlock,
  type ShowBlock,
  type UiBlock,
  BLOCK_LIMITS,
} from './blocks'
import type { LocalizedMessage } from '@ariadne/i18n'
import type {
  AnyNewEvent,
  AnyStoredEvent,
  ArtifactKind,
  Attachment,
  BranchMerge,
  Author,
  IssueWrite,
  LinkedIssue,
  MessageKind,
  PlanAuthor,
  PlanOp,
  ProjectSource,
  ReportKind,
  StepInput,
  CallTiming,
  ReasoningRecord,
  ThreadPullRequest,
  TodoItem,
  TokenUsage,
  ToolCallRecord,
  TurnOutcome,
} from './events'
import { checkArtifactContent, MAX_ARTIFACT_JSON } from './artifact-content'
import {
  fileInfo,
  readFile,
  saveFile,
  setupFiles,
  storeInlineImages,
  type StoredFile,
} from './files'
import { newId } from './ids'
import { applyOps, describePlanOps, needsConfirmation, previewPlanOps } from './plan'
import {
  defaultProjections,
  dropProjectionTables,
  loadPlanGraph,
  MAX_CHECK_ATTEMPTS,
} from './projections'
import { needsUser, threadStage, type ThreadStage } from './stage'
import { EventStore, toStored as storedEvent, type EventListener, type EventRow } from './store'
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
export const PROJECTION_VERSION = 24

/** The events a conversation's trace is rebuilt from; each names its conversation. */
const TURN_EVENT_TYPES = [
  'turn.started',
  'turn.step',
  'tool.finished',
  'turn.finished',
  'conversation.model_retry',
  'conversation.compacted',
  'conversation.compaction_failed',
  'conversation.instructions',
] as const

/** Memory is read into every model request, so it stays short. */
/** Files the user can send with one message. */
export const MAX_ATTACHMENTS = 10
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
  context_tokens: number
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
    contextTokens: row.context_tokens,
  }
}

interface ProjectRow {
  id: string
  name: string
  coordinator_id: string
  source: string | null
  slots: number | null
  mcp: string
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
  /** The card the user agrees on, given every change spelled out. */
  card: (changes: LocalizedMessage[]) => ConfirmBlock
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
  reported_at: number | null
  reported_head: string | null
  workspace_label: string | null
  workspace_branch: string | null
  workspace_base: string | null
  workspace_removed: number
  merged: string | null
  pull_request: string | null
  check_failures: number
  stopped_reason: string | null
  stopped_by: string | null
  stopped_at: number | null
  model: string | null
  created_at: number
  updated_at: number
  open_attentions: number
  turn_id: string | null
  pending_seq: number | null
  last_outcome: TurnOutcome | null
  input_tokens: number | null
  output_tokens: number | null
}

interface AttentionRow {
  id: string
  project_id: string
  thread_id: string | null
  conversation_id: string | null
  message_id: string | null
  block: string
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
  blocks: string | null
  attachments: string | null
  ask_id: string | null
  ask_block: string | null
  ask_answer: string | null
  ask_withdrawn: string | null
}

/** Messages with the ask block they carry, if any, and its answer. */
const MESSAGE_SELECT = `SELECT m.*, a.id AS ask_id, a.block AS ask_block, a.answer AS ask_answer,
  a.withdrawn AS ask_withdrawn FROM messages m LEFT JOIN attentions a ON a.message_id = m.id`

// A thread that joined another's card waits on the user just as much as the one that asked.
const THREAD_SELECT = `SELECT t.*, c.turn_id, c.pending_seq, c.last_outcome, c.input_tokens,
  c.output_tokens, (
  SELECT count(*) FROM attentions a WHERE a.closed = 0 AND (a.thread_id = t.id OR EXISTS (
    SELECT 1 FROM attention_waiters w WHERE w.attention_id = a.id AND w.thread_id = t.id))
) AS open_attentions FROM threads t LEFT JOIN conversations c ON c.id = t.id`

function toThread(row: ThreadRow): ThreadView {
  const merged = row.merged ? (JSON.parse(row.merged) as ThreadView['merged']) : null
  const pullRequest = row.pull_request ? (JSON.parse(row.pull_request) as ThreadPullRequest) : null
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
      // Merged after it reported, or it reported the very commit that was merged.
      landed:
        (!!merged && (merged.at >= (row.reported_at ?? 0) || merged.head === row.reported_head)) ||
        pullRequest?.state === 'merged',
    }),
    lastReport: row.last_report,
    lastSummary: row.last_summary,
    workspace:
      row.workspace_label === null
        ? null
        : {
            label: row.workspace_label,
            branch: row.workspace_branch,
            base: row.workspace_base,
            removed: row.workspace_removed === 1,
          },
    merged,
    pullRequest,
    stopped:
      row.stopped_reason === null
        ? null
        : { reason: row.stopped_reason, by: row.stopped_by ?? 'user', at: row.stopped_at ?? 0 },
    model: row.model,
    usage: { input: row.input_tokens ?? 0, output: row.output_tokens ?? 0 },
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  }
}

function toAttention(row: AttentionRow, joinedBy: string[] = []): AttentionView {
  return {
    id: row.id,
    projectId: row.project_id,
    threadId: row.thread_id,
    joinedBy,
    block: JSON.parse(row.block) as AskBlock,
    messageId: row.message_id,
    conversationId: row.conversation_id,
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
    blocks: row.blocks ? (JSON.parse(row.blocks) as ShowBlock[]) : [],
    ask:
      row.ask_id && row.ask_block
        ? {
            attentionId: row.ask_id,
            block: JSON.parse(row.ask_block) as AskBlock,
            answer: row.ask_answer ? (JSON.parse(row.ask_answer) as BlockAnswer) : null,
            withdrawn: row.ask_withdrawn,
          }
        : null,
    attachments: row.attachments ? (JSON.parse(row.attachments) as Attachment[]) : [],
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
  /** Changes to issues being made right now, by the yes each uses, with the thread making it. */
  private readonly issueWritesUnderWay = new Map<string, string>()

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
        'SELECT * FROM attentions WHERE project_id = ? AND closed = 0 ORDER BY id',
      )
      .all(projectId)
      .map((row) => toAttention(row, this.joinedBy(row.id, row.thread_id)))
  }

  private joinedBy(attentionId: string, asker: string | null): string[] {
    return this.db
      .query<{ thread_id: string }, [string]>(
        'SELECT thread_id FROM attention_waiters WHERE attention_id = ? ORDER BY rowid',
      )
      .all(attentionId)
      .map((w) => w.thread_id)
      .filter((id) => id !== asker)
  }

  /** The project's "needs me" list: open attentions first, then threads waiting on the user. */
  listNeedsMe(projectId: string): NeedsMeItem[] {
    const threads = this.listThreads(projectId)
    const byId = new Map(threads.map((thread) => [thread.id, thread]))
    const attentions = this.listOpenAttentions(projectId)
    // A thread waiting on a card is shown with that card, not again on its own.
    const asked = new Set(attentions.flatMap((a) => [a.threadId, ...a.joinedBy]))
    return [
      ...attentions.map((attention): NeedsMeItem => ({
        kind: 'attention',
        attention,
        thread: attention.threadId ? (byId.get(attention.threadId) ?? null) : null,
        joined: attention.joinedBy.flatMap((id) => byId.get(id) ?? []),
      })),
      ...threads
        .filter((thread) => needsUser(thread.stage) && !asked.has(thread.id))
        .map((thread): NeedsMeItem => ({ kind: 'thread', thread })),
    ]
  }

  listMessages(conversationId: string): MessageView[] {
    return this.db
      .query<MessageRow, [string]>(`${MESSAGE_SELECT} WHERE m.conversation_id = ? ORDER BY m.id`)
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

  /**
   * Everything this conversation's agent has seen, oldest first. With `upTo`, only what
   * was there once event `upTo` was written, messages as they read then: the input a step
   * that had seen up to it was built from.
   */
  listTranscript(conversationId: string, options: { upTo?: number } = {}): TranscriptEntry[] {
    const upTo = options.upTo ?? Number.MAX_SAFE_INTEGER
    const rows = this.db
      .query<
        { seq: number; kind: string; ref: string | null; payload: string | null },
        [string, number]
      >(
        'SELECT seq, kind, ref, payload FROM transcript WHERE conversation_id = ? AND seq <= ? ORDER BY id',
      )
      .all(conversationId, upTo)
    const entries: TranscriptEntry[] = []
    for (const row of rows) {
      const payload = row.payload ? JSON.parse(row.payload) : null
      switch (row.kind) {
        case 'message': {
          const message = row.ref ? this.findMessage(row.ref) : null
          if (message) {
            const content =
              options.upTo === undefined ? message.content : this.contentAt(message, upTo)
            entries.push({
              seq: row.seq,
              kind: 'message',
              message: { ...message, content },
              ...(payload?.openAsks ? { openAsks: payload.openAsks } : {}),
            })
          }
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
    compaction: {
      summary: string
      upTo: number
      sawSeq: number
      usage: TokenUsage | null
      /** JSON of the provider, model, effort and settings, see `StepInput.call`. */
      call: string
      timing: CallTiming
    },
  ): void {
    const projectId = this.runningTurn(conversationId, turnId)
    const summary = compaction.summary.trim()
    if (!summary) throw new DomainError('error.invalidBody', { reason: 'summary' })
    const { pieces, keep } = this.promptKeeper(projectId)
    const data = {
      conversationId,
      summary,
      upTo: compaction.upTo,
      sawSeq: compaction.sawSeq,
      usage: compaction.usage,
      call: keep(compaction.call),
      timing: compaction.timing,
    }
    this.store.append([...pieces, { type: 'conversation.compacted', projectId, data }])
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
    file: { path: string; hash: string; content: string | null; uncommitted?: boolean },
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

  /**
   * Which external tool servers the project's conversations may use. Ids are the user's
   * settings'; the caller checks they exist.
   */
  setProjectMcp(projectId: string, servers: string[]): ProjectView {
    const project = this.getProject(projectId)
    if (!Array.isArray(servers) || servers.some((id) => typeof id !== 'string' || !id)) {
      throw new DomainError('error.invalidBody', { reason: 'servers' })
    }
    const unique = [...new Set(servers)]
    if (JSON.stringify(unique) === JSON.stringify(project.mcp)) return project
    this.store.append([{ type: 'project.mcp.set', projectId, data: { servers: unique } }])
    return this.getProject(projectId)
  }

  createThread(
    projectId: string,
    input: { title: string; nodeId?: string | null; model?: string | null },
  ): ThreadView {
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
        data: { threadId, title, nodeId: input.nodeId ?? null, model: input.model ?? null },
      },
    ])
    return this.getThread(threadId)
  }

  /** Give a thread another model setting from its next step on; `null` is the default. */
  setThreadModel(threadId: string, model: string | null, by: string): ThreadView {
    const thread = this.getThread(threadId)
    if (thread.model === model) return thread
    this.store.append([
      { type: 'thread.model_set', projectId: thread.projectId, data: { threadId, model, by } },
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
  /**
   * Link an issue to a plan node, or refresh its title when it is linked already. Small
   * and undoable like other plan edits: it applies at once, and unlinking takes it back.
   */
  linkIssue(projectId: string, nodeId: string, issue: LinkedIssue, by: string): PlanView {
    this.assertNode(projectId, nodeId)
    if (!Number.isInteger(issue.number) || issue.number < 1) {
      throw new DomainError('error.invalidBody', { reason: 'number' })
    }
    this.store.append([
      {
        type: 'plan.issue_linked',
        projectId,
        data: {
          nodeId,
          issue: {
            tracker: issue.tracker,
            number: issue.number,
            title: issue.title.slice(0, 300),
            url: issue.url,
          },
          by,
        },
      },
    ])
    return this.getPlan(projectId)
  }

  unlinkIssue(
    projectId: string,
    nodeId: string,
    issue: { tracker: string; number: number },
    by: string,
  ): PlanView {
    this.assertNode(projectId, nodeId)
    const linked = this.getPlan(projectId)
      .nodes.find((n) => n.id === nodeId)
      ?.issues.some((i) => i.tracker === issue.tracker && i.number === issue.number)
    if (!linked) return this.getPlan(projectId)
    this.store.append([
      {
        type: 'plan.issue_unlinked',
        projectId,
        data: { nodeId, tracker: issue.tracker, number: issue.number, by },
      },
    ])
    return this.getPlan(projectId)
  }

  private assertNode(projectId: string, nodeId: string) {
    this.getProject(projectId)
    if (!loadPlanGraph(this.db, projectId).nodes.has(nodeId)) {
      throw new DomainError('error.planInvalid', { reason: `no node ${nodeId}` })
    }
  }

  /**
   * The user's yes for one change to an issue. Unlike a standing approval, a yes is used for
   * one change only: once a change was made with it, the same change again asks again. A
   * no holds for the threads that waited on that card.
   *
   * Threads that waited on one card share its yes, and one of them makes the change: the
   * others hear `made` (with who made it and where) instead of being asked again, until
   * their model has seen the note that it was made (after that, the same change is new work
   * and asks again); while
   * it is under way they hear `writing` and are told how it went. `unsure` means a change
   * with this yes was started but not known to have gone through (Ariadne stopped in
   * between); a change still under way in this process is never taken for one cut off.
   */
  issueApproval(
    projectId: string,
    threadId: string,
    purpose: string,
    card: () => ConfirmBlock,
  ):
    | { state: 'approved' | 'unsure'; attentionId: string }
    | { state: 'writing'; by: string }
    | { state: 'made'; by: string; url: string | null }
    | { state: 'declined' | 'pending' | 'asked' } {
    this.assertThreadIn(projectId, threadId)
    const newest = this.db
      .query<
        {
          id: string
          closed: number
          approved: number | null
          thread_id: string | null
          status: string | null
          writer: string | null
          url: string | null
          written_seq: number | null
        },
        [string, string]
      >(
        `SELECT a.id, a.closed, a.approved, a.thread_id, w.status, w.thread_id AS writer, w.url,
           w.written_seq
         FROM attentions a
         LEFT JOIN issue_writes w ON w.attention_id = a.id
         WHERE a.project_id = ? AND a.purpose = ?
         ORDER BY a.created_at DESC, a.id DESC LIMIT 1`,
      )
      .get(projectId, purpose)
    if (!newest) {
      this.openAttention(projectId, { threadId, block: card(), purpose })
      return { state: 'asked' }
    }
    const onCard = this.onCard(newest.id, newest.thread_id, threadId)
    const join = () => {
      if (onCard) return
      this.store.append([
        { type: 'attention.joined', projectId, data: { attentionId: newest.id, threadId } },
      ])
    }
    if (!newest.closed) {
      join()
      return { state: 'pending' }
    }
    if (newest.approved === 1) {
      if (newest.status === null) return { state: 'approved', attentionId: newest.id }
      if (newest.status === 'writing') {
        const by = this.issueWritesUnderWay.get(newest.id)
        if (by === undefined) return { state: 'unsure', attentionId: newest.id }
        // Asking for it meanwhile: hear how it went with those who waited on the card.
        if (by !== threadId) join()
        return { state: 'writing', by }
      }
      // Only while the thread has not yet seen that it was made: once the note reached its
      // model, the same change again is new work and asks again.
      if (
        newest.status === 'written' &&
        onCard &&
        newest.writer &&
        newest.writer !== threadId &&
        this.lastSeenSeq(threadId) < (newest.written_seq ?? 0)
      ) {
        return { state: 'made', by: newest.writer, url: newest.url }
      }
    }
    if (newest.approved === 0 && onCard) return { state: 'declined' }
    this.openAttention(projectId, { threadId, block: card(), purpose })
    return { state: 'asked' }
  }

  /** The newest event the conversation's model had seen when it last took a step. */
  private lastSeenSeq(conversationId: string): number {
    return (
      this.db
        .query<{ saw: number | null }, [string]>(
          `SELECT json_extract(payload, '$.sawSeq') AS saw FROM transcript
           WHERE conversation_id = ? AND kind = 'step' ORDER BY id DESC LIMIT 1`,
        )
        .get(conversationId)?.saw ?? 0
    )
  }

  /** Whether the thread asked on this card or joined it, and so hears its answer. */
  private onCard(attentionId: string, asker: string | null, threadId: string): boolean {
    return (
      asker === threadId ||
      !!this.db
        .query<{ one: number }, [string, string]>(
          'SELECT 1 AS one FROM attention_waiters WHERE attention_id = ? AND thread_id = ?',
        )
        .get(attentionId, threadId)
    )
  }

  /**
   * Record that a change to an issue is about to be made with this yes; it is used now.
   * Until it is finished or marked unsure, it counts as under way in this process.
   */
  startIssueWrite(projectId: string, write: IssueWrite): void {
    this.assertThreadIn(projectId, write.threadId)
    const used = this.db
      .query<{ one: number }, [string]>('SELECT 1 AS one FROM issue_writes WHERE attention_id = ?')
      .get(write.attentionId)
    if (used) throw new DomainError('error.invalidBody', { reason: 'approval used' })
    this.store.append([{ type: 'issue.writing', projectId, data: write }])
    this.issueWritesUnderWay.set(write.attentionId, write.threadId)
  }

  finishIssueWrite(projectId: string, write: IssueWrite, url: string | null): void {
    this.issueWritesUnderWay.delete(write.attentionId)
    this.store.append([{ type: 'issue.written', projectId, data: { ...write, url } }])
  }

  /**
   * The change may or may not have gone through, and `threadId` was told to check; the
   * yes is spent. The others on the card hear it too.
   */
  markIssueWriteUnsure(projectId: string, attentionId: string, threadId: string): void {
    this.issueWritesUnderWay.delete(attentionId)
    this.store.append([{ type: 'issue.write_unsure', projectId, data: { attentionId, threadId } }])
  }

  /**
   * The user's yes for one call of an external tool by one thread: `purpose` names the
   * tool and the exact arguments. A yes is used for one call (`useMcpApproval`), and the
   * same call later asks again. A no holds until the user writes in the thread again, so
   * the thread does not ask the same thing twice on its own.
   */
  mcpApproval(
    projectId: string,
    threadId: string,
    purpose: string,
    card: () => ConfirmBlock,
  ): { state: 'approved'; attentionId: string } | { state: 'asked' | 'pending' | 'declined' } {
    this.assertThreadIn(projectId, threadId)
    const newest = this.db
      .query<
        { id: string; closed: number; approved: number | null; used: number | null },
        [string, string, string]
      >(
        `SELECT a.id, a.closed, a.approved,
           (SELECT 1 FROM mcp_calls c WHERE c.attention_id = a.id) AS used
         FROM attentions a
         WHERE a.project_id = ? AND a.purpose = ? AND a.conversation_id = ?
         ORDER BY a.created_at DESC, a.id DESC LIMIT 1`,
      )
      .get(projectId, purpose, threadId)
    const ask = () => {
      this.openAttention(projectId, { threadId, block: card(), purpose })
      return { state: 'asked' as const }
    }
    if (!newest) return ask()
    if (!newest.closed) return { state: 'pending' }
    if (newest.approved === 1 && !newest.used) return { state: 'approved', attentionId: newest.id }
    if (newest.approved === 0 && !this.userWroteSince(threadId, newest.id)) {
      return { state: 'declined' }
    }
    return ask()
  }

  /** Whether the user wrote in the conversation after it heard this attention's answer. */
  private userWroteSince(conversationId: string, attentionId: string): boolean {
    return !!this.db
      .query<{ one: number }, [string, string, string]>(
        `SELECT 1 AS one FROM transcript t JOIN messages m ON m.id = t.ref
         WHERE t.conversation_id = ? AND t.kind = 'message' AND m.author = 'user'
           AND t.seq > (SELECT a.seq FROM transcript a WHERE a.conversation_id = ?
             AND a.kind = 'note' AND json_extract(a.payload, '$.attentionId') = ?)
         LIMIT 1`,
      )
      .get(conversationId, conversationId, attentionId)
  }

  /** Spend the yes on `attentionId` on the call about to be made. */
  useMcpApproval(
    projectId: string,
    input: { threadId: string; attentionId: string; server: string; tool: string },
  ): void {
    this.assertThreadIn(projectId, input.threadId)
    const used = this.db
      .query<{ one: number }, [string]>('SELECT 1 AS one FROM mcp_calls WHERE attention_id = ?')
      .get(input.attentionId)
    if (used) throw new DomainError('error.invalidBody', { reason: 'approval used' })
    this.store.append([{ type: 'mcp.called', projectId, data: input }])
  }

  stopThread(threadId: string, input: { reason: string; by: string }): ThreadView {
    const thread = this.getThread(threadId)
    // Stopping again changes nothing, except the user taking over the coordinator's stop:
    // then only the user can lift it. Without a reason of their own, the one given stays.
    const takeover = !!thread.stopped && input.by === 'user' && thread.stopped.by !== 'user'
    if (thread.stopped && !takeover) return thread
    const reason = input.reason.trim() || (takeover ? (thread.stopped?.reason ?? '') : '')
    if (!reason) throw new DomainError('error.invalidBody', { reason: 'reason' })
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

  /** `head` is the branch's newest commit, to tell whether merged work is all it reports. */
  reportThread(threadId: string, report: ReportKind, summary: string, head?: string): ThreadView {
    const thread = this.getThread(threadId)
    this.store.append([
      {
        type: 'thread.reported',
        projectId: thread.projectId,
        data: { threadId, report, summary, ...(head ? { head } : {}) },
      },
    ])
    return this.getThread(threadId)
  }

  /**
   * Where the user stands on a standing approval for the project, as seen by one thread.
   * A yes holds for every thread of the project. A no holds only for the threads that
   * waited on that card, so other work may still ask. `null` when there is nothing to go by
   * and the thread may ask.
   */
  approvalFor(
    projectId: string,
    threadId: string,
    purpose: string,
  ): 'approved' | 'declined' | 'pending' | null {
    const approved = this.db
      .query<{ id: string }, [string, string]>(
        'SELECT id FROM attentions WHERE project_id = ? AND purpose = ? AND closed = 1 AND approved = 1 LIMIT 1',
      )
      .get(projectId, purpose)
    if (approved) return 'approved'
    if (this.openApproval(projectId, purpose)) return 'pending'
    const mine = this.db
      .query<{ approved: number }, [string, string, string, string]>(
        `SELECT a.approved FROM attentions a
         WHERE a.project_id = ? AND a.purpose = ? AND a.closed = 1
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
          `SELECT id FROM attentions WHERE project_id = ? AND purpose = ? AND closed = 0
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
    card: () => ConfirmBlock,
  ): 'approved' | 'declined' | 'pending' | 'asked' {
    const state = this.approvalFor(projectId, threadId, purpose)
    if (state === 'approved' || state === 'declined') return state
    this.assertThreadIn(projectId, threadId)
    const open = this.openApproval(projectId, purpose)
    if (open) {
      const asker = this.db
        .query<{ thread_id: string | null }, [string]>(
          'SELECT thread_id FROM attentions WHERE id = ?',
        )
        .get(open)?.thread_id
      if (this.onCard(open, asker ?? null, threadId)) return 'pending'
      this.store.append([
        { type: 'attention.joined', projectId, data: { attentionId: open, threadId } },
      ])
      return 'pending'
    }
    this.openAttention(projectId, { threadId, block: card(), purpose })
    return 'asked'
  }

  openAttention(
    projectId: string,
    input: {
      threadId?: string | null
      block: AskBlock
      messageId?: string | null
      purpose?: string | null
    },
  ): AttentionView {
    this.getProject(projectId)
    if (input.threadId) this.assertThreadIn(projectId, input.threadId)
    const attentionId = newId('att')
    this.store.append([this.attentionOpened(projectId, attentionId, input)])
    const view = this.listOpenAttentions(projectId).find((a) => a.id === attentionId)
    if (!view) throw new Error('attention projection missing')
    return view
  }

  private attentionOpened(
    projectId: string,
    attentionId: string,
    input: {
      threadId?: string | null
      block: AskBlock
      messageId?: string | null
      purpose?: string | null
    },
  ): AnyNewEvent {
    // Checked again even when the system builds it: what is stored is what the web draws.
    const block = parseBlock(input.block)
    if (!isAskBlock(block))
      throw new DomainError('error.blockInvalid', { reason: 'not an ask block' })
    if (input.purpose && block.type !== 'confirm') {
      throw new DomainError('error.blockInvalid', { reason: 'an approval is a confirm block' })
    }
    return {
      type: 'attention.opened',
      projectId,
      data: {
        attentionId,
        threadId: input.threadId ?? null,
        block,
        messageId: input.messageId ?? null,
        ...(input.purpose ? { purpose: input.purpose } : {}),
      },
    }
  }

  answerAttention(attentionId: string, input: unknown) {
    const row = this.db
      .query<AttentionRow & { closed: number; withdrawn: string | null }, [string]>(
        'SELECT * FROM attentions WHERE id = ?',
      )
      .get(attentionId)
    if (row?.withdrawn !== null && row?.withdrawn !== undefined) {
      throw new DomainError('error.askWithdrawn', { reason: row.withdrawn })
    }
    if (!row || row.closed === 1) throw new DomainError('error.notFound', { what: attentionId })
    const answer = parseAnswer(toAttention(row).block, input)
    const events: Parameters<EventStore['append']>[0] = [
      {
        type: 'attention.answered',
        projectId: row.project_id,
        data: { attentionId, answer },
      },
    ]
    // Agreeing to a plan proposal applies it, if it still fits the plan as it is now.
    const proposal = this.db
      .query<{ id: string; reason: string; ops: string; revert_of: string | null }, [string]>(
        "SELECT id, reason, ops, revert_of FROM plan_proposals WHERE attention_id = ? AND status = 'open'",
      )
      .get(attentionId)
    if (proposal && answer.type === 'confirm' && answer.approved) {
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
   * An agent takes back a question it asked in its own conversation, e.g. after the user
   * answered it in words. The system's own approvals and proposals are not the agent's to
   * withdraw, and neither is a question already answered.
   */
  withdrawAttention(conversationId: string, attentionId: string, reason: string): void {
    const row = this.db
      .query<
        {
          project_id: string
          conversation_id: string | null
          message_id: string | null
          closed: number
        },
        [string]
      >('SELECT project_id, conversation_id, message_id, closed FROM attentions WHERE id = ?')
      .get(attentionId)
    if (!row || row.conversation_id !== conversationId || row.message_id === null) {
      throw new DomainError('error.notFound', { what: attentionId })
    }
    if (row.closed === 1) throw new DomainError('error.askClosed')
    const text = reason.replace(/\s+/g, ' ').trim().slice(0, 200)
    if (!text) throw new DomainError('error.invalidBody', { reason: 'reason' })
    this.store.append([
      {
        type: 'attention.withdrawn',
        projectId: row.project_id,
        data: { attentionId, reason: text },
      },
    ])
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

  /**
   * Post a message. An agent's may carry interface blocks: tables, charts, steps and
   * entries to the project's threads are shown under the text, and at most one choice,
   * confirmation or form asks the user, as an attention of the conversation's own.
   */
  postMessage(
    projectId: string,
    input: {
      conversationId: string
      author: Author
      content: string
      kind?: MessageKind
      blocks?: unknown[]
      /** Files of this project the user sends with it, as uploaded. */
      attachments?: { fileId: string; name: string }[]
    },
  ): MessageView {
    this.assertConversationIn(projectId, input.conversationId)
    const attachments = this.attachmentsOf(projectId, input.attachments ?? [])
    if (attachments.length && input.author !== 'user') {
      throw new DomainError('error.invalidBody', { reason: 'attachments' })
    }
    const kind = input.kind ?? 'chat'
    // Only the coordinator relays into a thread.
    if (kind === 'relay' && input.author !== 'coordinator') {
      throw new DomainError('error.invalidBody', { reason: 'kind' })
    }
    const parsed: UiBlock[] = (input.blocks ?? []).map((block, i) =>
      parseBlock(block, `blocks[${i}]`),
    )
    if (parsed.length && (input.author === 'user' || kind !== 'chat')) {
      throw new DomainError('error.invalidBody', { reason: 'blocks' })
    }
    if (parsed.length > BLOCK_LIMITS.blocks) {
      throw new DomainError('error.blockInvalid', {
        reason: `a message holds at most ${BLOCK_LIMITS.blocks} blocks`,
      })
    }
    const asks = parsed.filter(isAskBlock)
    if (asks.length > 1) {
      throw new DomainError('error.blockInvalid', {
        reason: 'a message asks at most one thing: one choice, confirm or form',
      })
    }
    const shown = parsed.filter((block): block is ShowBlock => !isAskBlock(block))
    parsed.forEach((block, i) => {
      if (block.type !== 'thread') return
      const owner = this.db
        .query<{ project_id: string }, [string]>('SELECT project_id FROM threads WHERE id = ?')
        .get(block.threadId)
      if (owner?.project_id !== projectId) {
        throw new DomainError('error.blockInvalid', {
          reason: `blocks[${i}].threadId is not a thread of this project`,
        })
      }
    })
    if (!input.content.trim() && !parsed.length && !attachments.length) {
      throw new DomainError('error.invalidBody', { reason: 'content' })
    }
    const messageId = newId('msg')
    const asker =
      this.getProject(projectId).coordinatorId === input.conversationId
        ? null
        : input.conversationId
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
          ...(shown.length ? { blocks: shown } : {}),
          ...(attachments.length ? { attachments } : {}),
        },
      },
      ...asks.map((block) =>
        this.attentionOpened(projectId, newId('att'), {
          threadId: asker,
          block,
          messageId,
        }),
      ),
    ])
    return this.getMessage(messageId)
  }

  /** Attachments checked against the project's files, named as given (trimmed, one line). */
  private attachmentsOf(
    projectId: string,
    given: { fileId: string; name: string }[],
  ): Attachment[] {
    if (!Array.isArray(given)) throw new DomainError('error.invalidBody', { reason: 'attachments' })
    if (given.length > MAX_ATTACHMENTS) {
      throw new DomainError('error.tooManyAttachments', { max: MAX_ATTACHMENTS })
    }
    return given.map((item) => {
      const fileId = typeof item?.fileId === 'string' ? item.fileId : ''
      const info = fileId ? fileInfo(this.db, projectId, fileId) : null
      if (!info) throw new DomainError('error.notFound', { what: fileId || 'file' })
      const name =
        (typeof item.name === 'string' ? item.name : '')
          .replace(/[\s\p{Cc}]+/gu, ' ')
          .trim()
          .slice(0, 200) || fileId
      return { fileId, name, mime: info.mime, size: info.size }
    })
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
  recordWorkspace(
    threadId: string,
    workspace: { label: string; branch: string | null; base: string | null },
  ): void {
    const thread = this.getThread(threadId)
    const known = thread.workspace
    if (
      known &&
      !known.removed &&
      known.label === workspace.label &&
      known.branch === workspace.branch &&
      known.base === workspace.base
    ) {
      return
    }
    this.store.append([
      { type: 'workspace.opened', projectId: thread.projectId, data: { threadId, ...workspace } },
    ])
  }

  /** Record that the thread's worktree is gone; its branch stays. */
  recordWorktreeRemoved(threadId: string, reason: 'merged' | 'user'): void {
    const thread = this.getThread(threadId)
    if (!thread.workspace || thread.workspace.removed) return
    this.store.append([
      { type: 'workspace.removed', projectId: thread.projectId, data: { threadId, reason } },
    ])
  }

  /** The user merged the thread's branch: the coordinator hears of it, the thread later. */
  recordMerge(threadId: string, merge: BranchMerge): ThreadView {
    const thread = this.getThread(threadId)
    this.store.append([
      { type: 'branch.merged', projectId: thread.projectId, data: { threadId, ...merge } },
    ])
    return this.getThread(threadId)
  }

  /** A merge the user tried stopped at conflicts: the thread is woken to resolve them. */
  recordMergeConflict(
    threadId: string,
    conflict: { branch: string; base: string; files: string[] },
  ): ThreadView {
    const thread = this.getThread(threadId)
    this.store.append([
      { type: 'branch.conflicted', projectId: thread.projectId, data: { threadId, ...conflict } },
    ])
    return this.getThread(threadId)
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
    const issues = new Map<string, PlanNodeView['issues']>()
    for (const row of this.db
      .query<
        {
          node_id: string
          tracker: string
          number: number
          title: string
          url: string
          linked_by: string
        },
        [string]
      >(
        'SELECT node_id, tracker, number, title, url, linked_by FROM plan_node_issues WHERE project_id = ? ORDER BY seq',
      )
      .all(projectId)) {
      const { node_id, linked_by, ...issue } = row
      issues.set(node_id, [...(issues.get(node_id) ?? []), { ...issue, linkedBy: linked_by }])
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
            ...(thread.pullRequest.reference ? { reference: thread.pullRequest.reference } : {}),
            url: thread.pullRequest.url,
            state: thread.pullRequest.state,
            checks: thread.pullRequest.checks,
          },
        })),
        changedIn: changedIn.get(id) ?? null,
        issues: issues.get(id) ?? [],
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
        this.attentionOpened(projectId, attentionId, {
          block: confirmation.card(this.describePlanChange(projectId, ops)),
        }),
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
  saveFile(
    projectId: string,
    input: { mime: string; data: Uint8Array },
    options: { text?: boolean } = {},
  ): StoredFile {
    this.getProject(projectId)
    return saveFile(this.db, projectId, input, this.now(), options)
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

  /**
   * Record one model response, with the input it answered that the transcript does not
   * hold (system prompt, tool definitions, current state). Each piece is stored once per
   * project by its hash, so the trace can rebuild every request whole.
   */
  recordStep(
    conversationId: string,
    turnId: string,
    step: {
      text: string
      reasoning: ReasoningRecord[]
      toolCalls: ToolCallRecord[]
      sawSeq: number
      usage: TokenUsage | null
      input: { system: string; tools: string; state: string | null; call: string; images: boolean }
      timing: CallTiming
    },
  ): void {
    const projectId = this.runningTurn(conversationId, turnId)
    const { pieces, keep } = this.promptKeeper(projectId)
    const input: StepInput = {
      system: keep(step.input.system),
      tools: keep(step.input.tools),
      state: step.input.state === null ? null : keep(step.input.state),
      call: keep(step.input.call),
      images: step.input.images,
    }
    const { text, reasoning, toolCalls, sawSeq, usage, timing } = step
    const finish = toolCalls.length ? ('tool-calls' as const) : ('stop' as const)
    this.store.append([
      ...pieces,
      {
        type: 'turn.step',
        projectId,
        data: {
          conversationId,
          turnId,
          text,
          reasoning,
          toolCalls,
          finish,
          sawSeq,
          usage,
          input,
          timing,
        },
      },
    ])
  }

  /** Stores each piece of model input once per project; `keep` gives its hash. */
  private promptKeeper(projectId: string) {
    const pieces: AnyNewEvent[] = []
    const seen = new Set<string>()
    const keep = (content: string) => {
      const hash = createHash('sha256').update(content).digest('hex')
      if (!seen.has(hash) && this.getPrompt(projectId, hash) === null) {
        pieces.push({ type: 'prompt.recorded', projectId, data: { hash, content } })
      }
      seen.add(hash)
      return hash
    }
    return { pieces, keep }
  }

  /** A piece of model input by its hash, see `recordStep`; `null` when not stored. */
  getPrompt(projectId: string, hash: string): string | null {
    return (
      this.db
        .query<{ content: string }, [string, string]>(
          'SELECT content FROM prompts WHERE project_id = ? AND hash = ?',
        )
        .get(projectId, hash)?.content ?? null
    )
  }

  /** How many model requests (steps and summaries) a conversation made before `seq`. */
  countRequests(conversationId: string, seq: number): number {
    const conversation = this.getConversation(conversationId)
    return (
      this.db
        .query<{ n: number }, [string, string, number]>(
          `SELECT count(*) AS n FROM events WHERE project_id = ?
             AND type IN ('turn.step', 'conversation.compacted')
             AND json_extract(data, '$.conversationId') = ? AND seq < ?`,
        )
        .get(conversation.projectId, conversationId, seq)?.n ?? 0
    )
  }

  /** The last model step of a conversation before `seq`, in whatever turn it was. */
  lastStepBefore(conversationId: string, seq: number): AnyStoredEvent | null {
    const conversation = this.getConversation(conversationId)
    const row = this.db
      .query<EventRow, [string, string, number]>(
        `SELECT * FROM events WHERE project_id = ? AND type = 'turn.step'
           AND json_extract(data, '$.conversationId') = ? AND seq < ?
         ORDER BY seq DESC LIMIT 1`,
      )
      .get(conversation.projectId, conversationId, seq)
    return row ? storedEvent(row) : null
  }

  /**
   * Every event of a conversation's turns, oldest first: what the trace is rebuilt from.
   * `before` (a `turn.started` seq) and `turns` page backwards through them.
   */
  listTurnEvents(
    conversationId: string,
    options: { before?: number; turns?: number } = {},
  ): { events: AnyStoredEvent[]; more: boolean } {
    const conversation = this.getConversation(conversationId)
    const turns = options.turns ?? 10
    const starts = this.db
      .query<{ seq: number }, [string, string, number, number]>(
        `SELECT seq FROM events WHERE project_id = ? AND type = 'turn.started'
           AND json_extract(data, '$.conversationId') = ? AND seq < ?
         ORDER BY seq DESC LIMIT ?`,
      )
      .all(
        conversation.projectId,
        conversationId,
        options.before ?? Number.MAX_SAFE_INTEGER,
        turns + 1,
      )
    const more = starts.length > turns
    const first = starts.slice(0, turns).at(-1)?.seq
    if (first === undefined) return { events: [], more: false }
    const until = options.before ?? Number.MAX_SAFE_INTEGER
    const rows = this.db
      .query<EventRow, [string, number, number, string]>(
        `SELECT * FROM events WHERE project_id = ? AND seq >= ? AND seq < ?
           AND type IN (${TURN_EVENT_TYPES.map((type) => `'${type}'`).join(', ')})
           AND json_extract(data, '$.conversationId') = ?
         ORDER BY seq`,
      )
      .all(conversation.projectId, first, until, conversationId)
    return { events: rows.map(storedEvent), more }
  }

  recordToolResult(
    conversationId: string,
    turnId: string,
    result: {
      toolCallId: string
      name: string
      content: string
      isError: boolean
      files?: Attachment[]
    },
  ): void {
    const projectId = this.runningTurn(conversationId, turnId)
    const { files, ...rest } = result
    this.store.append([
      {
        type: 'tool.finished',
        projectId,
        data: { conversationId, turnId, ...rest, ...(files?.length ? { files } : {}) },
      },
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

  /** What a message said once event `seq` was written: its last edit by then, if any. */
  private contentAt(message: MessageView, seq: number): string {
    if (message.editedAt === null) return message.content
    const edit = this.db
      .query<{ content: string }, [string, string, number]>(
        `SELECT json_extract(data, '$.content') AS content FROM events
         WHERE project_id = (SELECT project_id FROM messages WHERE id = ?1)
           AND type = 'message.edited' AND json_extract(data, '$.messageId') = ?2 AND seq <= ?3
         ORDER BY seq DESC LIMIT 1`,
      )
      .get(message.id, message.id, seq)
    return edit?.content ?? message.originalContent ?? message.content
  }

  private findMessage(messageId: string): MessageView | null {
    const row = this.db
      .query<MessageRow, [string]>(`${MESSAGE_SELECT} WHERE m.id = ?`)
      .get(messageId)
    return row ? toMessage(row) : null
  }

  private getMessage(messageId: string): MessageView {
    const row = this.db
      .query<MessageRow, [string]>(`${MESSAGE_SELECT} WHERE m.id = ?`)
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

  private projectUsage(projectId: string): { input: number; output: number } {
    const row = this.db
      .query<{ input: number | null; output: number | null }, [string]>(
        `SELECT sum(input_tokens) AS input, sum(output_tokens) AS output FROM conversations
         WHERE project_id = ?`,
      )
      .get(projectId)
    return { input: row?.input ?? 0, output: row?.output ?? 0 }
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
      mcp: JSON.parse(row.mcp) as string[],
      usage: this.projectUsage(row.id),
    }
  }
}

export type { AnyStoredEvent }
