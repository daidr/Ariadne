import type { ReportKind } from './events'

/** What a thread waits on. The first matching rule wins; see docs/architecture.md. */
export type ThreadStage = 'answer' | 'review' | 'blocked' | 'queued' | 'working' | 'idle' | 'done'

export interface StageFacts {
  openAttentions: number
  started: boolean
  lastReport: ReportKind | null
}

export function threadStage(facts: StageFacts): ThreadStage {
  if (facts.lastReport === 'done') return 'done'
  if (facts.openAttentions > 0) return 'answer'
  if (facts.lastReport === 'ready') return 'review'
  if (facts.lastReport === 'blocked' || facts.lastReport === 'failed') return 'blocked'
  if (!facts.started) return 'queued'
  if (facts.lastReport === 'waiting') return 'idle'
  return 'working'
}

/** Stages in which the thread cannot go on without the user. */
export function needsUser(stage: ThreadStage): boolean {
  return stage === 'answer' || stage === 'review' || stage === 'blocked'
}
