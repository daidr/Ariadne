import type { ArtifactCommentThreadView } from '@ariadne/core'
import type { CommentAuthor, CommentThread } from 'dou-ui'
import { authorName } from './author'

/** Who wrote a comment, as the comment cards name them. */
export const commentAuthor = (id: string): CommentAuthor => ({ id, name: authorName(id) })

const iso = (at: number) => new Date(at).toISOString()

/** A thread from the server as the comment cards take it. */
export function toCommentThread(thread: ArtifactCommentThreadView): CommentThread {
  return {
    id: thread.id,
    comments: thread.comments.map((c) => ({
      id: c.id,
      author: commentAuthor(c.author),
      body: c.body,
      createdAt: iso(c.createdAt),
      ...(c.editedAt ? { editedAt: iso(c.editedAt) } : {}),
    })),
    ...(thread.resolved
      ? { resolved: { by: commentAuthor(thread.resolved.by), at: iso(thread.resolved.at) } }
      : {}),
  }
}
