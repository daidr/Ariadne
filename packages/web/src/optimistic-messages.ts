import type { Attachment, FileType, LineComment, MessageView } from '@ariadne/core'
import type { MessageExtras } from './api'

/** A user message shown immediately while `postMessage` is still in flight. */
export interface PendingUserMessage {
  /** Client-only render key, preserved on the confirmed server message to avoid remounting. */
  clientId: string
  projectId: string
  conversationId: string
  /** Server message IDs already loaded when queued, preventing false matches with older messages. */
  baselineIds: readonly string[]
  /** Optimistic message rendered in the timeline until confirmed or rolled back. */
  message: MessageView
}

export interface OptimisticMessageOptions {
  /** Uploaded attachment metadata so the optimistic bubble shows files before confirmation. */
  attachments?: readonly Attachment[]
}

/** Build the attachments shown on an optimistic bubble from full previews or raw extras. */
export function optimisticAttachmentsOf(
  extras: MessageExtras,
  options?: OptimisticMessageOptions,
): Attachment[] {
  if (options?.attachments?.length) return [...options.attachments]
  return (extras.attachments ?? []).map((file) => ({
    fileId: file.fileId,
    name: file.name,
    mime: 'text/plain' as FileType,
    size: 0,
  }))
}

export function buildOptimisticMessage(input: {
  clientId: string
  conversationId: string
  content: string
  attachments?: readonly Attachment[]
  comments?: readonly LineComment[]
  createdAt: number
}): MessageView {
  return {
    id: input.clientId,
    conversationId: input.conversationId,
    author: 'user',
    kind: 'chat',
    content: input.content,
    blocks: [],
    ask: null,
    attachments: input.attachments ? [...input.attachments] : [],
    mentions: [],
    comments: input.comments ? [...input.comments] : [],
    cites: [],
    relayedTo: [],
    replyTo: null,
    reactions: [],
    createdAt: input.createdAt,
    editedAt: null,
    editReason: null,
    originalContent: null,
    editedSinceRead: false,
  }
}

/** Pick a timestamp that keeps newly sent optimistic messages ordered at the bottom of the log. */
export function nextOptimisticTimestamp(
  now: number,
  messages: readonly Pick<MessageView, 'createdAt'>[],
  notices: readonly { createdAt: number }[],
  pending: readonly PendingUserMessage[],
): number {
  let latest = -Infinity
  for (const item of messages) if (item.createdAt > latest) latest = item.createdAt
  for (const item of notices) if (item.createdAt > latest) latest = item.createdAt
  for (const item of pending) if (item.message.createdAt > latest) latest = item.message.createdAt
  return Number.isFinite(latest) ? Math.max(now, latest + 1) : now
}

function attachmentsMatch(a: readonly Attachment[], b: readonly Attachment[]): boolean {
  if (a.length !== b.length) return false
  return a.every((file, index) => file.fileId === b[index]?.fileId)
}

function commentsMatch(a: readonly LineComment[], b: readonly LineComment[]): boolean {
  if (a.length !== b.length) return false
  return a.every(
    (comment, index) =>
      comment.path.trim() === b[index]?.path.trim() &&
      comment.line === b[index]?.line &&
      comment.body.trim() === b[index]?.body.trim() &&
      comment.side === b[index]?.side,
  )
}

function matchesServerMessage(pending: PendingUserMessage, candidate: MessageView): boolean {
  if (candidate.author !== 'user' || candidate.kind !== 'chat') return false
  if (pending.baselineIds.includes(candidate.id)) return false
  if (candidate.content !== pending.message.content) return false
  if (!attachmentsMatch(pending.message.attachments, candidate.attachments)) return false
  return commentsMatch(pending.message.comments, candidate.comments)
}

/**
 * Filter out pending messages whose confirmed server message is already in `messages`
 * (for example when an event-driven refresh arrives slightly before the `postMessage` response),
 * returning both the still-pending entries and a serverId -> clientId map for stable render keys.
 */
export function reconcilePendingMessages(
  pending: readonly PendingUserMessage[],
  messages: readonly MessageView[],
): {
  visible: PendingUserMessage[]
  matchedKeys: Map<string, string>
} {
  if (!pending.length) return { visible: [], matchedKeys: new Map() }
  const matchedKeys = new Map<string, string>()
  const visible: PendingUserMessage[] = []
  for (const entry of pending) {
    const match = messages.find(
      (candidate) => !matchedKeys.has(candidate.id) && matchesServerMessage(entry, candidate),
    )
    if (match) matchedKeys.set(match.id, entry.clientId)
    else visible.push(entry)
  }
  return { visible, matchedKeys }
}
