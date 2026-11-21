import type { NeedsMeItem, ThreadPullRequest, ThreadStage, ThreadView } from '@ariadne/core'
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
export function checksTone(
  state: 'none' | 'pending' | 'success' | 'failure' | undefined,
): 'success' | 'danger' | 'warning' | 'neutral' {
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

export function threadStageLabel(stage: ThreadStage, pr?: ThreadPullRequest['state']) {
  if (stage === 'review' && pr === 'open') return t('thread.stage.waitingMerge')
  if (stage === 'review' && pr === 'closed') return t('pr.state.closed')
  return t(`thread.stage.${stage}`)
}

/** Stages where the last report explains the review or what halted the work. */
export function showsHeadline(stage: ThreadStage): boolean {
  return stage === 'review' || stage === 'blocked'
}

/**
 * The one line shown for a thread wherever it is listed: its last result, or why it
 * is waiting to continue.
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
      return { text: thread.doing || t('thread.stage.working'), live: true }
    case 'queued':
      return { text: t('thread.stage.queued'), live: false }
    case 'answer':
      return { text: question || t('thread.stage.answer'), live: false }
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
  'aside',
  'done',
  'archived',
] as const
export type ThreadGroup = (typeof threadGroups)[number]

/** Thread-level actions supplied by the server. Open questions have their own stage. */
export function threadActionIds(items: NeedsMeItem[] | undefined): Set<string> {
  return new Set((items ?? []).flatMap((item) => (item.kind === 'thread' ? [item.threadId] : [])))
}

/** The group a thread is listed in: archived ones go last, whatever they ended as. */
export const groupOfThread = (thread: ThreadView, actions: ReadonlySet<string>): ThreadGroup =>
  thread.archived
    ? 'archived'
    : // Set aside, it is out of the way until its day, unless it is at work or asks something.
      thread.aside && !thread.aside.due && ASIDE_STAGES.has(thread.stage)
      ? 'aside'
      : thread.stage === 'blocked' && actions.has(thread.id)
        ? 'waiting'
        : groupOf[thread.stage]

const ASIDE_STAGES = new Set<ThreadStage>([
  'idle',
  'done',
  'blocked',
  'stopped',
  'halted',
  'silent',
])

export const groupOf: Record<ThreadStage, Exclude<ThreadGroup, 'archived' | 'aside'>> = {
  review: 'review',
  answer: 'waiting',
  blocked: 'idle',
  halted: 'waiting',
  silent: 'waiting',
  working: 'working',
  queued: 'working',
  idle: 'idle',
  stopped: 'paused',
  done: 'done',
}

/**
 * Threads in the order the list shows them within a group.
 * Active groups keep creation order so concurrent turns and check syncs never jump rows;
 * finished and archived groups still show the most recently active threads first.
 */
export function sortThreads(threads: readonly ThreadView[], group?: ThreadGroup): ThreadView[] {
  if (group === 'done' || group === 'archived') {
    return threads.toSorted(
      (a, b) => b.activeAt - a.activeAt || b.createdAt - a.createdAt || b.id.localeCompare(a.id),
    )
  }
  return threads.toSorted((a, b) => b.createdAt - a.createdAt || b.id.localeCompare(a.id))
}
