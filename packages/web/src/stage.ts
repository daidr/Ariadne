import type { ThreadStage } from '@ariadne/core'

/** How each thread stage is tinted wherever it is shown. */
export const stageTone: Record<
  ThreadStage,
  'warning' | 'primary' | 'danger' | 'neutral' | 'success'
> = {
  answer: 'warning',
  review: 'primary',
  blocked: 'danger',
  queued: 'neutral',
  working: 'primary',
  idle: 'neutral',
  done: 'success',
}
