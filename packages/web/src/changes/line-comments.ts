import type { LineComment } from '@ariadne/core'
import type {
  CodeDiffGutter,
  CodeDiffGutterMarker,
  CodeDiffSide,
  CodeDiffWidget,
} from '@dou-ui/code'
import { MessageIcon, PlusIcon } from '@dou-ui/icons'
import type { CommentAction, CommentThread } from 'dou-ui'
import { computed, h, ref, watch, type ComputedRef } from 'vue'
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
export function lineThread(
  path: string,
  spot: CommentSpot,
  comments: DraftComment[],
): CommentThread {
  return {
    id: `${path}:${spotKey(spot)}`,
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
 * line's comments. Drafts have no resolved state; the card offers no way to resolve one.
 */
export function applyDraftAction(
  drafts: readonly DraftComment[],
  at: Omit<LineComment, 'body'>,
  action: CommentAction,
): DraftComment[] {
  const onLine = (comment: DraftComment) =>
    comment.path === at.path &&
    comment.line === at.line &&
    sideOf(comment) === (at.side ?? 'modified')
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
    .toSorted(
      (a, b) =>
        a.path.localeCompare(b.path) ||
        (a.side === b.side ? 0 : a.side ? -1 : 1) ||
        a.line - b.line,
    )
    .map(({ path, line, quote, body, side }) => ({
      path,
      line,
      quote,
      body,
      ...(side ? { side } : {}),
    }))
}

/** Where a comment sits: the side of the diff and the line on it. */
export interface CommentSpot {
  side: CodeDiffSide
  line: number
}

const sideOf = (comment: LineComment): CodeDiffSide => comment.side ?? 'modified'
const spotKey = (spot: CommentSpot) => `${spot.side}:${spot.line}`

/**
 * Comments on the lines of a file in the diff, on either side: a press beside a line
 * starts one under it, and the line's comments show there until they are sent. A plus
 * shows beside the line under the pointer; it does not keep unchanged lines unfolded.
 */
export function useLineComments(
  threadId: () => string,
  path: () => string | null,
  texts: () => { original: string; modified: string } | null,
): { gutter: ComputedRef<CodeDiffGutter>; widgets: ComputedRef<CodeDiffWidget[]> } {
  const drafting = ref<CommentSpot | null>(null)
  const linesOf = (side: CodeDiffSide) => (texts()?.[side] ?? '').split('\n')
  const forFile = () => draftsOf(threadId()).filter((comment) => comment.path === path())
  const act = (spot: CommentSpot, action: CommentAction) => {
    const file = path()
    if (!file) return
    const quote = linesOf(spot.side)[spot.line - 1] ?? ''
    const at = {
      path: file,
      line: spot.line,
      quote,
      ...(spot.side === 'original' ? { side: 'original' as const } : {}),
    }
    setDrafts(threadId(), applyDraftAction(draftsOf(threadId()), at, action))
    if (action.type === 'create') drafting.value = null
  }
  // A file shown elsewhere drops the comment being started.
  watch(path, () => (drafting.value = null))

  const gutter = computed<CodeDiffGutter>(() => {
    const commented = new Set(
      forFile().map((comment) => spotKey({ side: sideOf(comment), line: comment.line })),
    )
    // Commented lines get a mark; every other line shows a plus under the pointer.
    const markers: CodeDiffGutterMarker[] = [...commented].map((key) => {
      const [side, line] = key.split(':') as [CodeDiffSide, string]
      return {
        side,
        line: Number(line),
        icon: MessageIcon,
        class: 'line-comment-mark',
        title: t('changes.commented'),
      }
    })
    return {
      name: 'line-comments',
      width: 18,
      markers,
      hoverMarker: { icon: PlusIcon, title: t('changes.comment') },
      onPress: (line, _event, side) => {
        const spot = { side, line }
        if (!forFile().some((c) => spotKey({ side: sideOf(c), line: c.line }) === spotKey(spot))) {
          drafting.value = spot
        }
      },
    }
  })

  const widgets = computed<CodeDiffWidget[]>(() => {
    const bySpot = new Map<string, { spot: CommentSpot; comments: DraftComment[] }>()
    for (const comment of forFile()) {
      const spot = { side: sideOf(comment), line: comment.line }
      const entry = bySpot.get(spotKey(spot)) ?? { spot, comments: [] }
      entry.comments.push(comment)
      bySpot.set(spotKey(spot), entry)
    }
    if (drafting.value && !bySpot.has(spotKey(drafting.value))) {
      bySpot.set(spotKey(drafting.value), { spot: drafting.value, comments: [] })
    }
    return [...bySpot.values()].map(({ spot, comments }) => ({
      id: `line-${spotKey(spot)}`,
      side: spot.side,
      line: spot.line,
      height: 120,
      label: t('changes.commentOn', { line: spot.line }),
      render: () =>
        h(LineCommentWidget, {
          thread: comments.length ? lineThread(path() ?? '', spot, comments) : null,
          onAction: (action: CommentAction) => act(spot, action),
          onCancel: () => (drafting.value = null),
        }),
    }))
  })
  return { gutter, widgets }
}
