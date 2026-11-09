import type { Database } from 'bun:sqlite'
import { createHash } from 'node:crypto'
import { DomainError } from './errors'
import {
  branchSlug,
  MAX_CHECK_COMMAND,
  MAX_REVIEW_ROUNDS,
  parseProjectSettings,
  parseRoutines,
  storedProjectSettings,
  type ProjectDetected,
} from './project-settings'
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
  AutopilotHold,
  ArtifactCommentAction,
  ArtifactCommentAnchor,
  ArtifactFeedbackItem,
  ArtifactKind,
  Attachment,
  BranchMerge,
  Author,
  IssueWrite,
  LineComment,
  LinkedIssue,
  MergeMethod,
  MessageKind,
  MessageMention,
  PlanAuthor,
  PlanOp,
  ProjectSource,
  ReportKind,
  ReviewComment,
  ReviewCommentKind,
  ReviewReply,
  ReviewVerdict,
  StepInput,
  CallTiming,
  EventMap,
  ReasoningRecord,
  ThreadPullRequest,
  TodoItem,
  TokenUsage,
  ToolCallRecord,
  TurnFile,
  ChangeTotals,
  TurnOutcome,
} from './events'
import { checkArtifactContent, MAX_ARTIFACT_JSON } from './artifact-content'
import {
  fileInfo,
  isImageType,
  readFile,
  saveFile,
  setupFiles,
  storeInlineImages,
  type StoredFile,
} from './files'
import {
  readSharedState,
  setupSharedState,
  writeSharedState,
  type SharedState,
} from './shared-state'
import { newId } from './ids'
import { BackgroundCommandsList, LiveCommands, ReplyDrafts } from './live'
import {
  applyOps,
  describePlanOps,
  droppedWith,
  needsConfirmation,
  previewPlanOps,
  removedNodes,
  whyUndoFails,
  type PlanGraph,
} from './plan'
import {
  defaultProjections,
  dropProjectionTables,
  loadPlanGraph,
  MAX_CHECK_ATTEMPTS,
} from './projections'
import { searchRows, searchTerms, SEARCH_LIMITS, snippetOf } from './search'
import { needsUser, threadStage, type ThreadStage } from './stage'
import { EventStore, toStored as storedEvent, type EventListener, type EventRow } from './store'
import { lineDiff } from './text-diff'
import type {
  ArtifactCommentThreadView,
  ArtifactFeedbackView,
  ArtifactPreview,
  ArtifactSummary,
  ArtifactVersionView,
  ArtifactView,
  MemoryView,
  NoticeView,
  AttentionView,
  ConversationRole,
  ConversationView,
  MessagePage,
  MessageView,
  NeedsMeItem,
  PlanNodeStatus,
  PlanNodeView,
  PlanProposalView,
  PlanRevisionView,
  PlanUndoPreview,
  PlanView,
  ProjectView,
  ReviewRoundView,
  SentLineComment,
  SearchHit,
  SearchResult,
  ThreadArtifact,
  ThreadReport,
  ThreadView,
  TodoView,
  TranscriptEntry,
  UsageTotals,
} from './views'

/**
 * Bump when a projection's tables or meaning change: on open, a database written with an
 * older version folds the whole log again. The log itself never changes.
 */
export const PROJECTION_VERSION = 54

/**
 * The purpose of the question the autopilot asks at a milestone, `<this>:<node id>`: a yes
 * lets the work go on past it.
 */
export const MILESTONE_GATE = 'milestone'

/** How long the user is gone from a project before coming back counts as a return. */
export const AWAY_MS = 10 * 60_000

/** How many messages a conversation shows at first, and loads each time it is scrolled up. */
export const MESSAGE_PAGE = 40

/** The events a conversation's trace is rebuilt from; each names its conversation. */
const TURN_EVENT_TYPES = [
  'turn.started',
  'turn.step',
  'research.step',
  'tool.finished',
  'turn.finished',
  'conversation.model_retry',
  'conversation.model_fallback',
  'conversation.compacted',
  'conversation.results_cleared',
  'conversation.compaction_failed',
  'conversation.instructions',
] as const

/** Memory is read into every model request, so it stays short. */
/** Files the user can send with one message. */
export const MAX_ATTACHMENTS = 10
/** Things one message can point at with `@`. */
export const MAX_MENTIONS = 20
/** Line comments one message can carry, and how long each may be. */
export const LINE_COMMENT_LIMITS = { comments: 100, body: 4000, quote: 500, path: 1000 }
/** Files a turn's changes keep by name; the rest are counted. */
export const MAX_TURN_FILES = 200

const badComments = () => new DomainError('error.invalidBody', { reason: 'comments' })

/** Line comments as the user sent them, checked and trimmed. */
function lineComments(given: unknown[]): LineComment[] {
  const bad = badComments
  if (!Array.isArray(given) || given.length > LINE_COMMENT_LIMITS.comments) throw bad()
  return given.map((item) => {
    const { path, line, quote, body, side } = (item ?? {}) as Record<string, unknown>
    if (typeof path !== 'string' || !path.trim() || path.length > LINE_COMMENT_LIMITS.path) {
      throw bad()
    }
    if (typeof line !== 'number' || !Number.isInteger(line) || line < 1) throw bad()
    if (typeof body !== 'string' || !body.trim() || body.length > LINE_COMMENT_LIMITS.body) {
      throw bad()
    }
    if (side !== undefined && side !== 'original') throw bad()
    const text = typeof quote === 'string' ? quote : ''
    return {
      path: path.trim(),
      line,
      quote:
        text.length > LINE_COMMENT_LIMITS.quote ? text.slice(0, LINE_COMMENT_LIMITS.quote) : text,
      body: body.trim(),
      ...(side ? { side } : {}),
    }
  })
}
const REVIEW_KINDS: readonly ReviewCommentKind[] = ['bug', 'acceptance', 'security']
const badReplies = () => new DomainError('error.invalidBody', { reason: 'replies' })

/** A reviewer's comments, checked like the user's line comments, each with its kind. */
function reviewComments(given: unknown[]): ReviewComment[] {
  const bad = badComments
  if (!Array.isArray(given)) throw bad()
  const lines = lineComments(given)
  return lines.map((comment, i) => {
    const kind = (given[i] as Record<string, unknown>).kind as ReviewCommentKind
    if (!REVIEW_KINDS.includes(kind)) throw bad()
    return { ...comment, kind }
  })
}

/** The author's answers to a round's `count` comments: each one once, declines with a reason. */
export function reviewReplies(given: unknown[], count: number): ReviewReply[] {
  const bad = badReplies
  if (!Array.isArray(given) || given.length !== count) throw bad()
  const seen = new Set<number>()
  const replies = given.map((item) => {
    const { comment, outcome, note } = (item ?? {}) as Record<string, unknown>
    if (!Number.isInteger(comment) || (comment as number) < 1 || (comment as number) > count) {
      throw bad()
    }
    if (seen.has(comment as number)) throw bad()
    seen.add(comment as number)
    if (outcome !== 'fixed' && outcome !== 'declined') throw bad()
    const text = typeof note === 'string' ? note.trim() : ''
    if ((outcome === 'declined' && !text) || text.length > LINE_COMMENT_LIMITS.body) throw bad()
    return { comment: comment as number, outcome, note: text } satisfies ReviewReply
  })
  return replies.toSorted((a, b) => a.comment - b.comment)
}

interface ReviewRow {
  thread_id: string
  reviewer_id: string
  round: number
  head: string | null
  requested_at: number
  verdict: ReviewVerdict | null
  summary: string | null
  comments: string
  replies: string | null
  submitted_at: number | null
  abandoned_at: number | null
}

const toReviewRound = (row: ReviewRow): ReviewRoundView => ({
  threadId: row.thread_id,
  reviewerId: row.reviewer_id,
  round: row.round,
  head: row.head,
  requestedAt: row.requested_at,
  verdict: row.verdict,
  abandoned: row.abandoned_at !== null,
  summary: row.summary,
  comments: JSON.parse(row.comments) as ReviewComment[],
  replies: row.replies ? (JSON.parse(row.replies) as ReviewReply[]) : null,
  submittedAt: row.submitted_at,
})

/**
 * A skill's name, as its file is called without `.md`: letters (any script), digits, `-`,
 * `_` and `.` inside, at most 64 characters.
 */
export const SKILL_NAME = /^[\p{L}\p{N}](?:[\p{L}\p{N}._-]{0,62}[\p{L}\p{N}])?$/u

export const MAX_MEMORIES = 50
export const MAX_MEMORY = 500

interface ConversationRow {
  id: string
  project_id: string
  role: ConversationRole
  pending_seq: number
  turn_id: string | null
  turn_started_at: number | null
  activity: string | null
  model: string | null
  last_outcome: TurnOutcome | null
  last_error: string | null
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  cache_write_tokens: number
  reasoning_tokens: number
  cost_usd: number
  cost_cny: number
  context_tokens: number
  fallback: number
  compactions: number
  wait_id: string | null
  wait_until: number | null
  wait_note: string | null
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
        : {
            id: row.turn_id,
            startedAt: row.turn_started_at ?? 0,
            model: row.model,
            activity: row.activity,
            fallback: row.fallback === 1,
          },
    lastOutcome: row.last_outcome,
    lastError: row.last_error ? (JSON.parse(row.last_error) as LocalizedMessage) : null,
    usage: usageOf(row),
    contextTokens: row.context_tokens,
    compactions: row.compactions,
    wait:
      row.wait_id === null
        ? null
        : { id: row.wait_id, until: row.wait_until ?? 0, note: row.wait_note ?? '' },
  }
}

interface ProjectRow {
  id: string
  name: string
  coordinator_id: string
  source: string | null
  slots: number | null
  mcp: string
  settings: string | null
  detected: string | null
  autopilot_paused: number
  autopilot_since: number | null
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
  open_comments: number
  commented_at: number
  created_at: number
  updated_at: number
}

/** Every column but the content, which lists do not need. */
const ARTIFACT_COLUMNS =
  'id, project_id, kind, title, node_id, version, preview, created_by, updated_by, open_comments, commented_at, created_at, updated_at'

/** A comment's text is kept to this many characters, a quote to this many. */
export const MAX_COMMENT = 4000
export const MAX_COMMENT_QUOTE = 1000
const MAX_COMMENT_BODY = MAX_COMMENT
/** Pictures one round of feedback carries: one per option the user marked. */
export const MAX_FEEDBACK_IMAGES = 6

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
    openComments: row.open_comments,
    commentedAt: row.commented_at,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  }
}

interface CommentThreadRow {
  id: string
  artifact_id: string
  quote: string
  anchor: string | null
  comments: string
  resolved_by: string | null
  resolved_at: number | null
  created_at: number
}

function toCommentThread(row: CommentThreadRow): ArtifactCommentThreadView {
  return {
    id: row.id,
    artifactId: row.artifact_id,
    quote: row.quote,
    anchor: row.anchor ? (JSON.parse(row.anchor) as ArtifactCommentAnchor) : null,
    comments: JSON.parse(row.comments),
    resolved: row.resolved_by ? { by: row.resolved_by, at: row.resolved_at ?? 0 } : null,
    createdAt: row.created_at,
  }
}

interface FeedbackRow {
  artifact_id: string
  round: number
  version: number
  notify: string
  items: string
  images: string
  done_by: string | null
  done_at: number | null
  summary: string | null
  created_at: number
}

function toFeedbackView(row: FeedbackRow): ArtifactFeedbackView {
  return {
    artifactId: row.artifact_id,
    round: row.round,
    version: row.version,
    notify: row.notify,
    items: JSON.parse(row.items),
    images: JSON.parse(row.images),
    doneBy: row.done_by,
    doneAt: row.done_at,
    summary: row.summary,
    createdAt: row.created_at,
  }
}

const invalid = (reason: string) => new DomainError('error.invalidBody', { reason })

/** A comment's text, trimmed; refused when empty or too long. */
function commentText(body: string): string {
  const trimmed = body.trim()
  if (!trimmed || trimmed.length > MAX_COMMENT) throw invalid('body')
  return trimmed
}

/** A plan node's status from the stages of the threads working on it. */
function nodeStatus(all: ThreadStage[], gate: PlanNodeView['gate'] = null): PlanNodeStatus {
  // A milestone the user let the work pass is behind it, whatever ran on it.
  if (gate === 'passed') return 'done'
  if (gate === 'open') return 'waiting'
  // A stopped thread does not move its node either way; a node whose threads are all
  // stopped shows as paused, not as never started.
  const stages = all.filter((stage) => stage !== 'stopped')
  if (!all.length) return 'none'
  if (!stages.length) return 'paused'
  if (stages.some((stage) => stage === 'blocked')) return 'error'
  if (
    stages.some(
      (stage) =>
        stage === 'answer' || stage === 'review' || stage === 'halted' || stage === 'silent',
    )
  )
    return 'waiting'
  if (stages.every((stage) => stage === 'done')) return 'done'
  return 'running'
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
  last_headline: string | null
  /** The pointed-to artifacts that still exist, in order, as JSON. */
  report_artifacts: string
  reported_at: number | null
  reviewed_at: number | null
  reported_head: string | null
  workspace_label: string | null
  workspace_branch: string | null
  workspace_base: string | null
  workspace_removed: number
  merged: string | null
  landed_at: number | null
  user_at: number | null
  changes: string | null
  pull_request: string | null
  check_failures: number
  stopped_reason: string | null
  stopped_by: string | null
  stopped_at: number | null
  model: string | null
  branch_slug: string | null
  review_of: string | null
  autopilot_held: string | null
  auto: number
  reviewer_id: string | null
  review_rounds: number
  reviewing: number
  /** The reviewing thread is held, or idle with nothing to do: it will not finish the round. */
  created_at: number
  updated_at: number
  open_attentions: number
  turn_id: string | null
  pending_seq: number | null
  last_outcome: TurnOutcome | null
  last_turn_at: number | null
  wait_id: string | null
  nudged_at: number | null
  input_tokens: number | null
  output_tokens: number | null
  cached_tokens: number | null
  cache_write_tokens: number | null
  reasoning_tokens: number | null
  cost_usd: number | null
  cost_cny: number | null
  turn_started_at: number | null
  todo_items: string | null
  todo_at: number | null
  message_at: number | null
  /** When the newest entry of what its agent has seen was written: a step, a tool result. */
  transcript_at: number | null
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
  mentions: string | null
  comments: string | null
  ask_id: string | null
  ask_block: string | null
  ask_answer: string | null
  ask_withdrawn: string | null
}

/** Messages with the ask block they carry, if any, and its answer. */
const MESSAGE_SELECT = `SELECT m.*, a.id AS ask_id, a.block AS ask_block, a.answer AS ask_answer,
  a.withdrawn AS ask_withdrawn FROM messages m LEFT JOIN attentions a ON a.message_id = m.id`

/** Token columns of `conversations`, summed or not. */
interface UsageColumns {
  input_tokens?: number | null
  output_tokens?: number | null
  cached_tokens?: number | null
  cache_write_tokens?: number | null
  reasoning_tokens?: number | null
  cost_usd?: number | null
  cost_cny?: number | null
}

const usageOf = (row: UsageColumns): UsageTotals => ({
  input: row.input_tokens ?? 0,
  output: row.output_tokens ?? 0,
  cached: row.cached_tokens ?? 0,
  cacheWrite: row.cache_write_tokens ?? 0,
  reasoning: row.reasoning_tokens ?? 0,
  cost: { USD: row.cost_usd ?? 0, CNY: row.cost_cny ?? 0 },
})

// A thread that joined another's card waits on the user just as much as the one that asked.
const THREAD_SELECT = `SELECT t.*, c.turn_id, c.pending_seq, c.last_outcome, c.last_turn_at,
  c.wait_id, c.input_tokens,
  c.output_tokens, c.cached_tokens, c.cache_write_tokens, c.reasoning_tokens, c.cost_usd, c.cost_cny, c.turn_started_at,
  td.items AS todo_items, td.updated_at AS todo_at,
  (SELECT max(m.created_at) FROM messages m WHERE m.conversation_id = t.id) AS message_at,
  (SELECT e.at FROM events e WHERE e.seq = (SELECT tr.seq FROM transcript tr
    WHERE tr.conversation_id = t.id ORDER BY tr.id DESC LIMIT 1)) AS transcript_at, (
  SELECT json_group_array(json_object('id', ar.id, 'kind', ar.kind, 'title', ar.title))
  FROM (SELECT ar.id, ar.kind, ar.title FROM json_each(t.last_artifacts) j
    JOIN artifacts ar ON ar.id = j.value AND ar.project_id = t.project_id ORDER BY j.key) ar
) AS report_artifacts, (
  SELECT count(*) FROM attentions a WHERE a.closed = 0 AND (a.thread_id = t.id OR EXISTS (
    SELECT 1 FROM attention_waiters w WHERE w.attention_id = a.id AND w.thread_id = t.id))
) AS open_attentions FROM threads t LEFT JOIN conversations c ON c.id = t.id
  LEFT JOIN todos td ON td.conversation_id = t.id`

/** Its last turn ended this way, and it has not reported since that turn began. */
function unreported(row: ThreadRow, outcome: TurnOutcome): boolean {
  return (
    row.turn_id === null &&
    row.last_outcome === outcome &&
    row.last_turn_at !== null &&
    (row.reported_at ?? 0) < row.last_turn_at
  )
}

/** What it is doing now, by its todos: the first one it marked as under way. */
function doingOf(items: string | null): string | null {
  const todos = items ? (JSON.parse(items) as TodoItem[]) : []
  return todos.find((item) => item.status === 'doing')?.label ?? null
}

/** How many of its todos are done, out of how many; `null` while it has none. */
function progressOf(items: string | null): ThreadView['progress'] {
  const todos = items ? (JSON.parse(items) as TodoItem[]) : []
  if (!todos.length) return null
  return { done: todos.filter((item) => item.status === 'done').length, total: todos.length }
}

/** How long a finished thread stays in sight with nothing happening in it. */
export const ARCHIVE_AFTER_MS = 7 * 24 * 60 * 60 * 1000
/** How long one whose work landed stays first: it was finished just now, not "earlier". */
export const ARCHIVE_LANDED_AFTER_MS = 24 * 60 * 60 * 1000

function toThread(row: ThreadRow, now: number): ThreadView {
  const merged = row.merged ? (JSON.parse(row.merged) as ThreadView['merged']) : null
  const pullRequest = row.pull_request ? (JSON.parse(row.pull_request) as ThreadPullRequest) : null
  const underReview = row.reviewing === 1
  const stage = threadStage({
    openAttentions: row.open_attentions,
    started: row.started === 1,
    lastReport: row.last_report,
    turnRunning: row.turn_id !== null,
    turnPending: row.turn_id === null && (row.pending_seq ?? 0) > 0,
    turnFailed: row.turn_id === null && row.last_outcome === 'failed',
    turnCancelled: unreported(row, 'cancelled'),
    unreported: unreported(row, 'completed'),
    waiting: row.wait_id !== null,
    checksGaveUp: row.check_failures >= MAX_CHECK_ATTEMPTS,
    stopped: row.stopped_reason !== null,
    // Merged after it reported, or it reported the very commit that was merged.
    landed:
      (!!merged && (merged.at >= (row.reported_at ?? 0) || merged.head === row.reported_head)) ||
      pullRequest?.state === 'merged',
    reviewed: row.reviewed_at !== null && row.reviewed_at >= (row.reported_at ?? 0),
    underReview,
    reviewer: row.review_of !== null,
  })
  const activeAt = Math.max(
    row.updated_at,
    row.message_at ?? 0,
    row.transcript_at ?? 0,
    row.todo_at ?? 0,
    row.turn_started_at ?? 0,
  )
  return {
    id: row.id,
    projectId: row.project_id,
    title: row.title,
    nodeId: row.node_id,
    stage,
    lastReport: row.last_report,
    lastSummary: row.last_summary,
    lastHeadline: row.last_headline,
    lastArtifacts: JSON.parse(row.report_artifacts) as ThreadArtifact[],
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
    archived:
      stage === 'done' &&
      // Landed after its last report and nothing written to it since: what it did after
      // landing (a later report) has not landed.
      ((row.landed_at !== null &&
        row.landed_at >= (row.reported_at ?? 0) &&
        (row.user_at ?? 0) <= row.landed_at &&
        now - row.landed_at >= ARCHIVE_LANDED_AFTER_MS) ||
        now - activeAt >= ARCHIVE_AFTER_MS),
    changes: row.changes ? (JSON.parse(row.changes) as ChangeTotals) : null,
    pullRequest,
    stopped:
      row.stopped_reason === null
        ? null
        : { reason: row.stopped_reason, by: row.stopped_by ?? 'user', at: row.stopped_at ?? 0 },
    model: row.model,
    branchSlug: row.branch_slug,
    usage: usageOf(row),
    progress: progressOf(row.todo_items),
    doing: doingOf(row.todo_items),
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    activeAt,
    reviewOf: row.review_of,
    review: row.reviewer_id
      ? { reviewerId: row.reviewer_id, rounds: row.review_rounds, reviewing: underReview }
      : null,
    auto: row.auto === 1,
    autopilotHeld: row.autopilot_held,
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

interface NoticeRow {
  id: string
  conversation_id: string
  kind: string
  payload: string
  undone: number
  created_at: number
}

function toNotice(row: NoticeRow): NoticeView {
  const base = { id: row.id, conversationId: row.conversation_id, createdAt: row.created_at }
  const payload = JSON.parse(row.payload)
  return (
    row.kind === 'memory'
      ? { ...base, kind: 'memory', ...payload, undone: row.undone === 1 }
      : row.kind === 'merged' && row.undone === 1
        ? { ...base, kind: 'merged', ...payload, undone: true }
        : { ...base, kind: row.kind, ...payload }
  ) as NoticeView
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
    mentions: row.mentions ? (JSON.parse(row.mentions) as MessageMention[]) : [],
    comments: row.comments ? (JSON.parse(row.comments) as LineComment[]) : [],
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
  /** Commands threads are running now; not part of the log, see `LiveCommands`. */
  readonly live: LiveCommands
  /** Replies models are writing now; not part of the log, see `ReplyDrafts`. */
  readonly drafts: ReplyDrafts
  /** Threads' background commands, as the user watches them. */
  readonly background = new BackgroundCommandsList()
  private readonly db: Database
  private readonly now: () => number
  /** Changes to issues being made right now, by the yes each uses, with the thread making it. */
  private readonly issueWritesUnderWay = new Map<string, string>()

  constructor(options: AriadneOptions) {
    this.now = options.now ?? Date.now
    this.live = new LiveCommands(this.now)
    this.drafts = new ReplyDrafts(this.now)
    this.store = new EventStore({
      path: options.path,
      projections: defaultProjections,
      ...(options.now ? { now: options.now } : {}),
      // Nothing is written for a project that is not there, e.g. one deleted while a
      // plugin or a poll was still at work on it.
      check: (db, event) => {
        if (event.type === 'project.created') return
        const known = db
          .query<{ one: number }, [string]>('SELECT 1 AS one FROM projects WHERE id = ?')
          .get(event.projectId)
        if (!known) throw new DomainError('error.notFound', { what: event.projectId })
      },
    })
    this.db = this.store.db
    const version =
      this.db.query<{ user_version: number }, []>('PRAGMA user_version').get()?.user_version ?? 0
    if (version !== PROJECTION_VERSION) {
      this.rebuildProjections()
      this.db.run(`PRAGMA user_version = ${PROJECTION_VERSION}`)
    }
    setupFiles(this.db)
    setupSharedState(this.db)
  }

  /**
   * Run several commands as one: they are written together or not at all, also when the
   * process dies halfway. Listeners hear the events once all of them are in.
   */
  transaction<T>(work: () => T): T {
    return this.store.transaction(work)
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

  /**
   * Copy the whole database to `path`, consistent as of now, while it stays in use. The
   * copy is a database of its own: to restore it, stop Ariadne and put it in place.
   */
  backupTo(path: string): void {
    this.db.run('VACUUM INTO ?', [path])
  }

  /**
   * Everything the log holds of a project, oldest first, with the files it refers to: a
   * record to keep, readable as JSON. Settings kept outside the log (secrets) are not in it.
   */
  exportProject(projectId: string): {
    project: { id: string; name: string }
    events: AnyStoredEvent[]
    files: { id: string; mime: string; data: string }[]
  } {
    const project = this.getProject(projectId)
    const events: AnyStoredEvent[] = []
    for (let after = 0; ;) {
      const page = this.store.read({ afterSeq: after, projectId, limit: 1000 })
      events.push(...page)
      if (page.length < 1000) break
      after = page.at(-1)!.seq
    }
    const files = this.db
      .query<{ id: string; mime: string; data: Uint8Array }, [string]>(
        'SELECT id, mime, data FROM files WHERE project_id = ? ORDER BY id',
      )
      .all(projectId)
      .map((file) => ({
        id: file.id,
        mime: file.mime,
        data: Buffer.from(file.data).toString('base64'),
      }))
    return { project: { id: project.id, name: project.name }, events, files }
  }

  // ---- queries ----

  listProjects(): ProjectView[] {
    return this.db
      .query<ProjectRow, []>('SELECT * FROM projects ORDER BY updated_at DESC')
      .all()
      .map((row) => this.toProject(row))
  }

  getProject(projectId: string): ProjectView {
    return this.toProject(this.projectRow(projectId))
  }

  /** The project as stored, or an error when there is none; costs one row, no counting. */
  private projectRow(projectId: string): ProjectRow {
    const row = this.db
      .query<ProjectRow, [string]>('SELECT * FROM projects WHERE id = ?')
      .get(projectId)
    if (!row) throw new DomainError('error.notFound', { what: projectId })
    return row
  }

  /** The project with its threads and "needs me" list, reading the threads once. */
  getProjectDetail(projectId: string): {
    project: ProjectView
    threads: ThreadView[]
    needsMe: NeedsMeItem[]
  } {
    const row = this.projectRow(projectId)
    const threads = this.listThreads(projectId)
    const needsMe = this.needsMeOf(projectId, threads)
    return { project: this.toProject(row, needsMe.length), threads, needsMe }
  }

  listThreads(projectId: string): ThreadView[] {
    return this.db
      .query<ThreadRow, [string]>(`${THREAD_SELECT} WHERE t.project_id = ? ORDER BY t.id`)
      .all(projectId)
      .map((row) => toThread(row, this.now()))
  }

  getThread(threadId: string): ThreadView {
    const row = this.db.query<ThreadRow, [string]>(`${THREAD_SELECT} WHERE t.id = ?`).get(threadId)
    if (!row) throw new DomainError('error.notFound', { what: threadId })
    return toThread(row, this.now())
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
    return this.needsMeOf(projectId, this.listThreads(projectId))
  }

  private needsMeOf(projectId: string, threads: ThreadView[]): NeedsMeItem[] {
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
        // A reviewing thread that stalls hands its author's changes to the user instead.
        .filter((thread) => needsUser(thread.stage) && !asked.has(thread.id) && !thread.reviewOf)
        .map((thread): NeedsMeItem => ({ kind: 'thread', thread })),
    ]
  }

  listMessages(conversationId: string): MessageView[] {
    return this.db
      .query<MessageRow, [string]>(`${MESSAGE_SELECT} WHERE m.conversation_id = ? ORDER BY m.id`)
      .all(conversationId)
      .map(toMessage)
  }

  /**
   * A stretch of a conversation, so a long one is not sent whole. With no cursor, the
   * newest `limit` messages; with `before`, the `limit` messages before that one; with
   * `from`, that message and everything after it (a page already shown, with what came
   * since). Notices come with the messages they fall among.
   */
  pageMessages(
    conversationId: string,
    cursor: { before?: string; from?: string; limit?: number; since?: number } = {},
  ): MessagePage {
    const limit = Math.max(1, Math.min(cursor.limit ?? MESSAGE_PAGE, 500))
    if (cursor.since !== undefined) return this.changedMessages(conversationId, cursor)
    let messages: MessageView[]
    if (cursor.from !== undefined) {
      messages = this.db
        .query<MessageRow, [string, string]>(
          `${MESSAGE_SELECT} WHERE m.conversation_id = ? AND m.id >= ? ORDER BY m.id`,
        )
        .all(conversationId, cursor.from)
        .map(toMessage)
    } else {
      messages = this.db
        .query<MessageRow, [string, string, number]>(
          `${MESSAGE_SELECT} WHERE m.conversation_id = ? AND m.id < ? ORDER BY m.id DESC LIMIT ?`,
        )
        .all(conversationId, cursor.before ?? '\uffff', limit)
        .map(toMessage)
        .toReversed()
    }
    const first = messages[0]
    const more = !!this.db
      .query<{ one: number }, [string, string]>(
        'SELECT 1 AS one FROM messages WHERE conversation_id = ? AND id < ? LIMIT 1',
      )
      .get(conversationId, first?.id ?? cursor.before ?? cursor.from ?? '')
    // Notices go with the page whose messages they follow: from the first message here
    // (or the very start) up to where the next page begins.
    const end =
      cursor.before === undefined
        ? undefined
        : this.db
            .query<{ created_at: number }, [string]>('SELECT created_at FROM messages WHERE id = ?')
            .get(cursor.before)?.created_at
    const notices = this.listNotices(conversationId, {
      ...(more && first ? { from: first.createdAt } : {}),
      ...(end === undefined ? {} : { to: end }),
    })
    return { messages, notices, more }
  }

  /**
   * What changed in a conversation after event `since`: messages posted or changed (an
   * edit, an answer to the question they ask), from `from` on, and notices. Pages already
   * shown are brought up to date with it instead of being fetched again.
   */
  private changedMessages(
    conversationId: string,
    cursor: { from?: string; since?: number },
  ): MessagePage {
    const since = cursor.since ?? 0
    const messages = this.db
      .query<MessageRow, [string, string, number, number]>(
        `${MESSAGE_SELECT} WHERE m.conversation_id = ? AND m.id >= ?
           AND (m.changed_seq > ? OR coalesce(a.changed_seq, 0) > ?) ORDER BY m.id`,
      )
      .all(conversationId, cursor.from ?? '', since, since)
      .map(toMessage)
    const notices = this.db
      .query<NoticeRow, [string, number]>(
        'SELECT * FROM notices WHERE conversation_id = ? AND changed_seq > ? ORDER BY created_at, id',
      )
      .all(conversationId, since)
      .map(toNotice)
    return { messages, notices, more: false }
  }

  /**
   * Find what the project said and made: messages of every conversation, threads by title
   * and latest headline, and artifacts. Every word of `query` must be there.
   */
  search(projectId: string, query: string): SearchResult {
    this.projectRow(projectId)
    const terms = searchTerms(query)
    const rows = (kind: SearchHit['kind']) =>
      searchRows(this.db, projectId, kind, terms, SEARCH_LIMITS[kind])
    const titles = new Map(
      this.db
        .query<{ id: string; title: string }, [string]>(
          'SELECT id, title FROM threads WHERE project_id = ?',
        )
        .all(projectId)
        .map((row) => [row.id, row.title]),
    )
    const messages = rows('message')
    const kinds = new Map(
      this.db
        .query<{ id: string; kind: ArtifactKind }, [string]>(
          'SELECT id, kind FROM artifacts WHERE project_id = ?',
        )
        .all(projectId)
        .map((row) => [row.id, row.kind]),
    )
    return {
      terms,
      threads: rows('thread').map((row) => ({
        kind: 'thread',
        id: row.ref_id,
        title: row.title,
        snippet: snippetOf(row.body, terms),
        at: row.at,
      })),
      artifacts: rows('artifact').map((row) => ({
        kind: 'artifact',
        id: row.ref_id,
        artifactKind: kinds.get(row.ref_id) ?? 'document',
        title: row.title,
        snippet: snippetOf(row.body, terms),
        at: row.at,
      })),
      messages: messages.map((row) => {
        const conversationId = row.conversation_id ?? ''
        return {
          kind: 'message',
          id: row.ref_id,
          conversationId,
          where: titles.get(conversationId) ?? null,
          snippet: snippetOf(row.body, terms),
          at: row.at,
        }
      }),
    }
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
   * that had seen up to it was built from. With `after`, only what came after event
   * `after`, to add to what was read before (see `messagesChangedSince`).
   */
  listTranscript(
    conversationId: string,
    options: { upTo?: number; after?: number } = {},
  ): TranscriptEntry[] {
    const upTo = options.upTo ?? Number.MAX_SAFE_INTEGER
    const rows = this.db
      .query<
        { seq: number; kind: string; ref: string | null; payload: string | null },
        [string, number, number]
      >(
        'SELECT seq, kind, ref, payload FROM transcript WHERE conversation_id = ? AND seq > ? AND seq <= ? ORDER BY id',
      )
      .all(conversationId, options.after ?? -1, upTo)
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
        case 'cleared':
          entries.push({ seq: row.seq, kind: 'cleared', ...payload })
          break
      }
    }
    return entries
  }

  /**
   * Whether a message of the conversation was changed (edited) after event `seq`: what
   * `listTranscript` gave before then no longer holds, and must be read again.
   */
  messagesChangedSince(conversationId: string, seq: number): boolean {
    return !!this.db
      .query<{ one: number }, [string, number]>(
        `SELECT 1 AS one FROM messages WHERE conversation_id = ? AND changed_seq > ?
           AND edited_at IS NOT NULL LIMIT 1`,
      )
      .get(conversationId, seq)
  }

  /**
   * Clear the long tool results a conversation's model read up to `upTo`, during one of
   * its turns, to make room. The log keeps them; the model reads a placeholder.
   */
  clearToolResults(
    conversationId: string,
    turnId: string,
    clearing: { upTo: number; sawSeq: number },
  ): void {
    const projectId = this.runningTurn(conversationId, turnId)
    this.store.append([
      {
        type: 'conversation.results_cleared',
        projectId,
        data: { conversationId, upTo: clearing.upTo, sawSeq: clearing.sawSeq },
      },
    ])
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
      reasoning: ReasoningRecord[]
      /** The todo list and open questions now, kept beside the notes. */
      state: string
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
      // A signature belongs to the summarising call and would not hold beside the notes.
      reasoning: compaction.reasoning
        .filter((block) => !block.redacted && block.text)
        .map((block) => ({ text: block.text })),
      state: compaction.state,
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

  /** The main model failed past its retries; the turn goes on with the fallback, `to`. */
  recordModelFallback(
    conversationId: string,
    turnId: string,
    fallback: { from: string; to: string; detail: string },
  ): void {
    const projectId = this.runningTurn(conversationId, turnId)
    this.store.append([
      {
        type: 'conversation.model_fallback',
        projectId,
        data: { conversationId, turnId, ...fallback },
      },
    ])
  }

  /**
   * The agent waits until `until` to look at `note`; a wait it had before is replaced.
   * Returns the new wait's id.
   */
  setWait(conversationId: string, wait: { until: number; note: string }): string {
    const conversation = this.getConversation(conversationId)
    const projectId = conversation.projectId
    const waitId = newId('wat')
    const before = conversation.wait
    this.store.append([
      ...(before
        ? [
            {
              type: 'conversation.wait_ended' as const,
              projectId,
              data: {
                conversationId,
                waitId: before.id,
                how: 'cancelled' as const,
                by: conversationId,
                until: before.until,
                note: before.note,
              },
            },
          ]
        : []),
      {
        type: 'conversation.wait_set',
        projectId,
        data: { conversationId, waitId, until: wait.until, note: wait.note },
      },
    ])
    return waitId
  }

  /**
   * End the conversation's wait: `due` when its time came, `now` when the user wakes it
   * early, `cancelled` to call it off (by `user` or the conversation itself). With `waitId`,
   * only that wait: a newer one stays. `false` when there was none to end.
   */
  endWait(
    conversationId: string,
    how: 'due' | 'now' | 'cancelled',
    by: string,
    waitId?: string,
  ): boolean {
    const conversation = this.getConversation(conversationId)
    const wait = conversation.wait
    if (!wait || (waitId !== undefined && wait.id !== waitId)) return false
    this.store.append([
      {
        type: 'conversation.wait_ended',
        projectId: conversation.projectId,
        data: { conversationId, waitId: wait.id, how, by, until: wait.until, note: wait.note },
      },
    ])
    return true
  }

  /** Every conversation's wait, soonest first. */
  listWaits(): { conversationId: string; projectId: string; waitId: string; until: number }[] {
    return this.db
      .query<{ id: string; project_id: string; wait_id: string; wait_until: number }, []>(
        `SELECT id, project_id, wait_id, wait_until FROM conversations
         WHERE wait_id IS NOT NULL ORDER BY wait_until`,
      )
      .all()
      .map((row) => ({
        conversationId: row.id,
        projectId: row.project_id,
        waitId: row.wait_id,
        until: row.wait_until,
      }))
  }

  /** The project's routine came due at `dueAt`: the coordinator is woken with its prompt. */
  runRoutine(projectId: string, routineId: string, dueAt: number): void {
    const routine = this.getProject(projectId).settings.routines.find((r) => r.id === routineId)
    if (!routine) throw new DomainError('error.notFound', { what: routineId })
    this.store.append([
      { type: 'routine.ran', projectId, data: { routineId, prompt: routine.prompt, dueAt } },
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
    this.projectRow(projectId)
    const trimmed = name.trim()
    if (!trimmed) throw new DomainError('error.invalidBody', { reason: 'name' })
    this.store.append([{ type: 'project.renamed', projectId, data: { name: trimmed } }])
    return this.getProject(projectId)
  }

  /**
   * Delete the project with its threads, conversations, plan, artifacts, memory and files.
   * Its turns must have ended first: a turn still running would write on into nothing.
   * Worktrees and the source's own directory are not here; the server sees to worktrees.
   */
  deleteProject(projectId: string): void {
    this.projectRow(projectId)
    const running = this.db
      .query<{ one: number }, [string]>(
        'SELECT 1 AS one FROM conversations WHERE project_id = ? AND turn_id IS NOT NULL',
      )
      .get(projectId)
    if (running) throw new DomainError('error.projectBusy')
    const threads = new Set(this.listThreads(projectId).map((thread) => thread.id))
    for (const [attentionId, threadId] of this.issueWritesUnderWay) {
      if (threads.has(threadId)) this.issueWritesUnderWay.delete(attentionId)
    }
    this.store.purge(projectId, { type: 'project.deleted', projectId, data: {} }, (db) => {
      db.run('DELETE FROM files WHERE project_id = ?', [projectId])
      db.run('DELETE FROM artifact_shared WHERE project_id = ?', [projectId])
    })
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

  /**
   * Replace the project's own settings. `tools` names the built-in tools that can be
   * turned off; the input may turn off only those.
   */
  setProjectSettings(projectId: string, input: unknown, tools: readonly string[]): ProjectView {
    const project = this.getProject(projectId)
    const settings = parseProjectSettings(input, tools)
    if (JSON.stringify(settings) === JSON.stringify(project.settings)) return project
    this.store.append([{ type: 'project.settings.set', projectId, data: { settings } }])
    return this.getProject(projectId)
  }

  /**
   * Replace the project's routines, checked like the rest of its settings. `change` is the
   * one an agent added or removed: its conversation gets a line saying so.
   */
  setRoutines(
    projectId: string,
    routines: unknown,
    change?: { by: string; action: 'added' | 'removed'; prompt: string },
  ): ProjectView {
    const { settings } = this.getProject(projectId)
    const next = { ...settings, routines: parseRoutines(routines) }
    if (JSON.stringify(next) === JSON.stringify(settings)) return this.getProject(projectId)
    this.store.append([
      {
        type: 'project.settings.set',
        projectId,
        data: { settings: next, ...(change ? { routine: change } : {}) },
      },
    ])
    return this.getProject(projectId)
  }

  /**
   * Keep the way the user last merged in this project: the next merge starts from it, and
   * the autopilot merges the same way.
   */
  rememberMergeMethod(projectId: string, method: MergeMethod): void {
    const { settings } = this.getProject(projectId)
    if (settings.autopilot.method === method) return
    const next = { ...settings, autopilot: { ...settings.autopilot, method } }
    this.store.append([{ type: 'project.settings.set', projectId, data: { settings: next } }])
  }

  /** Pause the project's autopilot, or let it go on. */
  setAutopilotPaused(projectId: string, paused: boolean): ProjectView {
    const project = this.getProject(projectId)
    if (!project.settings.autopilot.enabled) throw new DomainError('error.autopilotOff')
    if (project.autopilotPaused === paused) return project
    this.store.append([{ type: 'project.autopilot.paused', projectId, data: { paused } }])
    return this.getProject(projectId)
  }

  /**
   * Milestones' gates by node: `passed` once the user agreed to go on past one, `open`
   * while the question waits.
   */
  private milestoneGates(projectId: string): Map<string, 'open' | 'passed'> {
    const gates = new Map<string, 'open' | 'passed'>()
    for (const row of this.db
      .query<{ purpose: string; closed: number; approved: number | null }, [string, string]>(
        `SELECT purpose, closed, approved FROM attentions WHERE project_id = ? AND purpose LIKE ?
         ORDER BY created_at, id`,
      )
      .all(projectId, `${MILESTONE_GATE}:%`)) {
      const nodeId = row.purpose.slice(MILESTONE_GATE.length + 1)
      if (row.closed === 1 && row.approved === 1) gates.set(nodeId, 'passed')
      else if (row.closed === 0 && gates.get(nodeId) !== 'passed') gates.set(nodeId, 'open')
    }
    return gates
  }

  /**
   * Whether the autopilot may ask about a milestone's gate now: not while a card is open
   * or after the user agreed, and not after a no since the autopilot last went on (being
   * switched on, or resumed after a pause, asks again). Before and after are the order of
   * the events, not their times: two can fall in the same millisecond.
   */
  mayAskGate(projectId: string, nodeId: string): boolean {
    const since =
      this.db
        .query<{ autopilot_since: number | null }, [string]>(
          'SELECT autopilot_since FROM projects WHERE id = ?',
        )
        .get(projectId)?.autopilot_since ?? 0
    const rows = this.db
      .query<{ closed: number; approved: number | null; changed_seq: number }, [string, string]>(
        'SELECT closed, approved, changed_seq FROM attentions WHERE project_id = ? AND purpose = ?',
      )
      .all(projectId, `${MILESTONE_GATE}:${nodeId}`)
    return !rows.some(
      (row) =>
        row.closed === 0 ||
        row.approved === 1 ||
        // A question taken back (its milestone left the plan) is not a no.
        (row.approved === 0 && row.changed_seq > since),
    )
  }

  /** Ask the user whether the work goes on past a milestone; the coordinator hears the answer. */
  askGate(projectId: string, nodeId: string, block: ConfirmBlock): AttentionView {
    this.assertNode(projectId, nodeId)
    return this.openAttention(projectId, { block, purpose: `${MILESTONE_GATE}:${nodeId}` })
  }

  /** Gates still asking about milestones that are gone from the plan, by attention. */
  staleGates(projectId: string): string[] {
    const graph = loadPlanGraph(this.db, projectId)
    return this.db
      .query<{ id: string; purpose: string }, [string, string]>(
        'SELECT id, purpose FROM attentions WHERE project_id = ? AND closed = 0 AND purpose LIKE ?',
      )
      .all(projectId, `${MILESTONE_GATE}:%`)
      .filter((row) => {
        const node = graph.nodes.get(row.purpose.slice(MILESTONE_GATE.length + 1))
        return node?.kind !== 'milestone'
      })
      .map((row) => row.id)
  }

  /** Take back a question the system asked, e.g. a gate whose milestone was removed. */
  withdrawSystemAttention(projectId: string, attentionId: string, reason: string): void {
    const row = this.db
      .query<{ closed: number; message_id: string | null }, [string, string]>(
        'SELECT closed, message_id FROM attentions WHERE id = ? AND project_id = ?',
      )
      .get(attentionId, projectId)
    if (!row || row.message_id !== null || row.closed === 1) return
    this.store.append([{ type: 'attention.withdrawn', projectId, data: { attentionId, reason } }])
  }

  /**
   * The autopilot leaves changes that passed the review for the user to merge; they stay in
   * "needs me" as they would without it, and the thread says why.
   */
  holdAutopilot(
    threadId: string,
    input: { head: string | null; reason: AutopilotHold; detail: string },
  ): void {
    const thread = this.getThread(threadId)
    this.store.append([
      {
        type: 'autopilot.held',
        projectId: thread.projectId,
        data: { threadId, head: input.head, reason: input.reason, detail: input.detail },
      },
    ])
  }

  createThread(
    projectId: string,
    input: {
      title: string
      nodeId?: string | null
      model?: string | null
      branch?: string | null
      /** Opened by the autopilot. */
      auto?: boolean
    },
  ): ThreadView {
    this.projectRow(projectId)
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
        data: {
          threadId,
          title,
          nodeId: input.nodeId ?? null,
          model: input.model ?? null,
          branch: branchSlug(input.branch),
          ...(input.auto ? { auto: true as const } : {}),
        },
      },
    ])
    return this.getThread(threadId)
  }

  /**
   * Open a thread for the autopilot on a plan node, with its first message: both are written
   * together, so a crash never leaves a thread waiting for a brief that never comes.
   */
  startAutoThread(
    projectId: string,
    input: { title: string; nodeId: string; brief: string },
  ): ThreadView {
    return this.transaction(() => {
      const thread = this.createThread(projectId, {
        title: input.title,
        nodeId: input.nodeId,
        auto: true,
      })
      this.postMessage(projectId, {
        conversationId: thread.id,
        author: 'coordinator',
        kind: 'relay',
        content: input.brief,
      })
      return this.getThread(thread.id)
    })
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
    this.projectRow(projectId)
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
   * The user's yes for one call by one thread (an external tool, a command): `purpose` names
   * the call and its exact arguments. A yes is used for one call (`useApproval`), and the
   * same call later asks again. A no holds until the user writes in the thread again, so
   * the thread does not ask the same thing twice on its own.
   */
  callApproval(
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
           (SELECT 1 FROM used_approvals c WHERE c.attention_id = a.id) AS used
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
  useApproval(
    projectId: string,
    input: { threadId: string; attentionId: string; what: string },
  ): void {
    this.assertThreadIn(projectId, input.threadId)
    const used = this.db
      .query<{ one: number }, [string]>(
        'SELECT 1 AS one FROM used_approvals WHERE attention_id = ?',
      )
      .get(input.attentionId)
    if (used) throw new DomainError('error.invalidBody', { reason: 'approval used' })
    this.store.append([{ type: 'approval.used', projectId, data: input }])
  }

  stopThread(threadId: string, input: { reason: string; by: string }): ThreadView {
    const thread = this.getThread(threadId)
    // Stopping again changes nothing, except the user taking over the coordinator's stop:
    // then only the user can lift it. Without a reason of their own, the one given stays.
    const takeover = !!thread.stopped && input.by === 'user' && thread.stopped.by !== 'user'
    if (thread.stopped && !takeover) return thread
    const reason = input.reason.trim() || (takeover ? (thread.stopped?.reason ?? '') : '')
    if (!reason) throw new DomainError('error.invalidBody', { reason: 'reason' })
    // A stopped thread does not wait for anything: its wait goes with the stop.
    const wait = this.getConversation(threadId).wait
    this.store.append([
      ...(wait
        ? [
            {
              type: 'conversation.wait_ended' as const,
              projectId: thread.projectId,
              data: {
                conversationId: threadId,
                waitId: wait.id,
                how: 'cancelled' as const,
                by: input.by,
                until: wait.until,
                note: wait.note,
              },
            },
          ]
        : []),
      {
        type: 'thread.stopped',
        projectId: thread.projectId,
        data: { threadId, reason, by: input.by },
      },
      // A stopped reviewer lets the round under way go.
      ...(thread.stopped ? [] : this.abandonedReview(threadId)),
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

  /** The user is through with what the thread put up for review; it counts as done. */
  reviewThread(threadId: string): ThreadView {
    const thread = this.getThread(threadId)
    if (thread.stage !== 'review') throw new DomainError('error.threadNotInReview')
    this.store.append([
      { type: 'thread.reviewed', projectId: thread.projectId, data: { threadId } },
    ])
    return this.getThread(threadId)
  }

  /**
   * `head` is the branch's newest commit, to tell whether merged work is all it reports.
   * A `ready` with `review.reviewable` (a branch with changes to merge) goes to the
   * project's pre-delivery review first, while rounds are left; `replies` answer the
   * comments of a round that asked for changes, which a `ready` must do.
   */
  reportThread(
    threadId: string,
    report: ThreadReport,
    head?: string,
    review: {
      reviewable?: boolean
      /** The branch's newest commit, which the review reads. */
      head?: string
      replies?: ReviewReply[]
      reviewerTitle?: string
    } = {},
  ): ThreadView {
    const thread = this.getThread(threadId)
    const summary = report.summary.trim()
    const headline = report.headline.trim()
    if (!summary) throw new DomainError('error.invalidBody', { reason: 'summary' })
    if (!headline) throw new DomainError('error.invalidBody', { reason: 'headline' })
    const artifacts = [...new Set(report.artifacts ?? [])]
    for (const id of artifacts) {
      if (this.getArtifact(id).projectId !== thread.projectId) {
        throw new DomainError('error.notFound', { what: id })
      }
    }
    const events: AnyNewEvent[] = []
    const projectId = thread.projectId
    const open = this.openReviewRound(threadId)
    if (report.kind === 'ready' && open) {
      const replies = reviewReplies(review.replies ?? [], open.comments.length)
      events.push({
        type: 'review.replied',
        projectId,
        data: { threadId, round: open.round, replies },
      })
    }
    const { reviewing, round } = this.nextReviewRound(thread, report.kind, !!review.reviewable)
    events.push({
      type: 'thread.reported',
      projectId,
      data: {
        threadId,
        report: report.kind,
        summary,
        headline,
        artifacts,
        ...(head ? { head } : {}),
        ...(reviewing ? { reviewing: true } : {}),
        ...(!reviewing && report.kind === 'ready' && round > 0 ? { reviewRounds: round } : {}),
      },
    })
    // A round already under way reads the newer commits by itself.
    if (reviewing && !thread.review?.reviewing) {
      let reviewerId = thread.review?.reviewerId
      if (!reviewerId) {
        reviewerId = newId('thr')
        events.push({
          type: 'thread.created',
          projectId,
          data: {
            threadId: reviewerId,
            title: review.reviewerTitle?.trim() || thread.title,
            nodeId: null,
            model: thread.model,
            branch: null,
            reviewOf: threadId,
          },
        })
        events.push({ type: 'thread.started', projectId, data: { threadId: reviewerId } })
      }
      events.push({
        type: 'review.requested',
        projectId,
        data: { threadId, reviewerId, round, head: review.head ?? head ?? null },
      })
    }
    this.store.append(events)
    return this.getThread(threadId)
  }

  /**
   * Whether a report goes to review, and the round: the next one of this delivery while
   * rounds are left, else the rounds it went through (0 for none) as it goes to the user.
   */
  private nextReviewRound(
    thread: ThreadView,
    kind: ReportKind,
    reviewable: boolean,
  ): { reviewing: boolean; round: number } {
    if (kind !== 'ready' || thread.reviewOf) return { reviewing: false, round: 0 }
    const row = this.db
      .query<{ review_rounds: number; review_delivered: number; reviewing: number }, [string]>(
        'SELECT review_rounds, review_delivered, reviewing FROM threads WHERE id = ?',
      )
      .get(thread.id)
    const rounds = row && row.review_delivered === 0 ? row.review_rounds : 0
    if (row?.reviewing === 1 && thread.review?.reviewing) return { reviewing: true, round: rounds }
    const enabled = this.getProject(thread.projectId).settings.review.enabled
    // A reviewer someone stopped reviews nothing more until it is resumed.
    const reviewer = thread.review ? this.getThread(thread.review.reviewerId) : null
    if (reviewable && enabled && !reviewer?.stopped && rounds < MAX_REVIEW_ROUNDS) {
      return { reviewing: true, round: rounds + 1 }
    }
    return { reviewing: false, round: rounds }
  }

  /** The thread's newest review round, when it asked for changes the author has not answered. */
  openReviewRound(threadId: string): ReviewRoundView | null {
    // A delivery that ended (reached the user, or the thread reported done or failed)
    // leaves its comments behind.
    const delivered = this.db
      .query<{ review_delivered: number }, [string]>(
        'SELECT review_delivered FROM threads WHERE id = ?',
      )
      .get(threadId)?.review_delivered
    if (delivered !== 0) return null
    const latest = this.listReviews(threadId).at(-1)
    return latest?.verdict === 'changes' && latest.replies === null ? latest : null
  }

  /**
   * `review.abandoned` for the round a reviewer leaves without a verdict, if it reviews one;
   * none for other conversations.
   */
  private abandonedReview(conversationId: string): AnyNewEvent[] {
    const row = this.db
      .query<{ review_of: string | null; project_id: string }, [string]>(
        'SELECT review_of, project_id FROM threads WHERE id = ?',
      )
      .get(conversationId)
    if (!row?.review_of) return []
    const author = this.db
      .query<{ reviewing: number }, [string]>('SELECT reviewing FROM threads WHERE id = ?')
      .get(row.review_of)
    const round = this.listReviews(row.review_of).at(-1)
    if (author?.reviewing !== 1 || !round || round.verdict !== null || round.abandoned) return []
    if (round.reviewerId !== conversationId) return []
    return [
      {
        type: 'review.abandoned',
        projectId: row.project_id,
        data: { threadId: row.review_of, reviewerId: conversationId, round: round.round },
      },
    ]
  }

  /**
   * The round that let the thread's changes now up for review through: its latest, when it
   * passed after the thread last reported. `null` for changes that reached the user any
   * other way (no review, a round left without a verdict, comments left after the last round).
   */
  passedReview(threadId: string): ReviewRoundView | null {
    const thread = this.getThread(threadId)
    if (thread.stage !== 'review' || thread.reviewOf) return null
    const reportedAt =
      this.db
        .query<{ reported_at: number | null }, [string]>(
          'SELECT reported_at FROM threads WHERE id = ?',
        )
        .get(threadId)?.reported_at ?? 0
    const round = this.listReviews(threadId).at(-1)
    if (round?.verdict !== 'pass' || (round.submittedAt ?? 0) < reportedAt) return null
    return round
  }

  /** The line comments the user sent a thread, oldest first, with when each went. */
  listLineComments(threadId: string): SentLineComment[] {
    this.getThread(threadId)
    const rows = this.db
      .query<{ id: string; comments: string; created_at: number }, [string]>(
        `SELECT id, comments, created_at FROM messages
         WHERE conversation_id = ? AND author = 'user' AND comments IS NOT NULL ORDER BY id`,
      )
      .all(threadId)
    return rows.flatMap((row) =>
      (JSON.parse(row.comments) as LineComment[]).map((comment, index) => ({
        ...comment,
        id: `${row.id}:${index}`,
        messageId: row.id,
        sentAt: row.created_at,
      })),
    )
  }

  /** The review rounds of a thread's changes, oldest first; a reviewer gives its author's. */
  listReviews(threadId: string): ReviewRoundView[] {
    const thread = this.getThread(threadId)
    const author = thread.reviewOf ?? thread.id
    return this.db
      .query<ReviewRow, [string]>('SELECT * FROM reviews WHERE thread_id = ? ORDER BY id')
      .all(author)
      .map(toReviewRound)
  }

  /**
   * A reviewing thread's verdict on the round under way. `pass` sends the author's changes
   * to the user; `changes` sends the comments to the author. `headline` is what the
   * reviewer's own row says afterwards.
   */
  submitReview(
    reviewerId: string,
    input: { verdict: ReviewVerdict; summary: string; comments: ReviewComment[]; headline: string },
  ): ReviewRoundView {
    const reviewer = this.getThread(reviewerId)
    const authorId = reviewer.reviewOf
    if (!authorId) throw new DomainError('error.notReviewer')
    const round = this.listReviews(authorId).at(-1)
    if (!round || round.verdict !== null || round.abandoned || round.reviewerId !== reviewerId) {
      throw new DomainError('error.noReviewRound')
    }
    const summary = input.summary.trim()
    if (!summary) throw new DomainError('error.invalidBody', { reason: 'summary' })
    if (input.verdict !== 'pass' && input.verdict !== 'changes') {
      throw new DomainError('error.invalidBody', { reason: 'verdict' })
    }
    const comments = reviewComments(input.comments)
    if ((input.verdict === 'changes') !== comments.length > 0) {
      throw new DomainError('error.invalidBody', { reason: 'comments' })
    }
    const { projectId } = reviewer
    this.store.append([
      {
        type: 'review.submitted',
        projectId,
        data: {
          threadId: authorId,
          reviewerId,
          round: round.round,
          verdict: input.verdict,
          summary,
          comments,
        },
      },
      {
        type: 'thread.reported',
        projectId,
        data: {
          threadId: reviewerId,
          report: 'done',
          summary,
          headline: input.headline.trim() || summary.slice(0, 60),
          artifacts: [],
        },
      },
    ])
    return this.listReviews(authorId).at(-1)!
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
    this.projectRow(projectId)
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
        const before = loadPlanGraph(this.db, row.project_id)
        const after = applyOps(before, ops).graph
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
        events.push(
          ...this.stopsForRemoved(row.project_id, before, after, proposal.reason, 'coordinator'),
        )
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
      /** What the user pointed at with `@`; threads and artifacts are titled from the project. */
      mentions?: unknown[]
      /** The user's comments on lines of a thread's changes. */
      comments?: unknown[]
      /** The id its draft was shown under while the model wrote it. */
      messageId?: string
    },
  ): MessageView {
    this.assertConversationIn(projectId, input.conversationId)
    const attachments = this.attachmentsOf(projectId, input.attachments ?? [])
    if (attachments.length && input.author !== 'user') {
      throw new DomainError('error.invalidBody', { reason: 'attachments' })
    }
    const mentions = this.mentionsOf(projectId, input.mentions ?? [])
    const comments = lineComments(input.comments ?? [])
    if ((mentions.length || comments.length) && input.author !== 'user') {
      throw new DomainError('error.invalidBody', {
        reason: mentions.length ? 'mentions' : 'comments',
      })
    }
    if (comments.length && this.projectRow(projectId).coordinator_id === input.conversationId) {
      throw new DomainError('error.invalidBody', { reason: 'comments' })
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
    if (!input.content.trim() && !parsed.length && !attachments.length && !comments.length) {
      throw new DomainError('error.invalidBody', { reason: 'content' })
    }
    const messageId = input.messageId ?? newId('msg')
    const asker =
      this.projectRow(projectId).coordinator_id === input.conversationId
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
          ...(mentions.length ? { mentions } : {}),
          ...(comments.length ? { comments } : {}),
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

  /**
   * Mentions checked against the project: threads and artifacts must be its own and take
   * their titles from it; a file is a path relative to the workspace, which the agent
   * reads itself.
   */
  private mentionsOf(projectId: string, given: unknown[]): MessageMention[] {
    if (!Array.isArray(given)) throw new DomainError('error.invalidBody', { reason: 'mentions' })
    if (given.length > MAX_MENTIONS) {
      throw new DomainError('error.invalidBody', { reason: 'mentions' })
    }
    const seen = new Set<string>()
    const mentions: MessageMention[] = []
    for (const item of given) {
      const { kind, id, path } = (item ?? {}) as Record<string, unknown>
      let mention: MessageMention
      if (kind === 'file') {
        const clean = typeof path === 'string' ? path.trim().replace(/^\.\/+/, '') : ''
        if (!clean || clean.length > 1000 || /[\0\n\r]/.test(clean) || clean.startsWith('/')) {
          throw new DomainError('error.invalidBody', { reason: 'mentions' })
        }
        mention = { kind, path: clean }
      } else if (kind === 'skill') {
        const { name } = (item ?? {}) as Record<string, unknown>
        if (typeof name !== 'string' || !SKILL_NAME.test(name)) {
          throw new DomainError('error.invalidBody', { reason: 'mentions' })
        }
        mention = { kind, name }
      } else if (kind === 'thread' || kind === 'artifact') {
        const table = kind === 'thread' ? 'threads' : 'artifacts'
        const row =
          typeof id === 'string'
            ? this.db
                .query<{ title: string }, [string, string]>(
                  `SELECT title FROM ${table} WHERE id = ? AND project_id = ?`,
                )
                .get(id, projectId)
            : null
        if (!row)
          throw new DomainError('error.notFound', { what: typeof id === 'string' ? id : kind })
        mention = { kind, id: id as string, title: row.title }
      } else {
        throw new DomainError('error.invalidBody', { reason: 'mentions' })
      }
      const key =
        mention.kind === 'file'
          ? `file:${mention.path}`
          : mention.kind === 'skill'
            ? `skill:${mention.name}`
            : `${mention.kind}:${mention.id}`
      if (seen.has(key)) continue
      seen.add(key)
      mentions.push(mention)
    }
    return mentions
  }

  /**
   * Record what a turn changed in its thread's workspace. A turn that changed nothing
   * records nothing.
   */
  recordTurnChanges(
    conversationId: string,
    turnId: string,
    input: { from: string; to: string; files: TurnFile[]; branch?: ChangeTotals },
  ): void {
    if (!input.files.length) return
    const projectId = this.runningTurn(conversationId, turnId)
    const startedAt = this.getConversation(conversationId).turn?.startedAt
    const sum = (key: 'added' | 'removed') =>
      input.files.reduce((total, file) => total + (file[key] ?? 0), 0)
    this.store.append([
      {
        type: 'turn.changed',
        projectId,
        data: {
          conversationId,
          turnId,
          from: input.from,
          to: input.to,
          files: input.files.slice(0, MAX_TURN_FILES),
          total: { files: input.files.length, added: sum('added'), removed: sum('removed') },
          ...(startedAt ? { startedAt } : {}),
          ...(input.branch ? { branch: input.branch } : {}),
        },
      },
    ])
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
    // Opening a conversation again says the same as last time: nothing to record.
    const unseen = this.db
      .query<{ n: number }, [string, string]>(
        `SELECT 1 AS n FROM messages WHERE conversation_id = ? AND id <= ? AND author != 'user'
           AND (read_at IS NULL OR edited_since_read = 1) LIMIT 1`,
      )
      .get(conversationId, upTo)
    if (!unseen) return
    const projectId = this.projectOfMessage(upTo)
    this.store.append([{ type: 'message.read', projectId, data: { conversationId, upTo } }])
  }

  /**
   * The user is back at the project after `awayMs` without touching it. Away for long
   * enough, with threads that finished, were merged or came to need them meanwhile, it is
   * recorded, and the coordinator's conversation shows it as a recap; otherwise nothing
   * is. Read from the log and the projections alone: no model is asked.
   */
  noteReturn(projectId: string, awayMs: number): void {
    const project = this.projectRow(projectId)
    if (!Number.isFinite(awayMs) || awayMs < AWAY_MS) return
    const now = this.now()
    // What an earlier recap told is not told again.
    const told =
      this.db
        .query<{ at: number }, [string]>(
          `SELECT created_at AS at FROM notices WHERE conversation_id = ? AND kind = 'recap'
           ORDER BY created_at DESC LIMIT 1`,
        )
        .get(project.coordinator_id)?.at ?? 0
    const since = Math.max(now - awayMs, told)
    if (now - since < AWAY_MS) return

    const threads = this.listThreads(projectId)
    const byId = new Map(threads.map((thread) => [thread.id, thread]))
    const reported = new Set<string>()
    const landed = new Set<string>()
    const rows = this.db
      .query<{ type: string; data: string }, [string, number]>(
        `SELECT type, data FROM events WHERE project_id = ? AND seq >= ?
           AND type IN ('thread.reported', 'branch.merged', 'pull_request.closed')
         ORDER BY seq`,
      )
      .all(projectId, this.firstSeqAfter(since))
    for (const row of rows) {
      const data = JSON.parse(row.data) as { threadId: string; report?: string; merged?: boolean }
      if (row.type === 'thread.reported') {
        if (data.report === 'done') reported.add(data.threadId)
      } else if (row.type === 'branch.merged' || data.merged) {
        landed.add(data.threadId)
      }
    }

    // What waits on the user now and did not when they left.
    const waiting = new Set<string>()
    const asks: string[] = []
    for (const item of this.needsMeOf(projectId, threads)) {
      if (item.kind === 'thread') {
        if (Math.max(item.thread.activeAt, item.thread.updatedAt) > since) {
          waiting.add(item.thread.id)
        }
      } else if (item.attention.createdAt > since) {
        const askers = [item.thread, ...item.joined].flatMap((thread) => thread ?? [])
        for (const thread of askers) waiting.add(thread.id)
        if (!askers.length) asks.push(item.attention.id)
      }
    }
    // Each thread is told once, by what matters most of it: a review's own thread never.
    const tells = (id: string) => !!byId.get(id) && !byId.get(id)!.reviewOf && !waiting.has(id)
    const merged = [...landed].filter(tells)
    const done = [...reported].filter(
      (id) => tells(id) && !landed.has(id) && byId.get(id)!.stage === 'done',
    )
    if (!done.length && !merged.length && !waiting.size && !asks.length) return
    this.store.append([
      {
        type: 'user.returned',
        projectId,
        data: { since, done, merged, waiting: [...waiting], asks },
      },
    ])
  }

  /** The first event written after `at`; one past the newest when there is none. */
  private firstSeqAfter(at: number): number {
    const first = this.db.query<{ seq: number; at: number }, [number]>(
      'SELECT seq, at FROM events WHERE seq >= ? ORDER BY seq LIMIT 1',
    )
    // Events are written in the order of their times, so the log is searched by halves.
    let low = 1
    let high = this.store.lastSeq() + 1
    while (low < high) {
      const middle = Math.floor((low + high) / 2)
      const event = first.get(middle)
      if (!event || event.at > at) high = middle
      else low = event.seq + 1
    }
    return low
  }

  // ---- memory ----

  listMemories(projectId: string): MemoryView[] {
    this.projectRow(projectId)
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

  /**
   * What happened in a conversation besides its messages, oldest first; with `range`, only
   * from `from` (inclusive) to `to` (exclusive), in ms.
   */
  listNotices(conversationId: string, range: { from?: number; to?: number } = {}): NoticeView[] {
    return this.db
      .query<NoticeRow, [string, number, number]>(
        // Notices of the same millisecond in the order they were written: ids are random.
        `SELECT * FROM notices WHERE conversation_id = ? AND created_at >= ? AND created_at < ?
         ORDER BY created_at, rowid`,
      )
      .all(conversationId, range.from ?? -1, range.to ?? Number.MAX_SAFE_INTEGER)
      .map(toNotice)
  }

  /**
   * The user takes back an agent's change to the project memory: an added entry goes, a
   * changed one gets its old text back, a removed one returns.
   */
  undoMemoryNotice(projectId: string, noticeId: string): void {
    this.store.transaction(() => this.undoMemoryNoticeNow(projectId, noticeId))
  }

  private undoMemoryNoticeNow(projectId: string, noticeId: string): void {
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
    this.projectRow(projectId)
    this.store.append([{ type: 'project.source.set', projectId, data: { source } }])
    return this.getProject(projectId)
  }

  /**
   * Record the install commands a new workspace's lockfiles called for (one a line, `''`
   * for none), when they are not the ones known: the settings show them.
   */
  recordDetectedSetup(projectId: string, setup: string): void {
    if (this.getProject(projectId).detected.setup === setup) return
    this.store.append([{ type: 'project.setup.detected', projectId, data: { setup } }])
  }

  /**
   * Whether `threadId` may note the project's check after edits: the user left it to
   * Ariadne, and no other thread noted one. The thread that did may correct its own. Not
   * in a project whose threads run no commands: a check is a command of the thread's choosing.
   */
  canNoteCheck(threadId: string): boolean {
    const thread = this.getThread(threadId)
    if (thread.reviewOf) return false
    const project = this.getProject(thread.projectId)
    if (!project.settings.workspace.auto.check) return false
    if (project.settings.tools.includes('commands')) return false
    return !project.detected.check || project.detected.check.threadId === threadId
  }

  /** A thread notes the check to run after edits; `''` when the repository has none fit. */
  noteCheck(threadId: string, command: string): ProjectView {
    const thread = this.getThread(threadId)
    const project = this.getProject(thread.projectId)
    if (!this.canNoteCheck(threadId)) {
      throw new DomainError('error.invalidBody', { reason: 'check' })
    }
    const noted = command.trim()
    if (noted.length > MAX_CHECK_COMMAND) {
      throw new DomainError('error.invalidBody', { reason: 'command' })
    }
    if (project.detected.check?.command === noted) return project
    this.store.append([
      {
        type: 'project.check.noted',
        projectId: thread.projectId,
        data: { command: noted, threadId },
      },
    ])
    return this.getProject(thread.projectId)
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

  /** The user took the thread's last merge back: its changes wait to be merged again. */
  recordMergeUndone(threadId: string): ThreadView {
    const thread = this.getThread(threadId)
    if (!thread.merged) throw new DomainError('error.undoMerge.none')
    const { branch, base, sha, head } = thread.merged
    this.store.append([
      {
        type: 'branch.merge_undone',
        projectId: thread.projectId,
        data: { threadId, branch, base, sha, head },
      },
    ])
    return this.getThread(threadId)
  }

  /** A merge the user tried stopped at conflicts: the thread is woken to resolve them. */
  recordMergeConflict(
    threadId: string,
    conflict: { branch: string; base: string; files: string[]; pullRequest?: number },
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
  syncPullRequest(
    threadId: string,
    pullRequest: ThreadPullRequest,
    /** The autopilot just merged it, as this says. */
    auto?: { method: MergeMethod; base: string; sha: string | null },
  ): void {
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
        data: {
          threadId,
          number: pullRequest.number,
          merged: pullRequest.state === 'merged',
          ...(auto && pullRequest.state === 'merged' ? { auto } : {}),
        },
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
    this.projectRow(projectId)
    const graph = loadPlanGraph(this.db, projectId)
    const changedIn = new Map(
      this.db
        .query<{ id: string; changed_in: string | null }, [string]>(
          'SELECT id, changed_in FROM plan_nodes WHERE project_id = ?',
        )
        .all(projectId)
        .map((row) => [row.id, row.changed_in]),
    )
    const onNodes = new Map<string, ThreadView[]>()
    for (const thread of this.listThreads(projectId)) {
      if (thread.nodeId === null) continue
      const onNode = onNodes.get(thread.nodeId)
      if (onNode) onNode.push(thread)
      else onNodes.set(thread.nodeId, [thread])
    }
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
    const gates = this.milestoneGates(projectId)
    const nodes = graph.order.map((id): PlanNodeView => {
      const node = graph.nodes.get(id) as NonNullable<ReturnType<typeof graph.nodes.get>>
      const onNode = onNodes.get(id) ?? []
      return {
        id,
        ...node,
        dependsOn: dependsOn.get(id) ?? [],
        status: nodeStatus(
          onNode.map((thread) => thread.stage),
          node.kind === 'milestone' ? (gates.get(id) ?? null) : null,
        ),
        threads: onNode.map((thread) => ({
          id: thread.id,
          title: thread.title,
          stage: thread.stage,
          stoppedBy: thread.stopped?.by ?? null,
          lastHeadline: thread.lastHeadline,
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
        gate: node.kind === 'milestone' ? (gates.get(id) ?? null) : null,
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
    this.projectRow(projectId)
    const reason = input.reason.trim()
    if (!reason) throw new DomainError('error.invalidBody', { reason: 'reason' })
    if (!input.ops.length) throw new DomainError('error.invalidBody', { reason: 'ops' })
    return this.changePlan(projectId, input.author, reason, input.ops, null, input.confirmation)
  }

  /** What a change would do, one line per op, as the user would read it. */
  describePlanChange(projectId: string, ops: PlanOp[]): LocalizedMessage[] {
    // Only the coordinator's proposals are described this way; agreeing to one stops
    // threads as the coordinator.
    const threads = this.threadsToStop(projectId, 'coordinator')
    return describePlanOps(
      loadPlanGraph(this.db, projectId),
      ops,
      (nodeId) => threads.filter((thread) => thread.nodeId === nodeId).length,
    )
  }

  /**
   * Threads a removed node stops: those not done and not stopped yet, and when the user
   * removes it, also those the coordinator stopped (the user takes the stop over, as in
   * `stopThread`, so only the user can let them go on).
   */
  private threadsToStop(projectId: string, by: string): ThreadView[] {
    return this.listThreads(projectId).filter(
      (thread) =>
        thread.stage !== 'done' &&
        (!thread.stopped || (by === 'user' && thread.stopped.by !== 'user')),
    )
  }

  /**
   * A node that leaves the plan stops the threads still working on it, so nothing goes on
   * for work the plan no longer has. They are not resumed when the node comes back.
   */
  private stopsForRemoved(
    projectId: string,
    before: PlanGraph,
    after: PlanGraph,
    reason: string,
    by: string,
  ): AnyNewEvent[] {
    const removed = new Set(removedNodes(before, after))
    if (!removed.size) return []
    return this.threadsToStop(projectId, by)
      .filter((thread) => thread.nodeId && removed.has(thread.nodeId))
      .map((thread) => ({
        type: 'thread.stopped' as const,
        projectId,
        data: { threadId: thread.id, reason, by },
      }))
  }

  /**
   * What undoing a revision would do, for the user to judge before they undo it: the
   * changes, dependencies later changes added that go with removed nodes, and the threads
   * it stops. `blocked` says why it can no longer be undone.
   */
  previewPlanUndo(projectId: string, revisionId: string): PlanUndoPreview {
    const { graph, undo, titles } = this.undoOf(projectId, revisionId)
    const blocked = whyUndoFails(graph, undo, titles)
    if (blocked) return { changes: [], threads: [], blocked }
    const after = applyOps(graph, undo).graph
    const title = (id: string) => graph.nodes.get(id)?.title ?? id
    // Threads are listed once, below; the lines only say what changes.
    const changes = [
      ...describePlanOps(graph, undo, () => 0),
      ...droppedWith(graph, after, undo).map(({ from, to }): LocalizedMessage => ({
        key: 'plan.op.unlink',
        params: { from: title(from), to: title(to) },
      })),
    ]
    const removed = new Set(removedNodes(graph, after))
    const threads = this.threadsToStop(projectId, 'user')
      .filter((thread) => thread.nodeId && removed.has(thread.nodeId))
      .map((thread) => ({ id: thread.id, title: thread.title }))
    return { changes, threads, blocked: null }
  }

  private undoOf(projectId: string, revisionId: string) {
    const row = this.db
      .query<
        {
          ops: string
          inverse: string
          reverted_by: string | null
          reason: string
          undone_is_undo: number | null
        },
        [string, string]
      >(
        `SELECT r.ops, r.inverse, r.reverted_by, r.reason, u.revert_of IS NOT NULL AS undone_is_undo
         FROM plan_revisions r LEFT JOIN plan_revisions u ON u.id = r.revert_of
         WHERE r.id = ? AND r.project_id = ?`,
      )
      .get(revisionId, projectId)
    if (!row) throw new DomainError('error.notFound', { what: revisionId })
    if (row.reverted_by) throw new DomainError('error.planReverted')
    // Undo goes one level deep: a change, or the undo that took it back (restoring it). A
    // restore is undone by undoing the original change again, so every change's state is
    // read off the change itself.
    if (row.undone_is_undo) throw new DomainError('error.planUndoRestore')
    const undo = JSON.parse(row.inverse) as PlanOp[]
    // Names for nodes the plan may no longer have, from what the revision itself held.
    const titles = new Map<string, string>()
    for (const op of [...undo, ...(JSON.parse(row.ops) as PlanOp[])]) {
      if ((op.op === 'add' || op.op === 'update') && op.title) titles.set(op.id, op.title)
    }
    return { graph: loadPlanGraph(this.db, projectId), undo, titles, reason: row.reason }
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
    const { graph, undo, titles, reason: original } = this.undoOf(projectId, revisionId)
    const blocked = whyUndoFails(graph, undo, titles)
    if (blocked) throw new DomainError(blocked.key, blocked.params)
    return this.changePlan(
      projectId,
      author,
      reason.trim() || original,
      undo,
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
    const after = applyOps(graph, ops).graph
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
      ...this.stopsForRemoved(projectId, graph, after, reason, author),
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
    this.projectRow(projectId)
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
    this.projectRow(projectId)
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
      /** The user's edit: the commented text as it now reads, by comment thread. */
      quotes?: Record<string, string>
      /**
       * Saved from the shared editing state, where people and agents edit together: the
       * user's part reaches the agent as feedback, not as a note on this version.
       */
      shared?: boolean
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
    // Only quotes that moved, of threads this artifact has.
    const threads = new Map(this.listArtifactComments(artifactId).map((t) => [t.id, t]))
    const quotes: Record<string, string> = {}
    for (const [threadId, quote] of Object.entries(input.quotes ?? {})) {
      const text = quote.slice(0, MAX_COMMENT_QUOTE)
      const thread = threads.get(threadId)
      if (thread && text && text !== thread.quote) quotes[threadId] = text
    }
    const moved = Object.keys(quotes).length > 0
    // A quote moves only with the text it is in.
    if (title === current.title && content === current.content) return current
    const byUser = input.author === 'user' && !input.shared
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
          ...(byUser
            ? {
                notify: this.artifactListener(artifactId),
                diff: current.kind === 'document' ? lineDiff(current.content, content) : null,
              }
            : {}),
          ...(moved ? { quotes } : {}),
        },
      },
    ])
    return this.getArtifact(artifactId)
  }

  /**
   * Who hears about the user's edits and comments on an artifact: the agent that wrote it
   * last, unless that is a thread the user or the coordinator stopped (or there is none);
   * then the coordinator.
   */
  private artifactListener(artifactId: string): string | null {
    const row = this.db
      .query<{ project_id: string; last_agent: string | null }, [string]>(
        'SELECT project_id, last_agent FROM artifacts WHERE id = ?',
      )
      .get(artifactId)
    if (!row) return null
    const coordinator = this.projectRow(row.project_id).coordinator_id
    if (!row.last_agent || row.last_agent === coordinator) return coordinator
    const thread = this.db
      .query<{ stopped_at: number | null }, [string]>('SELECT stopped_at FROM threads WHERE id = ?')
      .get(row.last_agent)
    return thread && thread.stopped_at === null ? row.last_agent : coordinator
  }

  listArtifactComments(artifactId: string): ArtifactCommentThreadView[] {
    return this.db
      .query<CommentThreadRow, [string]>(
        'SELECT * FROM artifact_comments WHERE artifact_id = ? ORDER BY created_at, rowid',
      )
      .all(artifactId)
      .map(toCommentThread)
  }

  /**
   * Change an artifact's comments. Anyone can start a thread, reply, resolve or reopen
   * one; only a comment's author edits or deletes it, and only the user removes a whole
   * thread. The first comment goes only with its thread. The user's new comments and
   * replies wake the agent that hears about the artifact, to answer them.
   */
  commentOnArtifact(
    artifactId: string,
    by: string,
    action: ArtifactCommentAction,
  ): ArtifactCommentThreadView[] {
    const artifact = this.getArtifact(artifactId)
    if (artifact.kind === 'dashboard')
      throw new DomainError('error.invalidBody', { reason: 'kind' })
    const thread = this.listArtifactComments(artifactId).find((t) => t.id === action.threadId)
    let checked: ArtifactCommentAction
    if (action.type === 'create') {
      // Ids are the page's; one taken anywhere is refused, not overwritten.
      const taken = this.db
        .query<{ id: string }, [string]>('SELECT id FROM artifact_comments WHERE id = ?')
        .get(action.threadId)
      if (taken) throw invalid('threadId')
      if (artifact.kind === 'whiteboard') {
        // A whiteboard's thread is about an item or a connector, named by its id.
        const anchor = action.anchor
        const id = anchor && ('node' in anchor ? anchor.node : anchor.edge)
        if (!anchor || typeof id !== 'string' || !id || id.length > 200) throw invalid('anchor')
        checked = {
          type: 'create',
          threadId: action.threadId,
          commentId: action.commentId,
          quote: '',
          body: commentText(action.body),
          anchor: 'node' in anchor ? { node: id } : { edge: id },
        }
      } else {
        const quote = action.quote.trim()
        if (!quote || quote.length > MAX_COMMENT_QUOTE) throw invalid('quote')
        const { anchor: _, ...rest } = action
        checked = { ...rest, quote, body: commentText(action.body) }
      }
    } else {
      if (!thread) throw new DomainError('error.notFound', { what: action.threadId })
      const comment =
        'commentId' in action ? thread.comments.find((c) => c.id === action.commentId) : null
      switch (action.type) {
        case 'reply':
          if (thread.comments.some((c) => c.id === action.commentId)) throw invalid('commentId')
          checked = { ...action, body: commentText(action.body) }
          break
        case 'edit':
          if (!comment) throw new DomainError('error.notFound', { what: action.commentId })
          if (comment.author !== by) throw invalid('author')
          checked = { ...action, body: commentText(action.body) }
          break
        case 'delete':
          if (!comment) throw new DomainError('error.notFound', { what: action.commentId })
          if (comment.author !== by || thread.comments[0]?.id === comment.id) {
            throw invalid('author')
          }
          checked = action
          break
        case 'remove':
          if (by !== 'user') throw invalid('author')
          checked = action
          break
        default:
          if ((action.type === 'resolve') === !!thread.resolved)
            return this.listArtifactComments(artifactId)
          checked = action
      }
    }
    // The user's comments reach the agent with the next round of feedback, not one by one.
    this.store.append([
      {
        type: 'artifact.commented',
        projectId: artifact.projectId,
        data: { artifactId, by, action: checked, notify: null },
      },
    ])
    return this.listArtifactComments(artifactId)
  }

  // ---- editing together ----

  /** The shared editing state of a document or whiteboard; `null` before anyone opened it. */
  readSharedState(artifactId: string): SharedState | null {
    return readSharedState(this.db, artifactId)
  }

  writeSharedState(artifactId: string, shared: SharedState): void {
    const artifact = this.getArtifact(artifactId)
    writeSharedState(this.db, artifact.projectId, artifactId, shared, this.now())
  }

  /**
   * Send what the user did in an artifact since the last round to the agent that hears
   * about it, which it wakes. Nothing is sent without items.
   */
  sendArtifactFeedback(
    artifactId: string,
    input: { items: ArtifactFeedbackItem[]; images?: string[] },
  ): ArtifactFeedbackView | null {
    const artifact = this.getArtifact(artifactId)
    if (!input.items.length) return null
    const notify = this.artifactListener(artifactId)
    if (!notify) return null
    const images: Attachment[] = []
    for (const [i, fileId] of (input.images ?? []).entries()) {
      const file = fileInfo(this.db, artifact.projectId, fileId)
      if (!file || !isImageType(file.mime)) throw invalid('images')
      images.push({ fileId, name: `${artifact.title} ${i + 1}.png`, ...file })
    }
    const round = (this.listArtifactFeedback(artifactId)[0]?.round ?? 0) + 1
    this.store.append([
      {
        type: 'artifact.feedback',
        projectId: artifact.projectId,
        data: {
          artifactId,
          round,
          version: artifact.version,
          notify,
          items: input.items,
          images: images.slice(0, MAX_FEEDBACK_IMAGES),
        },
      },
    ])
    return this.listArtifactFeedback(artifactId)[0] ?? null
  }

  /** The agent says it finished a round of feedback, and what it did about it. */
  finishArtifactFeedback(
    artifactId: string,
    round: number,
    by: string,
    summary: string,
  ): ArtifactFeedbackView {
    const artifact = this.getArtifact(artifactId)
    const found = this.listArtifactFeedback(artifactId).find((f) => f.round === round)
    if (!found) throw new DomainError('error.notFound', { what: `${artifactId} round ${round}` })
    if (found.doneAt === null) {
      this.store.append([
        {
          type: 'artifact.feedback_done',
          projectId: artifact.projectId,
          data: { artifactId, round, by, summary: summary.trim().slice(0, MAX_COMMENT_BODY) },
        },
      ])
    }
    return this.listArtifactFeedback(artifactId).find((f) => f.round === round)!
  }

  /** The rounds of feedback sent on an artifact, newest first. */
  listArtifactFeedback(artifactId: string): ArtifactFeedbackView[] {
    return this.db
      .query<FeedbackRow, [string]>(
        'SELECT * FROM artifact_feedback WHERE artifact_id = ? ORDER BY round DESC',
      )
      .all(artifactId)
      .map(toFeedbackView)
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
      input: { system: string; tools: string; call: string; images: boolean }
      timing: CallTiming
      /** The output hit the length limit; `toolCalls` are only those that came whole. */
      cut?: boolean
    },
  ): void {
    const projectId = this.runningTurn(conversationId, turnId)
    const { pieces, keep } = this.promptKeeper(projectId)
    const input: StepInput = {
      system: keep(step.input.system),
      tools: keep(step.input.tools),
      call: keep(step.input.call),
      images: step.input.images,
    }
    const { text, reasoning, toolCalls, sawSeq, usage, timing } = step
    const finish = step.cut
      ? ('length' as const)
      : toolCalls.length
        ? ('tool-calls' as const)
        : ('stop' as const)
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

  /**
   * One step of the research a `research` call runs: the response, and the results of the
   * calls it made. Its tokens count toward the conversation's.
   */
  recordResearchStep(
    conversationId: string,
    turnId: string,
    step: Omit<EventMap['research.step'], 'conversationId' | 'turnId' | 'input'> & {
      input: { system: string; tools: string; call: string }
    },
  ): void {
    const projectId = this.runningTurn(conversationId, turnId)
    const { pieces, keep } = this.promptKeeper(projectId)
    const input = {
      system: keep(step.input.system),
      tools: keep(step.input.tools),
      call: keep(step.input.call),
    }
    this.store.append([
      ...pieces,
      { type: 'research.step', projectId, data: { ...step, conversationId, turnId, input } },
    ])
  }

  /** The tokens of a web search a model's own service ran for a call of this turn. */
  recordSearch(
    conversationId: string,
    turnId: string,
    search: { model: string; usage: TokenUsage | null },
  ): void {
    const projectId = this.runningTurn(conversationId, turnId)
    this.store.append([
      { type: 'search.answered', projectId, data: { ...search, conversationId, turnId } },
    ])
  }

  /** The steps of one research of a conversation, up to `upTo`, oldest first. */
  researchSteps(conversationId: string, toolCallId: string, upTo: number): AnyStoredEvent[] {
    const conversation = this.getConversation(conversationId)
    return this.db
      .query<EventRow, [string, string, string, number]>(
        `SELECT * FROM events WHERE project_id = ? AND type = 'research.step'
           AND json_extract(data, '$.conversationId') = ?
           AND json_extract(data, '$.toolCallId') = ? AND seq <= ?
         ORDER BY seq`,
      )
      .all(conversation.projectId, conversationId, toolCallId, upTo)
      .map(storedEvent)
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
      timing?: { startedAt: number; finishedAt: number }
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
    // One transaction: a crash halfway must not leave a finished turn whose review round
    // never hears that its reviewer left.
    this.store.transaction(() => this.finishTurnNow(conversationId, turnId, outcome, error))
  }

  private finishTurnNow(
    conversationId: string,
    turnId: string,
    outcome: TurnOutcome,
    error: LocalizedMessage | null,
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
    // A reviewer that ends its turn without a verdict, with nothing more to read, has left
    // the round; an interrupted turn resumes on the next start.
    if (outcome === 'interrupted') return
    if (outcome === 'completed' && this.getConversation(conversationId).pendingSeq > 0) return
    const abandoned = this.abandonedReview(conversationId)
    if (abandoned.length) this.store.append(abandoned)
    if (outcome === 'completed') this.nudgeUnreported(conversationId, projectId)
  }

  /**
   * A thread whose turn ended without a report is told once to report; when that turn is
   * silent too, it stays `silent` for the user. A question it asked speaks for itself.
   */
  private nudgeUnreported(threadId: string, projectId: string): void {
    const row = this.db.query<ThreadRow, [string]>(`${THREAD_SELECT} WHERE t.id = ?`).get(threadId)
    if (!row || toThread(row, this.now()).stage !== 'silent') return
    if (row.nudged_at !== null && row.nudged_at >= (row.reported_at ?? 0)) return
    this.store.append([{ type: 'thread.nudged', projectId, data: { threadId } }])
  }

  /**
   * Whether all the coordinator has waiting is what the user wrote inside threads (and
   * progress reports, which need nothing): the turn only decides where those belong.
   */
  pendingIsTriage(conversationId: string): boolean {
    const rows = this.db
      .query<{ kind: string; payload: string | null }, [string]>(
        `SELECT t.kind, t.payload FROM conversations c
           JOIN transcript t ON t.conversation_id = c.id AND t.seq >= c.pending_since
         WHERE c.id = ? AND c.pending_seq > 0 AND c.role = 'coordinator'`,
      )
      .all(conversationId)
    let triage = false
    for (const row of rows) {
      const note = row.kind === 'note' && row.payload ? JSON.parse(row.payload) : null
      if (note?.type === 'user-in-thread') triage = true
      else if (!(note?.type === 'report' && note.report === 'progress')) return false
    }
    return triage
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
      ...this.abandonedReview(conversationId),
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
    if (this.projectRow(projectId).coordinator_id === conversationId) return
    this.assertThreadIn(projectId, conversationId)
  }

  private projectUsage(projectId: string): UsageTotals {
    const row = this.db
      .query<UsageColumns, [string]>(
        `SELECT sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens,
           sum(cached_tokens) AS cached_tokens, sum(cache_write_tokens) AS cache_write_tokens,
           sum(reasoning_tokens) AS reasoning_tokens, sum(cost_usd) AS cost_usd,
           sum(cost_cny) AS cost_cny
         FROM conversations WHERE project_id = ?`,
      )
      .get(projectId)
    return usageOf(row ?? {})
  }

  private toProject(row: ProjectRow, needsMe = this.listNeedsMe(row.id).length): ProjectView {
    return {
      id: row.id,
      name: row.name,
      coordinatorId: row.coordinator_id,
      createdAt: row.created_at,
      updatedAt: row.updated_at,
      needsMe,
      source: row.source ? (JSON.parse(row.source) as ProjectSource) : null,
      slots: row.slots,
      mcp: JSON.parse(row.mcp) as string[],
      settings: storedProjectSettings(row.settings),
      detected: row.detected
        ? (JSON.parse(row.detected) as ProjectDetected)
        : { setup: null, check: null },
      autopilotPaused: row.autopilot_paused === 1,
      usage: this.projectUsage(row.id),
    }
  }
}

export type { AnyStoredEvent }
