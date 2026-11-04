import { DomainError, type Ariadne } from '@ariadne/core'
import type {
  BranchCapability,
  Capabilities,
  RepoCapability,
  TrackerCapability,
  Workspace,
} from '@ariadne/plugin-sdk'
import type { PluginHost } from './plugins'

/**
 * Opens each thread's workspace from its project's context source, once, and keeps it
 * for later turns. A source that changes gets connected again.
 */
export class WorkspaceManager {
  private readonly sources = new Map<
    string,
    { key: string; capabilities: Promise<Partial<Capabilities>> }
  >()
  private readonly open = new Map<string, { key: string; workspace: Promise<Workspace> }>()

  constructor(
    private readonly app: Ariadne,
    private readonly host: PluginHost,
    /** Secret settings of the project's source, kept in the user's config. */
    private readonly secrets: (projectId: string) => Record<string, unknown> = () => ({}),
  ) {}

  /** Whether the project's threads can get a workspace at all. */
  available(projectId: string): boolean {
    return this.provides(projectId, 'workspace')
  }

  /** Whether the project's source is a hosted repository, with pull requests and checks. */
  hasRepo(projectId: string): boolean {
    return this.provides(projectId, 'repo')
  }

  /** Whether the project's source has issues to read. */
  hasTracker(projectId: string): boolean {
    return this.provides(projectId, 'tracker')
  }

  /** The project's issue tracker; throws a `DomainError` the model can read. */
  async tracker(projectId: string): Promise<TrackerCapability> {
    const capabilities = await this.connect(projectId, this.sourceKey(projectId))
    if (!capabilities.tracker) throw new DomainError('error.noTracker')
    return capabilities.tracker
  }

  private provides(projectId: string, capability: keyof Capabilities): boolean {
    const source = this.app.getProject(projectId).source
    if (!source) return false
    return !!this.host.contexts.get(source.kind)?.provides.includes(capability)
  }

  /** The project's repository; throws a `DomainError` the model can read. */
  async repo(projectId: string): Promise<RepoCapability> {
    const capabilities = await this.connect(projectId, this.sourceKey(projectId))
    if (!capabilities.repo) throw new DomainError('error.noRepo')
    return capabilities.repo
  }

  /** The thread's workspace; throws a `DomainError` the model can read. */
  async forThread(projectId: string, threadId: string): Promise<Workspace> {
    const key = this.sourceKey(projectId)
    const cached = this.open.get(threadId)
    if (cached?.key === key) return cached.workspace
    // A workspace opened before keeps the base it started from.
    const known = this.app.getThread(threadId).workspace
    const workspace = this.connect(projectId, key).then((capabilities) => {
      if (!capabilities.workspace) throw new DomainError('error.noSource')
      return capabilities.workspace.open({ threadId, ...(known ? { base: known.base } : {}) })
    })
    this.open.set(threadId, { key, workspace })
    try {
      const ready = await workspace
      this.app.recordWorkspace(threadId, {
        label: ready.label,
        branch: ready.branch,
        base: ready.base,
      })
      return ready
    } catch (error) {
      this.open.delete(threadId)
      if (error instanceof DomainError) throw error
      throw new DomainError('error.workspace', {
        detail: error instanceof Error ? error.message : String(error),
      })
    }
  }

  /** The project's threads' branches and worktrees; `null` when they are not git worktrees. */
  async branches(projectId: string): Promise<BranchCapability | null> {
    if (!this.available(projectId)) return null
    const capabilities = await this.connect(projectId, this.sourceKey(projectId))
    return capabilities.workspace?.branches ?? null
  }

  /**
   * Remove the thread's worktree, keeping its branch, and record it. The next use opens it
   * again. One with changes not committed stays: `false`.
   */
  async removeWorktree(
    projectId: string,
    threadId: string,
    reason: 'merged' | 'user',
  ): Promise<boolean> {
    const branches = await this.branches(projectId)
    if (!branches) return false
    const open = this.open.get(threadId)
    this.open.delete(threadId)
    if (open) await open.workspace.then((workspace) => workspace.dispose()).catch(() => {})
    const removed = await branches.remove(threadId)
    const known = this.app.listThreads(projectId).some((thread) => thread.id === threadId)
    if (removed && known) this.app.recordWorktreeRemoved(threadId, reason)
    return removed
  }

  async dispose(): Promise<void> {
    const all = [...this.open.values()]
    this.open.clear()
    await Promise.allSettled(all.map(async (entry) => (await entry.workspace).dispose()))
  }

  private sourceKey(projectId: string): string {
    const source = this.app.getProject(projectId).source
    if (!source) throw new DomainError('error.noSource')
    // The secrets are part of the key, so a new token connects again.
    return JSON.stringify([source, this.secrets(projectId)])
  }

  /** Connect the project's source once per configuration; a failed connect is retried. */
  private connect(projectId: string, key: string): Promise<Partial<Capabilities>> {
    const known = this.sources.get(projectId)
    if (known?.key === key) return known.capabilities
    const [source, secrets] = JSON.parse(key) as [
      { kind: string; settings: Record<string, unknown> },
      Record<string, unknown>,
    ]
    const capabilities = (async () => {
      const provider = this.host.contexts.get(source.kind)
      if (!provider) throw new DomainError('error.notFound', { what: source.kind })
      try {
        return await provider.connect({ ...source.settings, ...secrets } as never)
      } catch (error) {
        throw new DomainError('error.workspace', {
          detail: error instanceof Error ? error.message : String(error),
        })
      }
    })()
    this.sources.set(projectId, { key, capabilities })
    capabilities.catch(() => {
      if (this.sources.get(projectId)?.capabilities === capabilities) this.sources.delete(projectId)
    })
    return capabilities
  }
}
