import type { NeedsMeItem, ThreadStage, ThreadView } from '@ariadne/core'
import { t } from './i18n'

/** How each thread stage is tinted wherever it is shown. */
export const stageTone: Record<
  ThreadStage,
  'warning' | 'primary' | 'danger' | 'neutral' | 'success'
> = {
  answer: 'warning',
  stopped: 'neutral',
  halted: 'warning',
  silent: 'warning',
  review: 'primary',
  blocked: 'danger',
  queued: 'neutral',
  working: 'primary',
  idle: 'neutral',
  done: 'success',
}

/** How a pull request's checks are tinted. */
export function checksTone(state: 'none' | 'pending' | 'success' | 'failure' | undefined) {
  switch (state) {
    case 'success':
      return 'success'
    case 'failure':
      return 'danger'
    case 'pending':
      return 'warning'
    default:
      return 'neutral'
  }
}

/**
 * Stages in which a thread's last report says what the user should do: review its work,
 * or unblock it. A question it asks speaks for itself.
 */
export function showsHeadline(stage: ThreadStage): boolean {
  return stage === 'review' || stage === 'blocked'
}

/**
 * The one line shown for a thread wherever it is listed: what its last report asks of the
 * user while it waits on them, or what it finished with. Once it works again, what it last
 * asked no longer holds.
 */
export function threadLine(thread: ThreadView): string | null {
  if (!thread.lastHeadline) return null
  return showsHeadline(thread.stage) || thread.stage === 'done' ? thread.lastHeadline : null
}

/**
 * What a thread is up to, in one line wherever it is shown with room for one: what it is
 * doing while it works, the question it waits on, why it stands still, or what its last
 * report asks of the user. `question` is the title of the card it asked, if any.
 */
export function statusLine(
  thread: ThreadView,
  question: string | null,
): { text: string; live: boolean } | null {
  if (thread.review?.reviewing) {
    return { text: t('review.underway', { round: thread.review.rounds }), live: true }
  }
  switch (thread.stage) {
    case 'working':
      return thread.doing ? { text: thread.doing, live: true } : null
    case 'answer':
      return question ? { text: question, live: false } : null
    case 'halted':
    case 'silent':
      return { text: t(`thread.stage.${thread.stage}`), live: false }
  }
  const line = threadLine(thread)
  return line ? { text: line, live: false } : null
}

/** The title of the card a thread waits on the user's answer to, if any. */
export function questionOf(threadId: string, needsMe: NeedsMeItem[] | undefined): string | null {
  for (const item of needsMe ?? []) {
    if (item.kind !== 'attention') continue
    const { attention } = item
    if (attention.threadId === threadId || attention.joinedBy.includes(threadId)) {
      const { block } = attention
      return block.type === 'choice' ? block.question : block.title
    }
  }
  return null
}

/** How the thread list groups threads, in the order the groups are shown. */
export const threadGroups = [
  'review',
  'waiting',
  'working',
  'paused',
  'idle',
  'done',
  'archived',
] as const
export type ThreadGroup = (typeof threadGroups)[number]

/** The group a thread is listed in: archived ones go last, whatever they ended as. */
export const groupOfThread = (thread: ThreadView): ThreadGroup =>
  thread.archived ? 'archived' : groupOf[thread.stage]

export const groupOf: Record<ThreadStage, Exclude<ThreadGroup, 'archived'>> = {
  review: 'review',
  answer: 'waiting',
  blocked: 'waiting',
  halted: 'waiting',
  silent: 'waiting',
  working: 'working',
  queued: 'working',
  idle: 'idle',
  stopped: 'paused',
  done: 'done',
}

export type ThreadSort = 'active' | 'title' | 'created'

/** Threads in the order the list shows them within a group: the newest first, or by title. */
export function sortThreads(threads: ThreadView[], sort: ThreadSort, locale: string) {
  const collator = new Intl.Collator(locale, { numeric: true })
  return threads.toSorted((a, b) =>
    sort === 'title'
      ? collator.compare(a.title, b.title)
      : sort === 'created'
        ? b.createdAt - a.createdAt
        : b.activeAt - a.activeAt,
  )
}
