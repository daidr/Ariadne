import type { AttentionOption, Author, MessageKind, ReportKind, TodoItem } from './events'
import type { ThreadStage } from './stage'

export interface ProjectView {
  id: string
  name: string
  coordinatorId: string
  createdAt: number
  updatedAt: number
  /** Open attentions plus threads waiting on the user: the "needs me" count. */
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
