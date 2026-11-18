/** What a thread waits on. The first matching rule wins; see docs/architecture.md. */
export type ThreadStage =
  | 'answer'
  | 'stopped'
  | 'halted'
  | 'silent'
  | 'working'
  | 'blocked'
  | 'review'
  | 'done'
  | 'queued'
  | 'idle'
