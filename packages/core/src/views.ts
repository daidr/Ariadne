import type { AttentionOption, Author, MessageKind, ReportKind, TodoItem } from './events'
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
