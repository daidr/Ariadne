import type { AnyStoredEvent, Ariadne, ThreadView } from '@ariadne/core'
import type { WorkspaceManager } from './workspaces'

/** Stages in which a thread has finished with its worktree for now. */
const FINISHED = new Set(['review', 'done'])

/**
 * Removes the worktrees of threads whose work has landed: the user merged the branch (in
 * Ariadne or by hand), or its pull request was merged or closed, and the thread is not
 * working. Branches stay, and a thread that works again gets its worktree back. A worktree
 * with changes not committed is left alone.
 *
 * Checks follow the events that can settle it, and a slow sweep catches merges made
 * outside Ariadne.
 */
export class WorktreeJanitor {
  private timer: ReturnType<typeof setInterval> | null = null
  private unsubscribe: (() => void) | null = null
  private queue: Promise<void> = Promise.resolve()
  private readonly waiting = new Set<string>()

  constructor(
    private readonly app: Ariadne,
    private readonly workspaces: WorkspaceManager,
    private readonly options: {
      intervalMs?: number
      /** How long after an event to look, so the thread's stage has settled. */
      delayMs?: number
      onError?: (error: unknown) => void
    } = {},
  ) {}

  start(): void {
    this.unsubscribe ??= this.app.subscribe((events) => {
      for (const event of events) this.onEvent(event)
    })
    this.timer ??= setInterval(() => void this.sweep(), this.options.intervalMs ?? 600_000)
    void this.sweep()
  }

  async stop(): Promise<void> {
    this.unsubscribe?.()
    this.unsubscribe = null
    if (this.timer) clearInterval(this.timer)
    this.timer = null
    await this.queue
  }

  /** Look at every project's threads once. */
  sweep(): Promise<void> {
    for (const project of this.app.listProjects()) {
      for (const thread of this.app.listThreads(project.id)) {
        if (candidate(thread)) this.enqueue(thread.id)
      }
    }
    return this.queue
  }

  /** Look at one thread now, after whatever is queued. */
  check(threadId: string): Promise<void> {
    this.enqueue(threadId)
    return this.queue
  }

  private onEvent(event: AnyStoredEvent) {
    const threadId =
      event.type === 'branch.merged' ||
      event.type === 'pull_request.closed' ||
      event.type === 'thread.reported'
        ? event.data.threadId
        : event.type === 'turn.finished'
          ? event.data.conversationId
          : null
    if (!threadId || this.waiting.has(threadId)) return
    this.waiting.add(threadId)
    setTimeout(() => {
      this.waiting.delete(threadId)
      this.enqueue(threadId)
    }, this.options.delayMs ?? 2000)
  }

  private enqueue(threadId: string) {
    this.queue = this.queue
      .then(() => this.tidy(threadId))
      .catch(this.options.onError ?? ((error) => console.error('清理 worktree 失败', error)))
  }

  private async tidy(threadId: string): Promise<void> {
    let thread: ThreadView
    try {
      thread = this.app.getThread(threadId)
    } catch {
      return
    }
    if (!candidate(thread)) return
    if (!(await this.landed(thread))) return
    // It may have been woken while the branch was being checked.
    if (!candidate(this.app.getThread(threadId))) return
    await this.workspaces.removeWorktree(thread.projectId, threadId, 'merged')
  }

  /** Whether the thread's work is where it was going. */
  private async landed(thread: ThreadView): Promise<boolean> {
    if (thread.pullRequest) return thread.pullRequest.state !== 'open'
    const base = thread.workspace?.base
    if (!base) return false
    const branches = await this.workspaces.branches(thread.projectId)
    if (!branches) return false
    // A squashed branch never becomes part of the base: it landed if nothing came after.
    if (thread.merged) {
      const changes = await branches.changes(thread.id, thread.merged.base)
      if (changes?.head === thread.merged.head) return true
    }
    return branches.merged(thread.id, base)
  }
}

function candidate(thread: ThreadView): boolean {
  return (
    !!thread.workspace?.branch &&
    !thread.workspace.removed &&
    FINISHED.has(thread.stage) &&
    (!!thread.pullRequest || !!thread.workspace.base)
  )
}
