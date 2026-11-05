import { nextRun, type Ariadne, type AnyStoredEvent } from '@ariadne/core'

/** A timer runs this long at most before the next due moment is looked up again. */
const MAX_TIMER_MS = 60 * 60 * 1000
/**
 * A routine run found later than this (the machine slept, or the process stalled) is
 * skipped like one missed while the server was down: the next one comes on time.
 */
export const ROUTINE_GRACE_MS = 5 * 60 * 1000

/** Events after which what comes due next may have changed. */
const CHANGES = new Set<AnyStoredEvent['type']>([
  'conversation.wait_set',
  'conversation.wait_ended',
  'routine.ran',
  'project.created',
  'project.deleted',
  'project.settings.set',
])

/**
 * Wakes conversations whose wait came due, and the coordinator of a project whose routine
 * came due. What is due lives in the log (waits) and the project settings (routines), so a
 * restart picks it up again: a wait that came due while the server was down ends at once;
 * a routine's run missed that way is not made up, the next one comes on time.
 */
export class Timers {
  private timer: ReturnType<typeof setTimeout> | null = null
  private unsubscribe: (() => void) | null = null
  /** When each routine was last looked at, so a run is never made twice. */
  private readonly routinesFrom = new Map<string, number>()
  private scheduled = false

  constructor(
    private readonly app: Ariadne,
    private readonly now: () => number = Date.now,
  ) {}

  start(): void {
    this.unsubscribe = this.app.subscribe((events) => {
      for (const event of events) {
        // Changed routines count from now: a time moved to earlier today is not run late.
        if (event.type === 'project.settings.set' || event.type === 'project.deleted') {
          for (const key of this.routinesFrom.keys()) {
            if (key.startsWith(`${event.projectId}/`)) this.routinesFrom.delete(key)
          }
        }
      }
      if (events.some((event) => CHANGES.has(event.type))) this.schedule()
    })
    this.schedule()
  }

  stop(): void {
    this.unsubscribe?.()
    this.unsubscribe = null
    if (this.timer) clearTimeout(this.timer)
    this.timer = null
  }

  // Ending a wait appends from inside a listener; that waits for the next microtask.
  private schedule(): void {
    if (this.scheduled || !this.unsubscribe) return
    this.scheduled = true
    queueMicrotask(() => {
      this.scheduled = false
      this.check()
    })
  }

  /** End what is due, then sleep until the next thing comes due. Tests call it by hand. */
  check(): void {
    if (!this.unsubscribe) return
    if (this.timer) clearTimeout(this.timer)
    this.timer = null
    const now = this.now()
    let next = Number.POSITIVE_INFINITY
    for (const wait of this.app.listWaits()) {
      if (wait.until <= now) {
        try {
          this.app.endWait(wait.conversationId, 'due', 'time', wait.waitId)
        } catch (error) {
          console.error('wait failed to end', error)
        }
      } else next = Math.min(next, wait.until)
    }
    for (const project of this.app.listProjects()) {
      for (const routine of project.settings.routines) {
        const key = `${project.id}/${routine.id}`
        // A routine first seen now starts counting now: runs before it was set up, or
        // while the server was down, are not made up.
        const from = this.routinesFrom.get(key) ?? now
        const due = nextRun(routine, from)
        if (due !== null && due <= now) {
          this.routinesFrom.set(key, now)
          if (now - due <= ROUTINE_GRACE_MS) {
            try {
              this.app.runRoutine(project.id, routine.id, due)
            } catch (error) {
              console.error('routine failed to run', error)
            }
          }
          const after = nextRun(routine, now)
          if (after !== null) next = Math.min(next, after)
        } else {
          this.routinesFrom.set(key, now)
          if (due !== null) next = Math.min(next, due)
        }
      }
    }
    if (next === Number.POSITIVE_INFINITY) return
    this.timer = setTimeout(() => this.check(), Math.min(Math.max(next - now, 0), MAX_TIMER_MS))
  }
}
