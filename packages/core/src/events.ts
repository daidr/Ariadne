import type { LocalizedMessage } from '@ariadne/i18n'

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

export interface TodoItem {
  label: string
  status: TodoStatus
  /** For `waiting`: what the item waits on. */
  detail?: string
}

export interface AttentionOption {
  label: string
  consequence: string
}

/** A tool call as the model made it; `arguments` is the model's JSON text, unparsed. */
export interface ToolCallRecord {
  id: string
  name: string
  arguments: string
}

export interface TokenUsage {
  input: number
  output: number
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
  url: string
  title: string
  state: 'open' | 'closed' | 'merged'
  headSha: string
  checks: { state: ChecksState; failed: string[] }
}

/** Every durable fact Ariadne records. Payloads are plain JSON. */
export interface EventMap {
  'project.created': { name: string; coordinatorId: string }
  'project.renamed': { name: string }
  /** The project's context source; `null` removes it. */
  'project.source.set': { source: ProjectSource | null }
  'thread.created': { threadId: string; title: string; nodeId: string | null }
  /** A thread's workspace was prepared, e.g. a git worktree on its own branch. */
  'workspace.opened': { threadId: string; label: string; branch: string | null }
  'thread.started': { threadId: string }
  'thread.reported': { threadId: string; report: ReportKind; summary: string }
  'attention.opened': {
    attentionId: string
    /** `null` when the coordinator asks. */
    threadId: string | null
    question: string
    options: AttentionOption[]
    recommended: number | null
    /**
     * Set when the system asks for a standing approval (e.g. `push-public`), so a tool can
     * check later whether the user agreed. The first option means yes.
     */
    purpose?: string | null
  }
  'attention.answered': { attentionId: string; choice: number | null; text: string | null }
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
  }
  'message.edited': { messageId: string; content: string; reason: string }
  'message.read': { conversationId: string; upTo: string }
  'todo.updated': { conversationId: string; items: TodoItem[] }
  /**
   * A standing preference or fact of the project ("write in Chinese", "the API lives in
   * packages/server"), saved or changed. Every conversation of the project sees them.
   */
  'memory.saved': { memoryId: string; text: string; author: string }
  'memory.removed': { memoryId: string; author: string }
  /**
   * A conversation's older history was summarised: the model sees `summary` in place of
   * every transcript entry it had read up to `upTo` (a position in the model's order).
   */
  'conversation.compacted': {
    conversationId: string
    summary: string
    upTo: number
    usage: TokenUsage | null
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
    toolCalls: ToolCallRecord[]
    sawSeq: number
    usage: TokenUsage | null
  }
  'tool.finished': {
    conversationId: string
    turnId: string
    toolCallId: string
    name: string
    content: string
    isError: boolean
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
