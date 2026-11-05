import { screenText } from '@ariadne/core'
import type { ExecOptions, ExecResult } from '@ariadne/plugin-sdk'

/** Output a background command keeps, its newest part: enough to look back over. */
const KEPT_OUTPUT = 2_000_000
/** How much of a command's output's end the user sees at once. */
const PEEK_CHARS = 20_000
/** Commands one thread may have running in the background at once. */
export const MAX_RUNNING = 4
/** Finished commands a thread keeps to read from; older ones are forgotten. */
const KEPT_FINISHED = 8
/** The longest a background command runs: a server left behind does not run for days. */
export const BACKGROUND_TIMEOUT_MS = 8 * 60 * 60 * 1000

export interface BackgroundCommand {
  /** What the model names it by, e.g. `cmd-3`. */
  id: string
  threadId: string
  command: string
  startedAt: number
  /** How it ended; `null` while it runs. */
  result: { exitCode: number; endedAt: number; stopped: boolean } | null
}

interface Job extends BackgroundCommand {
  /** stdout and stderr as they came, interleaved; only the newest `KEPT_OUTPUT` of it. */
  output: string
  /** How much output came before `output` begins. */
  dropped: number
  /** How much output the model has read, counted from the very start. */
  read: number
  controller: AbortController
  ended: Promise<void>
  /** Wakes whoever waits for more output or the end. */
  changed: Set<() => void>
}

/** What a read gives: what came since the last read, and whether it still runs. */
export interface CommandOutput {
  command: BackgroundCommand
  text: string
  /** Output that came since the last read but was dropped before this one. */
  missed: number
}

/**
 * Commands threads left running in the background, e.g. a dev server or a long build.
 * They outlive the turn that started them, and end when the thread is stopped, its work
 * is reported finished, its worktree goes, or the server stops.
 */
export class BackgroundCommands {
  private readonly jobs = new Map<string, Job>()
  private next = 0

  /** `onChange` hears of a thread whose commands started, ended or were forgotten. */
  constructor(private readonly onChange: (threadId: string) => void = () => {}) {}

  /** The thread's commands, oldest first. */
  list(threadId: string): BackgroundCommand[] {
    return [...this.jobs.values()].filter((job) => job.threadId === threadId).map(view)
  }

  running(threadId: string): number {
    return this.list(threadId).filter((job) => !job.result).length
  }

  /**
   * Start `command` with `exec`, which gets a signal that stops it and a callback for its
   * output. Throws when the thread already has `MAX_RUNNING` commands running.
   */
  start(
    threadId: string,
    command: string,
    exec: (options: Pick<ExecOptions, 'signal' | 'onOutput' | 'timeoutMs'>) => Promise<ExecResult>,
  ): BackgroundCommand {
    if (this.running(threadId) >= MAX_RUNNING) {
      throw new Error(
        `${MAX_RUNNING} commands of yours already run in the background; stop one first (stop_command)`,
      )
    }
    this.forgetOld(threadId)
    const controller = new AbortController()
    const job: Job = {
      id: `cmd-${++this.next}`,
      threadId,
      command,
      startedAt: Date.now(),
      result: null,
      output: '',
      dropped: 0,
      read: 0,
      controller,
      ended: Promise.resolve(),
      changed: new Set(),
    }
    const wake = () => {
      for (const done of job.changed) done()
      job.changed.clear()
    }
    const append = (chunk: string) => {
      job.output += chunk
      // Cut once well past the bound, not on every piece: a chatty command sends many.
      if (job.output.length > KEPT_OUTPUT * 1.25) {
        const cut = job.output.length - KEPT_OUTPUT
        job.output = job.output.slice(cut)
        job.dropped += cut
      }
      wake()
    }
    job.ended = exec({
      signal: controller.signal,
      onOutput: append,
      timeoutMs: BACKGROUND_TIMEOUT_MS,
    })
      .then(
        (result) => result.exitCode,
        (error: unknown) => {
          append(`\n${error instanceof Error ? error.message : String(error)}\n`)
          return -1
        },
      )
      .then((exitCode) => {
        job.result = { exitCode, endedAt: Date.now(), stopped: controller.signal.aborted }
        wake()
        this.onChange(threadId)
      })
    this.jobs.set(job.id, job)
    this.onChange(threadId)
    return view(job)
  }

  /**
   * What came since the last read, once there is some, the command ended, or `waitMs`
   * passed. `null` when the thread has no such command.
   */
  async read(threadId: string, id: string, waitMs = 0, signal?: AbortSignal) {
    const job = this.own(threadId, id)
    if (!job) return null
    if (waitMs > 0 && !job.result && job.read >= job.dropped + job.output.length) {
      await new Promise<void>((done) => {
        const timer = setTimeout(finish, waitMs)
        function finish() {
          clearTimeout(timer)
          signal?.removeEventListener('abort', finish)
          job!.changed.delete(finish)
          done()
        }
        job.changed.add(finish)
        signal?.addEventListener('abort', finish, { once: true })
      })
    }
    const missed = Math.max(0, job.dropped - job.read)
    const text = job.output.slice(Math.max(0, job.read - job.dropped))
    job.read = job.dropped + job.output.length
    return { command: view(job), text, missed } satisfies CommandOutput
  }

  /**
   * The end of what the command printed, for the user to look at; the model's own reading
   * is left where it was. `null` when the thread has no such command.
   */
  peek(
    threadId: string,
    id: string,
    chars = PEEK_CHARS,
  ): { command: BackgroundCommand; tail: string } | null {
    const job = this.own(threadId, id)
    if (!job) return null
    return { command: view(job), tail: screenText(job.output.slice(-chars * 2)).slice(-chars) }
  }

  /** Stop the command and wait a moment for it to end. `null` when there is no such command. */
  async stop(threadId: string, id: string): Promise<BackgroundCommand | null> {
    const job = this.own(threadId, id)
    if (!job) return null
    await this.end([job])
    return view(job)
  }

  /** Stop the thread's commands and forget them all, with what they printed. */
  async stopThread(threadId: string): Promise<void> {
    const jobs = [...this.jobs.values()].filter((job) => job.threadId === threadId)
    await this.end(jobs)
    for (const job of jobs) this.jobs.delete(job.id)
    if (jobs.length) this.onChange(threadId)
  }

  async stopAll(): Promise<void> {
    await this.end([...this.jobs.values()])
  }

  private async end(jobs: Job[]): Promise<void> {
    const running = jobs.filter((job) => !job.result)
    for (const job of running) job.controller.abort('stopped')
    let timer: ReturnType<typeof setTimeout> | undefined
    const late = new Promise<void>((done) => {
      timer = setTimeout(done, 5000)
    })
    await Promise.race([Promise.all(running.map((job) => job.ended)), late])
    clearTimeout(timer)
  }

  private own(threadId: string, id: string): Job | null {
    const job = this.jobs.get(id)
    return job && job.threadId === threadId ? job : null
  }

  private forgetOld(threadId: string): void {
    const finished = [...this.jobs.values()].filter(
      (job) => job.threadId === threadId && job.result,
    )
    for (const job of finished.slice(0, Math.max(0, finished.length - KEPT_FINISHED + 1))) {
      this.jobs.delete(job.id)
    }
  }
}

const view = (job: Job): BackgroundCommand => ({
  id: job.id,
  threadId: job.threadId,
  command: job.command,
  startedAt: job.startedAt,
  result: job.result,
})
