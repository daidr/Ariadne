import type { LocalizedMessage } from '@ariadne/i18n'
import type {
  ArtifactKind,
  AttentionOption,
  PlanAuthor,
  PlanNodeKind,
  PlanOp,
  ProjectSource,
  Author,
  MessageKind,
  ReportKind,
  ThreadPullRequest,
  TodoItem,
  ToolCallRecord,
  TurnOutcome,
} from './events'
import type { ThreadStage } from './stage'

export interface ProjectView {
  id: string
  name: string
  coordinatorId: string
  createdAt: number
  updatedAt: number
  /** Length of the project's "needs me" list, see `NeedsMeItem`. */
  needsMe: number
  /** Where threads work; `null` until the user picks a directory. */
  source: ProjectSource | null
}

export interface ThreadView {
  id: string
  projectId: string
  title: string
  nodeId: string | null
  stage: ThreadStage
  lastReport: ReportKind | null
  lastSummary: string | null
  /** The thread's prepared workspace, once it has used one. */
  workspace: { label: string; branch: string | null } | null
  /** The pull request of the thread's branch, once it opened one. */
  pullRequest: ThreadPullRequest | null
  createdAt: number
  updatedAt: number
}

export interface AttentionView {
  id: string
  projectId: string
  threadId: string | null
  question: string
  options: AttentionOption[]
  recommended: number | null
  createdAt: number
}

export interface MessageView {
  id: string
  conversationId: string
  author: Author
  kind: MessageKind
  content: string
  createdAt: number
  editedAt: number | null
  editReason: string | null
  /** Edited after the user read it: highlight once, until the next read. */
  editedSinceRead: boolean
}

export interface TodoView {
  conversationId: string
  items: TodoItem[]
  updatedAt: number
}

/**
 * One entry of the "needs me" list. Every open attention is an entry; a thread that waits
 * on the user without an open attention (review, blocked) is an entry of its own. Counts
 * shown anywhere are the length of this list.
 */
export type NeedsMeItem =
  | { kind: 'attention'; attention: AttentionView; thread: ThreadView | null }
  | { kind: 'thread'; thread: ThreadView }

export type ConversationRole = 'coordinator' | 'thread'

/** The agent side of a conversation: is it working, and how did its last turn end. */
export interface ConversationView {
  id: string
  projectId: string
  role: ConversationRole
  /** Newest input that no model response has seen yet; 0 when none. */
  pendingSeq: number
  turn: { id: string; startedAt: number; model: string | null } | null
  lastOutcome: TurnOutcome | null
  lastError: LocalizedMessage | null
  usage: { input: number; output: number }
}

/** One entry of what a conversation's agent has seen, oldest first. */
export type TranscriptEntry =
  | { seq: number; kind: 'message'; message: MessageView }
  | { seq: number; kind: 'note'; note: TranscriptNote }
  | {
      seq: number
      kind: 'step'
      text: string
      toolCalls: ToolCallRecord[]
      /** The newest event the model had seen when it wrote this step. */
      sawSeq: number
    }
  | {
      seq: number
      kind: 'tool'
      toolCallId: string
      name: string
      content: string
      isError: boolean
    }

/** Facts written into a transcript by the system rather than by a person. */
export type TranscriptNote =
  | { type: 'report'; threadId: string; report: ReportKind; summary: string }
  | {
      type: 'answer'
      attentionId: string
      question: string
      choice: string | null
      text: string | null
    }
  | { type: 'user-in-thread'; threadId: string; content: string }
  | { type: 'upstream-done'; threadId: string; nodeTitle: string; summary: string }
  | { type: 'artifact-edited'; artifactId: string; title: string; version: number }
  | { type: 'plan-changed-by-user'; revisionId: string; revertOf: string | null; reason: string }
  | { type: 'proposal-stale'; proposalId: string; reason: string; staleBecause: string }
  | { type: 'checks-failed'; threadId: string; number: number; headSha: string; failed: string[] }
  | { type: 'pull-request-closed'; threadId: string; number: number; merged: boolean }

/**
 * How far a plan node is, from the threads working on it: `none` without threads,
 * `wait` when one needs the user, `error` when one is blocked, `finish` when all are done.
 */
export type PlanNodeStatus = 'none' | 'process' | 'wait' | 'error' | 'finish'

export interface PlanNodeView {
  id: string
  kind: PlanNodeKind
  title: string
  description: string
  acceptance: string
  /** Nodes this one depends on. */
  dependsOn: string[]
  status: PlanNodeStatus
  threads: { id: string; title: string; stage: ThreadStage }[]
  /** The revision that last changed the node, to highlight what the coordinator changed. */
  changedIn: string | null
}

export interface PlanRevisionView {
  id: string
  author: PlanAuthor
  reason: string
  ops: PlanOp[]
  revertOf: string | null
  revertedBy: string | null
  createdAt: number
}

export interface PlanProposalView {
  id: string
  attentionId: string
  reason: string
  ops: PlanOp[]
  /** What the change does, one line per op, against the plan as it is now. */
  changes: LocalizedMessage[]
  /**
   * `open` waits on the user. `stale` means the user agreed but the plan had moved on, so
   * it did not take effect; it stays visible until the plan changes again.
   */
  status: 'open' | 'stale'
  staleReason: string | null
  createdAt: number
}

export interface PlanView {
  projectId: string
  nodes: PlanNodeView[]
  /** Newest first. */
  revisions: PlanRevisionView[]
  /** Changes waiting on the user, and agreed ones that no longer applied. */
  proposals: PlanProposalView[]
}

export interface ArtifactSummary {
  id: string
  projectId: string
  kind: ArtifactKind
  title: string
  nodeId: string | null
  version: number
  createdBy: string
  updatedBy: string
  createdAt: number
  updatedAt: number
  /** What the overview shows of it without loading the whole content. */
  preview: ArtifactPreview
}

export type ArtifactPreview =
  | { kind: 'document'; excerpt: string }
  | { kind: 'whiteboard'; items: number; connectors: number; texts: string[] }
  | { kind: 'dashboard'; widgets: number; takenAt: number | null; titles: string[] }

export interface ArtifactView extends ArtifactSummary {
  content: string
}

export interface ArtifactVersionView {
  version: number
  title: string
  author: string
  reason: string
  createdAt: number
}
