import { optionalBool, optionalStr, ToolInputError, type Tool } from './tool-helpers'
import { localTime } from './transcript'

/** The longest one wait lasts: a week. */
export const MAX_WAIT_MINUTES = 7 * 24 * 60
/** What the note to oneself may hold. */
const MAX_NOTE = 500

const CLOCK = /^([01]?\d|2[0-3]):([0-5]\d)$/
const DATE_TIME = /^(\d{4})-(\d{2})-(\d{2})[ T]([01]?\d|2[0-3]):([0-5]\d)$/

/**
 * When a wait ends, from what the model gave: minutes from `now`, or a local clock time
 * (`14:30`, the next one to come) or date and time (`2026-11-05 14:30`).
 */
export function waitUntil(args: { minutes?: unknown; at?: unknown }, now: number): number {
  const { minutes, at } = args
  if ((minutes === undefined) === (at === undefined)) {
    throw new ToolInputError('Give either "minutes" or "at".')
  }
  if (minutes !== undefined) {
    if (typeof minutes !== 'number' || !(minutes >= 1) || minutes > MAX_WAIT_MINUTES) {
      throw new ToolInputError(`"minutes" must be a number from 1 to ${MAX_WAIT_MINUTES}.`)
    }
    return now + Math.round(minutes * 60_000)
  }
  if (typeof at !== 'string') throw new ToolInputError('"at" must be a string.')
  const text = at.trim()
  let until: number
  const clock = CLOCK.exec(text)
  const full = DATE_TIME.exec(text)
  if (clock) {
    const day = new Date(now)
    const today = new Date(
      day.getFullYear(),
      day.getMonth(),
      day.getDate(),
      Number(clock[1]),
      Number(clock[2]),
    )
    until = today.getTime() > now ? today.getTime() : today.setDate(today.getDate() + 1)
  } else if (full) {
    const [, y, mo, d, h, mi] = full.map(Number) as number[]
    until = new Date(y!, mo! - 1, d!, h!, mi!).getTime()
  } else {
    throw new ToolInputError('"at" must read like "14:30" or "2026-11-05 14:30" (local time).')
  }
  if (until <= now) throw new ToolInputError(`${text} has already passed.`)
  if (until - now > MAX_WAIT_MINUTES * 60_000) {
    throw new ToolInputError('A wait lasts a week at most.')
  }
  return until
}

/**
 * Come back later: for a deploy, a long job elsewhere, or a reply that takes time. The
 * conversation is woken when the time comes, with the note, even after a restart; the
 * user sees the wait and can call it off or wake it early.
 */
export const waitTool: Tool = {
  spec: {
    name: 'wait',
    description: `Be woken later to look at something that takes time and wakes nobody by itself: a deploy, a job outside Ariadne, a page that updates. Give "minutes" from now or "at" a local time ("14:30", or "2026-11-05 14:30"), and in "note" what to look at then. End your turn after it; you are woken at that time with the note. The user sees the wait and may call it off or wake you sooner. New input (a message, an answer, checks of your pull request) wakes you as usual and leaves the wait in place. You have one wait at a time: calling wait again replaces it; "cancel": true drops it. Do not use it to wait for things that wake you anyway: answers, thread reports, checks.`,
    parameters: {
      type: 'object',
      properties: {
        minutes: { type: 'number', description: `From now, 1 to ${MAX_WAIT_MINUTES}.` },
        at: { type: 'string', description: 'A local time, e.g. "14:30" or "2026-11-05 14:30".' },
        note: { type: 'string', description: 'What to look at then, in one sentence.' },
        cancel: { type: 'boolean', description: 'Drop the wait you set instead.' },
      },
    },
  },
  run(args, ctx) {
    if (optionalBool(args, 'cancel')) {
      const dropped = ctx.app.endWait(ctx.conversationId, 'cancelled', ctx.conversationId)
      return { content: dropped ? 'Your wait is dropped.' : 'You had no wait.' }
    }
    const now = Date.now()
    const until = waitUntil(args, now)
    const note = optionalStr(args, 'note')?.trim()
    if (!note) throw new ToolInputError('"note" must say what to look at then.')
    if (note.length > MAX_NOTE)
      throw new ToolInputError(`"note" is ${MAX_NOTE} characters at most.`)
    ctx.app.setWait(ctx.conversationId, { until, note })
    return {
      content: `You will be woken at ${localTime(until)} (local time; it is ${localTime(now)} now). End your turn now.`,
    }
  },
}
