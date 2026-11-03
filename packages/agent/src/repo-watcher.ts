import type { Ariadne } from '@ariadne/core'
import { syncThreadPullRequest } from './repo-tools'
import type { WorkspaceManager } from './workspaces'

/**
 * Polls the open pull requests of threads, so a failed check wakes the thread that owns it
 * and a merge reaches the coordinator. Only threads that opened a pull request are polled.
 */
export class RepoWatcher {
  private timer: ReturnType<typeof setInterval> | null = null
  private running: Promise<void> | null = null

  constructor(
    private readonly app: Ariadne,
    private readonly workspaces: WorkspaceManager,
    private readonly intervalMs = 60_000,
    private readonly onError: (error: unknown) => void = (error) =>
      console.error('仓库状态同步失败', error),
  ) {}

  start(): void {
    this.timer ??= setInterval(() => void this.poll(), this.intervalMs)
  }

  async stop(): Promise<void> {
    if (this.timer) clearInterval(this.timer)
    this.timer = null
    await this.running
  }

  /** One pass over every project; overlapping passes are skipped. */
  poll(): Promise<void> {
    this.running ??= this.pass().finally(() => {
      this.running = null
    })
    return this.running
  }

  private async pass(): Promise<void> {
    for (const project of this.app.listProjects()) {
      if (!this.workspaces.hasRepo(project.id)) continue
      const threads = this.app
        .listThreads(project.id)
        .filter((thread) => thread.pullRequest?.state === 'open')
      if (!threads.length) continue
      try {
        const repo = await this.workspaces.repo(project.id)
        for (const thread of threads) {
          await syncThreadPullRequest(this.app, repo, thread.id).catch(this.onError)
        }
      } catch (error) {
        this.onError(error)
      }
    }
  }
}
