import type { LocalizedMessage } from '@ariadne/i18n'
import type { AskBlock, BlockAnswer, ShowBlock } from './blocks'
import type { FileType } from './files'

/** Who wrote a message. */
export type Author = 'user' | 'coordinator' | 'thread'

/**
 * `chat` is an ordinary message. `relay` is a message the coordinator sent into a thread:
 * the thread's panel shows it faded and collapsed.
 */
export type MessageKind = 'chat' | 'relay'

/** What a thread reports about its own work. */
export type ReportKind = 'progress' | 'waiting' | 'ready' | 'blocked' | 'failed' | 'done'

export type TodoStatus = 'doing' | 'next' | 'waiting' | 'done'

/** An issue of the project's tracker, as a plan node links it. */
export interface LinkedIssue {
  /** Where the issue lives, e.g. `owner/name`. */
  tracker: string
  number: number
  title: string
  url: string
}

export type IssueWriteAction = 'comment' | 'close' | 'reopen'

/** One change to an issue, made with the user's yes on `attentionId`. */
export interface IssueWrite {
  threadId: string
  attentionId: string
  purpose: string
  tracker: string
  number: number
  action: IssueWriteAction
  /** What is written, exactly as the user agreed to it (the comment, or why it is closed). */
  text: string
}

export interface TodoItem {
  label: string
  status: TodoStatus
  /** For `waiting`: what the item waits on. */
  detail?: string
}

/** A tool call as the model made it; `arguments` is the model's JSON text, unparsed. */
export interface ToolCallRecord {
  id: string
  name: string
  arguments: string
}

/**
 * A file that came with a message or a tool result: an image the model looks at, or text
 * it reads. The bytes stay in the project's files; `name` is what the user or tool called it.
 */
export interface Attachment {
  fileId: string
  name: string
  mime: FileType
  size: number
}

/**
 * A block of a model's reasoning as its provider returned it; `signature` (or the opaque
 * `text` of a `redacted` block) is what the provider checks when it is sent back.
 */
export interface ReasoningRecord {
  text: string
  signature?: string
  redacted?: boolean
}

/**
 * Tokens of one model call. `input` is everything read, `cached` the part of it read from
 * the provider's cache, `reasoning` the part of `output` spent thinking (when known).
 */
export interface TokenUsage {
  input: number
  output: number
  cached?: number
  reasoning?: number
}

/**
 * The parts of a step's model request that the transcript does not hold, by the hash of
 * their `prompt.recorded` text: the system prompt, the tool definitions (JSON), the
 * current state shown beside a summary (`null` without one), and `call`: JSON of the
 * provider kind, model, effort and settings (secrets left out) the request went with.
 */
export interface StepInput {
  system: string
  tools: string
  state: string | null
  call: string
  /** Whether images went to the model as images; without, they were described in words. */
  images: boolean
}

/** When a model call was sent and when its first output came, in ms since the epoch. */
export interface CallTiming {
  startedAt: number
  firstOutputAt: number | null
}

/**
 * How a turn ended. `interrupted` means the process stopped mid-turn; `cancelled` means
 * the user stopped it.
 */
export type TurnOutcome = 'completed' | 'failed' | 'cancelled' | 'interrupted'

export type PlanNodeKind = 'task' | 'milestone' | 'decision'

export interface PlanNodeFields {
  kind: PlanNodeKind
  title: string
  description: string
  /** How the user will judge the node finished. */
  acceptance: string
}

/**
 * One change to the plan graph. Node ids are assigned before the event is written, so
 * replaying a revision always produces the same graph. `link` makes `to` depend on `from`.
 */
export type PlanOp =
  | ({ op: 'add'; id: string } & PlanNodeFields)
  | ({
      op: 'update'
      id: string
      /**
       * Values the fields must still have, so undoing an edit refuses to overwrite a later
       * edit of the same field.
       */
      expect?: Partial<Omit<PlanNodeFields, 'kind'>>
    } & Partial<Omit<PlanNodeFields, 'kind'>>)
  | { op: 'remove'; id: string }
  | { op: 'link'; from: string; to: string }
  | { op: 'unlink'; from: string; to: string }

export type PlanAuthor = 'user' | 'coordinator'

/** Where a project's work happens. Only local directories for now. */
export interface ProjectSource {
  kind: string
  settings: Record<string, unknown>
}

/** Markdown documents, whiteboards and dashboard snapshots; see artifact-content.ts. */
export type ArtifactKind = 'document' | 'whiteboard' | 'dashboard'

/** How the checks of a commit stand, all together. */
export type ChecksState = 'none' | 'pending' | 'success' | 'failure'

/** A thread's pull request on the project's repository. */
export interface ThreadPullRequest {
  number: number
  /** How the host refers to it in text, e.g. `!12`; `#<number>` when left out. */
  reference?: string
  url: string
  title: string
  state: 'open' | 'closed' | 'merged'
  headSha: string
  checks: { state: ChecksState; failed: string[] }
}

export type MergeMethod = 'squash' | 'merge'

/** A thread's branch merged into its base by the user. */
export interface BranchMerge {
  branch: string
  base: string
  method: MergeMethod
  /** The commit the base moved to. */
  sha: string
  /** The branch's newest commit, which the merge took in. */
  head: string
}

/** Every durable fact Ariadne records. Payloads are plain JSON. */
export interface EventMap {
  'project.created': { name: string; coordinatorId: string }
  'project.renamed': { name: string }
  /**
   * The project was deleted with everything it held; this is all the log keeps of it.
   * Nothing else of the project is written after it.
   */
  'project.deleted': Record<string, never>
  /** The project's context source; `null` removes it. */
  'project.source.set': { source: ProjectSource | null }
  /** How many of the shared work slots this project may take at once; `null` is no cap. */
  'project.slots.set': { slots: number | null }
  /** The external tool servers (MCP) this project's conversations may use, by id. */
  'project.mcp.set': { servers: string[] }
  /** `model` is the id of the model setting the coordinator chose for it, if any. */
  'thread.created': { threadId: string; title: string; nodeId: string | null; model: string | null }
  /** The thread's model changed; `null` goes back to the default for threads. */
  'thread.model_set': { threadId: string; model: string | null; by: string }
  /**
   * A thread's workspace was prepared, e.g. a git worktree on its own branch. `base` is the
   * branch that one started from, which its changes are compared and merged against.
   */
  'workspace.opened': {
    threadId: string
    label: string
    branch: string | null
    base: string | null
  }
  /**
   * The thread's worktree was removed; its branch stays, and the next use checks it out
   * again. `merged`: removed on its own once the work was merged (or its pull request
   * closed) and the thread was finished with it; `user`: the user removed it.
   */
  'workspace.removed': { threadId: string; reason: 'merged' | 'user' }
  /** The user merged the thread's branch into its base; the base now ends with `sha`. */
  'branch.merged': { threadId: string } & BranchMerge
  /**
   * The user tried to merge the thread's branch, and these files changed on both sides.
   * Nothing was merged; the thread gets to bring the base in and resolve them.
   */
  'branch.conflicted': { threadId: string; branch: string; base: string; files: string[] }
  'thread.started': { threadId: string }
  'thread.reported': {
    threadId: string
    report: ReportKind
    summary: string
    /** The branch's newest commit when it reported, once the user merged it before. */
    head?: string
  }
  /**
   * The thread is held: its running turn ends and no new one starts until it is resumed.
   * `by` is `user` or the coordinator's id.
   */
  'thread.stopped': { threadId: string; reason: string; by: string }
  /** Work goes on; `reason` is why it had been stopped, for the thread to read. */
  'thread.resumed': { threadId: string; by: string; reason: string }
  /**
   * Something waits on the user: an ask block (choice, confirmation or form). An agent's
   * comes with the message it posted it in (`messageId`); the system's own (approvals,
   * plan proposals) are only listed under "needs me".
   */
  'attention.opened': {
    attentionId: string
    /** `null` when the coordinator asks. */
    threadId: string | null
    block: AskBlock
    messageId: string | null
    /**
     * Set when the system asks for a standing approval (e.g. `push-public`), so a tool can
     * check later whether the user agreed. Its block is a confirmation.
     */
    purpose?: string | null
  }
  'attention.answered': { attentionId: string; answer: BlockAnswer }
  /**
   * The agent that asked takes its question back, e.g. once the user answered it in words
   * or it no longer matters. It leaves "needs me"; the message shows it was withdrawn.
   */
  'attention.withdrawn': { attentionId: string; reason: string }
  /**
   * Another thread waits on the same standing approval: it hears the answer too, so the
   * user answers one card instead of one per thread.
   */
  'attention.joined': { attentionId: string; threadId: string }
  'message.posted': {
    messageId: string
    /** The coordinator id or a thread id. */
    conversationId: string
    author: Author
    kind: MessageKind
    content: string
    /** Tables, charts and steps shown under the text; an ask block is its attention's. */
    blocks?: ShowBlock[]
    /** Images and text files the user sent with it. */
    attachments?: Attachment[]
  }
  'message.edited': { messageId: string; content: string; reason: string }
  'message.read': { conversationId: string; upTo: string }
  'todo.updated': { conversationId: string; items: TodoItem[] }
  /**
   * A standing preference or fact of the project ("write in Chinese", "the API lives in
   * packages/server"), saved or changed. Every conversation of the project sees them.
   */
  'memory.saved': {
    memoryId: string
    text: string
    author: string
    /** The notice of an agent's change this one takes back, when the user undid it. */
    undoes?: string
  }
  'memory.removed': { memoryId: string; author: string; undoes?: string }
  /**
   * A conversation's older history was summarised: the model sees `summary` in place of
   * every transcript entry it had read up to `upTo` (a position in the model's order).
   * The summary was asked for from the transcript as of `sawSeq`.
   */
  'conversation.compacted': {
    conversationId: string
    summary: string
    upTo: number
    sawSeq: number
    usage: TokenUsage | null
    /** Hash of the call's `prompt.recorded` JSON, as in `StepInput.call`. */
    call: string
    timing: CallTiming
    /**
     * What the model thought while summarising, unsigned. It goes back with the notes:
     * services that think by default (DeepSeek) refuse an assistant message without it.
     */
    reasoning: ReasoningRecord[]
  }
  /** The user settled a notice without anything else to record, e.g. undid a stale one. */
  'notice.dismissed': { noticeId: string }
  /** Summarising a conversation's history failed; the turn went on with all of it. */
  'conversation.compaction_failed': { conversationId: string; detail: string }
  /**
   * The repository's instruction file (AGENTS.md) as the thread's workspace holds it now:
   * recorded when it first appears and whenever it changes, `content: null` once removed.
   * The model reads the newest recorded version, so every request can be rebuilt.
   */
  'conversation.instructions': {
    conversationId: string
    path: string
    hash: string
    content: string | null
    /** Whether an earlier version had been recorded, so the model is told it moved. */
    changed: boolean
    /** The version differs from the checkout's commit: someone edited it in the workspace. */
    uncommitted?: boolean
  }
  /** A model request failed in a way worth trying again; `attempt` counts the retries. */
  'conversation.model_retry': {
    conversationId: string
    attempt: number
    delayMs: number
    detail: string
  }
  /** The agent of a conversation began working. */
  'turn.started': { conversationId: string; turnId: string; model: string }
  /**
   * One model response inside a turn. `sawSeq` is the newest event the model's input
   * included, so inputs up to it count as handled.
   */
  'turn.step': {
    conversationId: string
    turnId: string
    text: string
    reasoning: ReasoningRecord[]
    toolCalls: ToolCallRecord[]
    /** Why the model stopped: it answered, or it called tools. */
    finish: 'stop' | 'tool-calls'
    sawSeq: number
    usage: TokenUsage | null
    input: StepInput
    timing: CallTiming
  }
  /**
   * A piece of model input seen for the first time in the project, stored once by its
   * hash; steps name the pieces they were sent, so the trace can show every request whole.
   */
  'prompt.recorded': { hash: string; content: string }
  'tool.finished': {
    conversationId: string
    turnId: string
    toolCallId: string
    name: string
    content: string
    isError: boolean
    /** Images the tool handed the model, e.g. a picture read from the workspace. */
    files?: Attachment[]
  }
  'turn.finished': {
    conversationId: string
    turnId: string
    outcome: TurnOutcome
    /** Why it failed, for the user. */
    error: LocalizedMessage | null
  }
  /** The user asked a conversation to try again after a failed turn. */
  'turn.requested': { conversationId: string }
  /**
   * A change to the plan graph took effect. `revertOf` names the revision it undoes.
   * `proposalId` names the proposal it carries out, after the user agreed.
   */
  'plan.revised': {
    revisionId: string
    author: PlanAuthor
    reason: string
    ops: PlanOp[]
    revertOf: string | null
    proposalId: string | null
  }
  /**
   * A change big enough to need the user's yes (removing nodes, changing acceptance or
   * milestones). It waits on an attention whose first option applies it.
   */
  'plan.proposed': {
    proposalId: string
    attentionId: string
    reason: string
    ops: PlanOp[]
    /** Set when the proposal undoes an earlier revision. */
    revertOf: string | null
  }
  /** The user agreed, but the plan had moved on and the proposal no longer applied. */
  'plan.proposal.stale': { proposalId: string; reason: string }
  /** The user has seen that an agreed proposal did not take effect. */
  'plan.proposal.dismissed': { proposalId: string }
  /**
   * An issue of the project's tracker belongs to a plan node: the node's threads read it as
   * part of their work. The title is as it was when linked, for showing without a fetch.
   */
  'plan.issue_linked': { nodeId: string; issue: LinkedIssue; by: string }
  'plan.issue_unlinked': { nodeId: string; tracker: string; number: number; by: string }
  /**
   * A thread is about to change an issue (comment, close, reopen) with the user's yes on
   * `attentionId`. Recorded first, so a change cut off by a crash is not made blindly again.
   * Each yes is used for one change only.
   */
  'issue.writing': IssueWrite
  /** The change went through; `url` shows it on the tracker. */
  'issue.written': IssueWrite & { url: string | null }
  /**
   * A change was cut off or failed before it was known to be made; `threadId` was told to
   * check (absent in logs written before it was recorded).
   */
  'issue.write_unsure': { attentionId: string; threadId?: string }
  /**
   * A thread calls an external tool with the user's yes on `attentionId`; the yes is used
   * now, so the same call again asks again.
   */
  'mcp.called': { threadId: string; attentionId: string; server: string; tool: string }
  /** The thread's pull request as last seen on the repository; written only on a change. */
  'pull_request.synced': { threadId: string } & ThreadPullRequest
  /** Checks of the pull request's newest commit failed; the thread gets to fix them. */
  'checks.failed': {
    threadId: string
    number: number
    headSha: string
    failed: string[]
    /** How many failures in a row this is; missing in older logs, where it counts as 1. */
    attempt?: number
  }
  /** The pull request was merged or closed. */
  'pull_request.closed': { threadId: string; number: number; merged: boolean }
  'artifact.created': {
    artifactId: string
    kind: ArtifactKind
    title: string
    nodeId: string | null
    /** The coordinator, a thread id, or `user`. */
    author: string
    content: string
  }
  'artifact.revised': {
    artifactId: string
    version: number
    title: string
    content: string
    author: string
    reason: string
  }
}

export type EventType = keyof EventMap

/** An event as stored: every event belongs to one project. */
export interface StoredEvent<T extends EventType = EventType> {
  seq: number
  id: string
  type: T
  projectId: string
  data: EventMap[T]
  at: number
}

export type AnyStoredEvent = { [T in EventType]: StoredEvent<T> }[EventType]

export interface NewEvent<T extends EventType = EventType> {
  type: T
  projectId: string
  data: EventMap[T]
}

export type AnyNewEvent = { [T in EventType]: NewEvent<T> }[EventType]
