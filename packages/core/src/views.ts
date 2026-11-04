import type { LocalizedMessage } from '@ariadne/i18n'
import type { PlanPreview } from './plan'
import type { AskBlock, BlockAnswer, ShowBlock } from './blocks'
import type {
  Attachment,
  ArtifactKind,
  BranchMerge,
  IssueWriteAction,
  LinkedIssue,
  MergeMethod,
  PlanAuthor,
  PlanNodeKind,
  PlanOp,
  ProjectSource,
  Author,
  MessageKind,
  ReasoningRecord,
  ReportKind,
  ThreadPullRequest,
  TodoItem,
  TokenUsage,
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
  /**
   * How many turns of this project may run at once, within the shared total; `null`
   * leaves it to the total. A turn answering the user may still take a reserved slot.
   */
  slots: number | null
  /** The external tool servers (MCP) its conversations may use, by the ids in the settings. */
  mcp: string[]
  /** Tokens every conversation of the project has used, summaries included. */
  usage: { input: number; output: number }
}

export interface ThreadView {
  id: string
  projectId: string
  title: string
  nodeId: string | null
  stage: ThreadStage
  lastReport: ReportKind | null
  lastSummary: string | null
  /**
   * The thread's prepared workspace, once it has used one. `removed`: its worktree was
   * removed since, and the branch is all that is left until it is used again.
   */
  workspace: {
    label: string
    branch: string | null
    base: string | null
    removed: boolean
  } | null
  /** The user's last merge of its branch, with the time it was made. */
  merged: (BranchMerge & { at: number }) | null
  /** The pull request of the thread's branch, once it opened one. */
  pullRequest: ThreadPullRequest | null
  /** Why and by whom it is held, while it is; see `thread.stopped`. */
  stopped: { reason: string; by: string; at: number } | null
  /** The model setting it was given; `null` uses the default for threads. */
  model: string | null
  /** Tokens its turns have used. */
  usage: { input: number; output: number }
  createdAt: number
  updatedAt: number
}

export interface AttentionView {
  id: string
  projectId: string
  threadId: string | null
  /** Threads that asked the same since and wait on this card too, in the order they joined. */
  joinedBy: string[]
  block: AskBlock
  /** The message it was asked in; `null` for the system's own approvals and proposals. */
  messageId: string | null
  /** The conversation it was asked in: the thread's, or the coordinator's. */
  conversationId: string | null
  createdAt: number
}

/** An ask block in a message, with the user's answer once given. */
export interface MessageAsk {
  attentionId: string
  block: AskBlock
  answer: BlockAnswer | null
  /** Why the agent took the question back; `null` while it stands or once answered. */
  withdrawn: string | null
}

export interface MessageView {
  id: string
  conversationId: string
  author: Author
  kind: MessageKind
  content: string
  /** Tables, charts and steps, shown under the text. */
  blocks: ShowBlock[]
  /** A choice, confirmation or form, shown last. */
  ask: MessageAsk | null
  /** Images and text files the user sent with it. */
  attachments: Attachment[]
  createdAt: number
  editedAt: number | null
  editReason: string | null
  /** What the message said when it was posted, once it has been edited. */
  originalContent: string | null
  /** Edited after the user read it: highlight once, until the next read. */
  editedSinceRead: boolean
}

/**
 * Something that happened in a conversation without being a message, shown in line with
 * them: an agent changed the project memory (the user can undo it), or older history was
 * summarised for the model.
 */
export type NoticeView = {
  id: string
  conversationId: string
  createdAt: number
} & (
  | {
      kind: 'memory'
      action: 'saved' | 'removed'
      memoryId: string
      /** The entry after the change; for a removal, what was removed. */
      text: string
      /** What the entry said before a change; `null` when it was added. */
      previous: string | null
      undone: boolean
    }
  | { kind: 'compacted'; summary: string }
  | { kind: 'compaction-failed'; detail: string }
  | { kind: 'model-retry'; attempt: number; delayMs: number; detail: string }
  | {
      kind: 'issue-linked'
      nodeId: string
      nodeTitle: string
      tracker: string
      number: number
      linked: boolean
    }
  | {
      kind: 'issue-written'
      tracker: string
      number: number
      action: IssueWriteAction
      text: string
      url: string | null
    }
  | { kind: 'merged'; base: string; method: MergeMethod; sha: string }
  | { kind: 'merge-conflict'; base: string; files: string[] }
  | { kind: 'worktree-removed'; reason: 'merged' | 'user' }
  | {
      kind: 'instructions'
      path: string
      /** `null` once the file was removed. */
      content: string | null
      changed: boolean
      uncommitted: boolean
    }
)

/** One entry of the project's memory. `author` is `user`, or the coordinator's id. */
export interface MemoryView {
  id: string
  projectId: string
  text: string
  author: string
  createdAt: number
  updatedAt: number
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
  | {
      kind: 'attention'
      attention: AttentionView
      thread: ThreadView | null
      /** Other threads waiting on the same card, which hear the same answer. */
      joined: ThreadView[]
    }
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
  /** How many tokens the model read in its latest step: how full its context is. */
  contextTokens: number
}

/** One entry of what a conversation's agent has seen, oldest first. */
export type TranscriptEntry =
  | {
      seq: number
      kind: 'message'
      message: MessageView
      /**
       * The conversation's own questions still open when the user wrote this: the user may
       * have answered one in words instead.
       */
      openAsks?: { attentionId: string; title: string }[]
    }
  | { seq: number; kind: 'note'; note: TranscriptNote }
  | {
      seq: number
      kind: 'step'
      text: string
      /** What the model thought first; providers that want it back get it in later steps. */
      reasoning: ReasoningRecord[]
      /** Hash of the call that wrote it (`StepInput.call`): whose the reasoning is. */
      call: string
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
      files?: Attachment[]
    }
  | {
      seq: number
      kind: 'summary'
      /** What the history up to `upTo` said, in short. */
      summary: string
      upTo: number
      /** What the model thought while summarising; goes back with the notes. */
      reasoning: ReasoningRecord[]
      /** Hash of the summarising call, as for a step. */
      call: string
    }

/** Facts written into a transcript by the system rather than by a person. */
export type TranscriptNote =
  | { type: 'report'; threadId: string; report: ReportKind; summary: string }
  | {
      type: 'answer'
      attentionId: string
      question: string
      /** The answer as the model reads it, e.g. `choice: …` or `approved`. */
      answer: string
    }
  | { type: 'user-in-thread'; threadId: string; content: string }
  | { type: 'upstream-done'; threadId: string; nodeTitle: string; summary: string }
  | { type: 'artifact-edited'; artifactId: string; title: string; version: number }
  | { type: 'plan-changed-by-user'; revisionId: string; revertOf: string | null; reason: string }
  | { type: 'proposal-stale'; proposalId: string; reason: string; staleBecause: string }
  | {
      type: 'checks-failed'
      threadId: string
      number: number
      headSha: string
      failed: string[]
      attempt?: number
    }
  | { type: 'pull-request-closed'; threadId: string; number: number; merged: boolean }
  | ({ type: 'branch-merged'; threadId: string } & BranchMerge)
  | { type: 'merge-conflict'; threadId: string; branch: string; base: string; files: string[] }
  | { type: 'thread-resumed'; threadId: string; by: string; reason: string }
  | { type: 'thread-stopped-by-user'; threadId: string; reason: string }
  | { type: 'instructions-changed'; path: string; removed: boolean }
  | {
      /**
       * A change to an issue that this thread waited on with others was made by
       * `threadId`, or that attempt may not have gone through.
       */
      type: 'issue-write-shared'
      threadId: string
      tracker: string
      number: number
      action: IssueWriteAction
      outcome: 'made' | 'unsure'
      url: string | null
    }

/**
 * How far a plan node is, from the threads working on it: `none` without threads,
 * `wait` when one needs the user, `error` when one is blocked, `finish` when all are done.
 */
export type PlanNodeStatus = 'none' | 'process' | 'wait' | 'error' | 'paused' | 'finish'

export interface PlanNodeView {
  id: string
  kind: PlanNodeKind
  title: string
  description: string
  acceptance: string
  /** Nodes this one depends on. */
  dependsOn: string[]
  status: PlanNodeStatus
  threads: {
    id: string
    title: string
    stage: ThreadStage
    pullRequest: Pick<ThreadPullRequest, 'number' | 'reference' | 'url' | 'state' | 'checks'> | null
  }[]
  /** The revision that last changed the node, to highlight what the coordinator changed. */
  changedIn: string | null
  /** Issues of the project's tracker that this node's work is about, and who linked each. */
  issues: (LinkedIssue & { linkedBy: string })[]
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
  /** The plan with the change applied, to draw on the graph; `null` when it no longer applies. */
  preview: PlanPreview | null
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

/**
 * One turn of a conversation as the trace shows it, rebuilt from the event log: every
 * model request with its output, the tool calls and their results, retries and timing.
 */
export interface TraceTurn {
  turnId: string
  /** The `turn.started` event; earlier turns are paged by it. */
  seq: number
  /** The model the turn started with, as the user named it. */
  model: string
  startedAt: number
  /** `null` while it runs. */
  finishedAt: number | null
  outcome: TurnOutcome | null
  error: LocalizedMessage | null
  usage: TokenUsage
  items: TraceItem[]
}

export interface TraceToolCall extends ToolCallRecord {
  startedAt: number
  /** `null` while it runs, or when the turn ended before it did. */
  result: { content: string; isError: boolean; finishedAt: number; files: Attachment[] } | null
}

/** One request to a model: a step of the turn, or the summary of older history. */
export interface TraceRequest {
  kind: 'request'
  /** Its `turn.step` or `conversation.compacted` event, which its details are fetched by. */
  seq: number
  /** Counted over the whole conversation, from 1. */
  number: number
  purpose: 'step' | 'summary'
  provider: string | null
  model: string | null
  effort: 'low' | 'medium' | 'high' | null
  startedAt: number
  firstOutputAt: number | null
  finishedAt: number
  usage: TokenUsage | null
  finish: 'stop' | 'tool-calls'
  /** The answer, or the summary. */
  text: string
  reasoning: { text: string; redacted: boolean }[]
  toolCalls: TraceToolCall[]
}

export type TraceItem =
  | TraceRequest
  | { kind: 'retry'; at: number; attempt: number; delayMs: number; detail: string }
  | { kind: 'compaction-failed'; at: number; detail: string }
  | { kind: 'instructions'; at: number; path: string; changed: boolean; removed: boolean }

/** A message of a model request, as the provider was handed it. */
export type TraceMessage =
  | { role: 'system'; content: string }
  | { role: 'user'; content: string; images?: Attachment[] }
  | {
      role: 'assistant'
      content: string
      toolCalls?: ToolCallRecord[]
      reasoning?: ReasoningRecord[]
    }
  | {
      role: 'tool'
      toolCallId: string
      content: string
      isError?: boolean
      images?: Attachment[]
    }

/** The whole of one model request, rebuilt from the event log. */
export interface TraceRequestDetail {
  messages: TraceMessage[]
  /** The tool definitions it was offered. */
  tools: { name: string; description: string; parameters: Record<string, unknown> }[]
  effort: 'low' | 'medium' | 'high' | null
  /** What it went with besides the messages: provider kind, model and settings without secrets. */
  call: { provider: string; model: string; settings: Record<string, unknown> } | null
  /** The body the provider sent, when its plugin can say; credentials are not in it. */
  body: unknown
  /**
   * Messages from here on are new since the conversation's previous step; the system
   * prompt (the first message) is compared apart.
   */
  newFrom: number
  /** The system prompt differs from the previous step's. */
  systemChanged: boolean
}
