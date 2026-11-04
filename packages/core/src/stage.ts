import type { ReportKind } from './events'

/** What a thread waits on. The first matching rule wins; see docs/architecture.md. */
export type ThreadStage =
  'answer' | 'stopped' | 'working' | 'blocked' | 'review' | 'done' | 'queued' | 'idle'

export interface StageFacts {
  openAttentions: number
  started: boolean
  lastReport: ReportKind | null
  /** A turn of the thread's agent is running now. */
  turnRunning: boolean
  /** New input waits for a free slot. */
  turnPending: boolean
  /** The last turn failed (e.g. the model request errored) and waits for a retry. */
  turnFailed: boolean
  /** Its pull request's checks failed too many times in a row for it to try again alone. */
  checksGaveUp?: boolean
  /** Held by the coordinator or the user until resumed. */
  stopped?: boolean
  /**
   * Its work landed after it last reported: the user merged its branch, or its pull request
   * was merged. A review it waited on is then over.
   */
  landed?: boolean
  /** The user marked what it last reported as reviewed. */
  reviewed?: boolean
}

export function threadStage(facts: StageFacts): ThreadStage {
  // An unanswered question outranks everything, even `done`: the thread is not finished
  // for the user while it still asks them something.
  if (facts.openAttentions > 0) return 'answer'
  // Held: nothing runs until it is resumed, even if its last turn is still winding down.
  if (facts.stopped) return 'stopped'
  // Whatever it reported before, a thread with a running turn is at work again.
  if (facts.turnRunning) return 'working'
  // Waiting for a slot to handle new input, e.g. an answer after it reported done.
  if (facts.turnPending) return 'queued'
  if (facts.turnFailed || facts.checksGaveUp) return 'blocked'
  if (facts.lastReport === 'ready') return facts.landed || facts.reviewed ? 'done' : 'review'
  if (facts.lastReport === 'blocked' || facts.lastReport === 'failed') return 'blocked'
  if (facts.lastReport === 'done') return 'done'
  if (!facts.started) return 'queued'
  if (facts.lastReport === 'waiting') return 'idle'
  return 'working'
}

/** Stages in which the thread cannot go on without the user. */
export function needsUser(stage: ThreadStage): boolean {
  return stage === 'answer' || stage === 'review' || stage === 'blocked'
}
