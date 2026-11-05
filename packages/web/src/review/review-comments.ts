import type { ReviewComment, ReviewReply, ReviewRoundView } from '@ariadne/core'
import { defineCodeExtension, type CodeExtension } from '@dou-ui/code'
import { ScanSearchIcon } from '@dou-ui/icons'
import type { CommentAuthor, CommentThread } from 'dou-ui'
import { h } from 'vue'
import { t } from '../i18n'
import { state } from '../store'
import ReviewCommentWidget from './ReviewCommentWidget.vue'

/** One reviewer comment with its round, what the author answered, and when. */
export interface ReviewNote {
  id: string
  round: ReviewRoundView
  comment: ReviewComment
  reply: ReviewReply | null
}

/** Every comment of every round, in order, with the author's answer when it gave one. */
export function reviewNotes(rounds: readonly ReviewRoundView[]): ReviewNote[] {
  return rounds.flatMap((round, r) =>
    round.comments.map((comment, index) => ({
      id: `r${r}c${index}`,
      round,
      comment,
      reply: round.replies?.find((reply) => reply.comment === index + 1) ?? null,
    })),
  )
}

/**
 * Where a comment shows in the file as it is now: its own line while that still reads
 * as quoted, else the nearest line that does, else the line it was written on. A fixed
 * line usually reads differently, so the comment stays near where it was.
 */
export function anchorLine(lines: readonly string[], comment: ReviewComment): number | null {
  const quote = comment.quote.trim()
  const at = comment.line - 1
  if (quote && lines[at]?.trim() !== quote) {
    let best: number | null = null
    lines.forEach((line, index) => {
      if (line.trim() !== quote) return
      if (best === null || Math.abs(index - at) < Math.abs(best - at)) best = index
    })
    if (best !== null) return best + 1
  }
  if (!lines.length) return null
  return Math.min(comment.line, lines.length)
}

const iso = (at: number | null) => new Date(at ?? 0).toISOString()

/** A comment and its answer as a read-only comment card. */
export function noteThread(note: ReviewNote, author: CommentAuthor): CommentThread {
  const reviewer: CommentAuthor = { id: note.round.reviewerId, name: t('review.reviewer') }
  const { reply } = note
  return {
    id: note.id,
    comments: [
      {
        id: `${note.id}:c`,
        author: reviewer,
        body: note.comment.body,
        createdAt: iso(note.round.submittedAt),
      },
      ...(reply
        ? [
            {
              id: `${note.id}:r`,
              author,
              body: reply.note
                ? t(`review.reply.${reply.outcome}`, { note: reply.note })
                : t(`review.replyBare.${reply.outcome}`),
              createdAt: iso(note.round.submittedAt),
            },
          ]
        : []),
    ],
    // A fixed comment is settled; a declined one stays open with its reason, for the user.
    ...(reply?.outcome === 'fixed' ? { resolved: { by: author, at: iso(null) } } : {}),
  }
}

/** The line comments of a thread's reviews, under the lines of the file shown in the diff. */
export function useReviewComments(
  authorId: () => string,
  notes: () => ReviewNote[],
  path: () => string | null,
  text: () => string | null,
): CodeExtension {
  const placed = () => {
    const file = path()
    const lines = (text() ?? '').split('\n')
    const byLine = new Map<number, ReviewNote[]>()
    for (const note of notes()) {
      if (note.comment.path !== file) continue
      const line = anchorLine(lines, note.comment)
      if (line !== null) byLine.set(line, [...(byLine.get(line) ?? []), note])
    }
    return byLine
  }
  const authorOf = (): CommentAuthor => ({
    id: authorId(),
    name: state.detail?.threads.find((thread) => thread.id === authorId())?.title ?? '',
  })
  return defineCodeExtension({
    name: 'review-comments',
    gutters: [
      {
        name: 'review-comments',
        width: 18,
        markers: () =>
          [...placed().keys()].map((line) => ({
            line,
            icon: ScanSearchIcon,
            class: 'review-comment-mark',
            title: t('review.commented'),
          })),
      },
    ],
    widgets: () =>
      [...placed()].map(([line, list]) => ({
        id: `review-${line}`,
        line,
        height: 140 * list.length,
        label: t('review.commentOn', { line }),
        render: () =>
          h(ReviewCommentWidget, {
            threads: list.map((note) => ({
              thread: noteThread(note, authorOf()),
              label: t('review.commentLabel', {
                round: note.round.round,
                kind: t(`review.kind.${note.comment.kind}`),
              }),
            })),
          }),
      })),
  })
}
