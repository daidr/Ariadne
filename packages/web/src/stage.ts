import type { ThreadStage } from '@ariadne/core'

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
