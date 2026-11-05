import type { LineComment } from '@ariadne/core'
import { defineCodeExtension, type CodeExtension } from '@dou-ui/code'
import { MessageIcon, PlusIcon } from '@dou-ui/icons'
import type { CommentAction, CommentThread } from 'dou-ui'
import { h, ref, type Ref } from 'vue'
import { commentAuthor } from '../artifacts/comment-threads'
import { t } from '../i18n'
import { state } from '../store'
import LineCommentWidget from './LineCommentWidget.vue'

/** A comment written on a line and not sent yet. */
export interface DraftComment extends LineComment {
  id: string
  createdAt: string
}

let next = 0
const newId = () => `lc_${Date.now().toString(36)}_${(++next).toString(36)}`

/** The thread's unsent comments, kept in the store so they survive leaving the view. */
export function draftsOf(threadId: string): DraftComment[] {
  return (state.lineComments[threadId] ?? []) as DraftComment[]
}

function setDrafts(threadId: string, drafts: DraftComment[]) {
  if (drafts.length) state.lineComments[threadId] = drafts
  else delete state.lineComments[threadId]
}

/** One line's comments as one comment card. */
export function lineThread(path: string, line: number, comments: DraftComment[]): CommentThread {
  return {
    id: `${path}:${line}`,
    comments: comments.map((comment) => ({
      id: comment.id,
      author: commentAuthor('user'),
      body: comment.body,
      createdAt: comment.createdAt,
    })),
  }
}

/**
 * What a card's action does to the drafts: a first comment or a reply adds one on the
 * line, an edit changes one, a delete removes one, and removing the card drops the
 * line's comments. Resolving changes nothing: its ✓ reads as "done writing", and a draft
 * lost to it could not be had back.
 */
export function applyDraftAction(
  drafts: readonly DraftComment[],
  at: { path: string; line: number; quote: string },
  action: CommentAction,
): DraftComment[] {
  const onLine = (comment: DraftComment) => comment.path === at.path && comment.line === at.line
  switch (action.type) {
    case 'create':
    case 'reply': {
      const body =
        action.type === 'create' ? (action.thread.comments[0]?.body ?? '') : action.comment.body
      if (!body.trim()) return [...drafts]
      return [
        ...drafts,
        { ...at, body: body.trim(), id: newId(), createdAt: new Date().toISOString() },
      ]
    }
    case 'edit':
      return drafts.map((comment) =>
        comment.id === action.commentId ? { ...comment, body: action.body } : comment,
      )
    case 'delete':
      return drafts.filter((comment) => comment.id !== action.commentId)
    case 'remove':
      return drafts.filter((comment) => !onLine(comment))
    default:
      return [...drafts]
  }
}

/** The comments as the thread reads them: in file order, then line order. */
export function sendable(drafts: readonly DraftComment[]): LineComment[] {
  return drafts
    .toSorted((a, b) => a.path.localeCompare(b.path) || a.line - b.line)
    .map(({ path, line, quote, body }) => ({ path, line, quote, body }))
}

/**
 * Comments on the lines of a file's new version in the diff: a press beside a line
 * starts one under it, and the line's comments show there until they are sent.
 */
export function useLineComments(
  threadId: () => string,
  path: () => string | null,
  text: () => string | null,
): { extension: CodeExtension; drafting: Ref<number | null> } {
  const drafting = ref<number | null>(null)
  const quoteOf = (line: number) => (text() ?? '').split('\n')[line - 1] ?? ''
  const forFile = () => draftsOf(threadId()).filter((comment) => comment.path === path())
  const act = (line: number, action: CommentAction) => {
    const file = path()
    if (!file) return
    setDrafts(
      threadId(),
      applyDraftAction(draftsOf(threadId()), { path: file, line, quote: quoteOf(line) }, action),
    )
    if (action.type === 'create') drafting.value = null
  }
  const extension = defineCodeExtension({
    name: 'line-comments',
    gutters: [
      {
        name: 'line-comments',
        width: 18,
        markers: (editor) => {
          const commented = new Set(forFile().map((comment) => comment.line))
          const markers = []
          for (let line = editor.visible.from; line <= editor.visible.to; line++) {
            markers.push(
              commented.has(line)
                ? {
                    line,
                    icon: MessageIcon,
                    class: 'line-comment-mark',
                    title: t('changes.commented'),
                  }
                : { line, icon: PlusIcon, class: 'line-comment-add', title: t('changes.comment') },
            )
          }
          return markers
        },
        onPress: (line) => {
          if (!forFile().some((comment) => comment.line === line)) drafting.value = line
        },
      },
    ],
    widgets: () => {
      const byLine = new Map<number, DraftComment[]>()
      for (const comment of forFile()) {
        byLine.set(comment.line, [...(byLine.get(comment.line) ?? []), comment])
      }
      const lines = [...byLine.keys()]
      if (drafting.value !== null && !byLine.has(drafting.value)) lines.push(drafting.value)
      return lines.map((line) => ({
        id: `line-${line}`,
        line,
        height: 120,
        label: t('changes.commentOn', { line }),
        render: () =>
          h(LineCommentWidget, {
            thread: byLine.has(line) ? lineThread(path() ?? '', line, byLine.get(line)!) : null,
            onAction: (action: CommentAction) => act(line, action),
            onCancel: () => (drafting.value = null),
          }),
      }))
    },
  })
  return { extension, drafting }
}
