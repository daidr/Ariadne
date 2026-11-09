import type { Routine } from '@ariadne/core'

const TIME = /^([01]\d|2[0-3]):([0-5]\d)$/

/**
 * When a routine runs next after `after`, in local time, as the server works it out
 * (`nextRun` in core): the page runs in the same place, so edits show their next run before
 * they are saved. `null` when it is off or has no days.
 */
export function nextRunOf(routine: Routine, after = Date.now()): number | null {
  if (!routine.enabled || !routine.days.length) return null
  const match = TIME.exec(routine.time)
  if (!match) return null
  const day = new Date(after)
  for (let i = 0; i <= 7; i++) {
    const at = new Date(
      day.getFullYear(),
      day.getMonth(),
      day.getDate() + i,
      Number(match[1]),
      Number(match[2]),
    )
    if (at.getTime() > after && routine.days.includes(at.getDay())) return at.getTime()
  }
  return null
}

/** Weekdays in the order a week reads here: Monday first. */
export const WEEK = [1, 2, 3, 4, 5, 6, 0] as const
