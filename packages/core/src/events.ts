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

/** Every durable fact Ariadne records. Payloads are plain JSON. */
export interface EventMap {
  'project.created': { name: string; coordinatorId: string }
  'project.renamed': { name: string }
  'thread.created': { threadId: string; title: string; nodeId: string | null }
  'thread.started': { threadId: string }
  'thread.reported': { threadId: string; report: ReportKind; summary: string }
  'attention.opened': {
    attentionId: string
    /** `null` when the coordinator asks. */
    threadId: string | null
    question: string
    options: AttentionOption[]
    recommended: number | null
  }
  'attention.answered': { attentionId: string; choice: number | null; text: string | null }
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
