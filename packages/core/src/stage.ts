import type { ReportKind } from './events'

/** What a thread waits on. The first matching rule wins; see docs/architecture.md. */
export type ThreadStage = 'answer' | 'review' | 'blocked' | 'done' | 'queued' | 'idle' | 'working'

export interface StageFacts {
  openAttentions: number
  started: boolean
  lastReport: ReportKind | null
}

export function threadStage(facts: StageFacts): ThreadStage {
  // An unanswered question outranks everything, even `done`: the thread is not finished
  // for the user while it still asks them something.
  if (facts.openAttentions > 0) return 'answer'
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
