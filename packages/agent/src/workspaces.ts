import { DomainError, type Ariadne } from '@ariadne/core'
import type { Workspace, WorkspaceCapability } from '@ariadne/plugin-sdk'
import type { PluginHost } from './plugins'

/**
 * Opens each thread's workspace from its project's context source, once, and keeps it
 * for later turns. A source that changes gets connected again.
 */
export class WorkspaceManager {
  private readonly sources = new Map<string, { key: string; capability: WorkspaceCapability }>()
  private readonly open = new Map<string, { key: string; workspace: Promise<Workspace> }>()

  constructor(
    private readonly app: Ariadne,
    private readonly host: PluginHost,
  ) {}

  /** Whether the project's threads can get a workspace at all. */
  available(projectId: string): boolean {
    const source = this.app.getProject(projectId).source
    return !!source && this.host.contexts.has(source.kind)
  }

  /** The thread's workspace; throws a `DomainError` the model can read. */
  async forThread(projectId: string, threadId: string): Promise<Workspace> {
    const key = this.sourceKey(projectId)
    const cached = this.open.get(threadId)
    if (cached?.key === key) return cached.workspace
    const workspace = this.connect(projectId, key).then((capability) =>
      capability.open({ threadId }),
    )
    this.open.set(threadId, { key, workspace })
    try {
      const ready = await workspace
      this.app.recordWorkspace(threadId, { label: ready.label, branch: ready.branch })
      return ready
    } catch (error) {
      this.open.delete(threadId)
      if (error instanceof DomainError) throw error
      throw new DomainError('error.workspace', {
        detail: error instanceof Error ? error.message : String(error),
      })
    }
  }

  async dispose(): Promise<void> {
    const all = [...this.open.values()]
    this.open.clear()
    await Promise.allSettled(all.map(async (entry) => (await entry.workspace).dispose()))
  }

  private sourceKey(projectId: string): string {
    const source = this.app.getProject(projectId).source
    if (!source) throw new DomainError('error.noSource')
    return JSON.stringify(source)
  }

  private async connect(projectId: string, key: string): Promise<WorkspaceCapability> {
    const known = this.sources.get(projectId)
    if (known?.key === key) return known.capability
    const source = JSON.parse(key) as { kind: string; settings: Record<string, unknown> }
    const provider = this.host.contexts.get(source.kind)
    if (!provider) throw new DomainError('error.notFound', { what: source.kind })
    const capabilities = await provider.connect(source.settings as never)
    if (!capabilities.workspace) {
      throw new DomainError('error.workspace', { detail: `${source.kind} has no workspace` })
    }
    this.sources.set(projectId, { key, capability: capabilities.workspace })
    return capabilities.workspace
  }
}
