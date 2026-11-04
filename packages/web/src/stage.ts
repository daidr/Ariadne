import type { ThreadStage, ThreadView } from '@ariadne/core'

/** How each thread stage is tinted wherever it is shown. */
export const stageTone: Record<
  ThreadStage,
  'warning' | 'primary' | 'danger' | 'neutral' | 'success'
> = {
  answer: 'warning',
  stopped: 'neutral',
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

/** How the thread list groups threads, in the order the groups are shown. */
export const threadGroups = ['review', 'waiting', 'working', 'idle', 'done'] as const
export type ThreadGroup = (typeof threadGroups)[number]

export const groupOf: Record<ThreadStage, ThreadGroup> = {
  review: 'review',
  answer: 'waiting',
  blocked: 'waiting',
  working: 'working',
  queued: 'working',
  idle: 'idle',
  stopped: 'idle',
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
