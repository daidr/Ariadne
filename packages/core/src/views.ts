import type { LocalizedMessage } from '@ariadne/i18n'
import type {
  AttentionOption,
  Author,
  MessageKind,
  ReportKind,
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
}

export interface ThreadView {
  id: string
  projectId: string
  title: string
  nodeId: string | null
  stage: ThreadStage
  lastReport: ReportKind | null
  lastSummary: string | null
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
