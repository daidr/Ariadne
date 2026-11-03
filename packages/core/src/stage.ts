import type { ReportKind } from './events'

/** What a thread waits on. The first matching rule wins; see docs/architecture.md. */
export type ThreadStage = 'answer' | 'working' | 'blocked' | 'review' | 'done' | 'queued' | 'idle'

export interface StageFacts {
  openAttentions: number
  started: boolean
  lastReport: ReportKind | null
  /** A turn of the thread's agent is running now. */
  turnRunning: boolean
  /** The last turn failed (e.g. the model request errored) and waits for a retry. */
  turnFailed: boolean
}

export function threadStage(facts: StageFacts): ThreadStage {
  // An unanswered question outranks everything, even `done`: the thread is not finished
  // for the user while it still asks them something.
  if (facts.openAttentions > 0) return 'answer'
  // Whatever it reported before, a thread with a running turn is at work again.
  if (facts.turnRunning) return 'working'
  if (facts.turnFailed) return 'blocked'
  if (facts.lastReport === 'ready') return 'review'
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
