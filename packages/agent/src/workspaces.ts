import { DomainError, type Ariadne } from '@ariadne/core'
import type {
  BranchCapability,
  Capabilities,
  RepoCapability,
  SourceView,
  TrackerCapability,
  Workspace,
} from '@ariadne/plugin-sdk'
import type { Locale } from '@ariadne/i18n'
import type { ProjectSource } from '@ariadne/core'
import type { ContextProvider } from '@ariadne/plugin-sdk'
import { withHosting } from './hosting'
import type { PluginHost } from './plugins'

/** How long a coordinator's turn waits on its source to know whether it can look at it. */
const VIEWABLE_WAIT_MS = 2000

/** Secret settings of a source's hosting are kept under this prefix. */
export const HOSTING_SECRET = 'hosting.'

/**
 * Connect a project's source, and its hosting when it has one. `secrets` are the user's
 * secret settings of both, the hosting's under `HOSTING_SECRET`.
 */
export async function connectSource(
  contexts: Map<string, ContextProvider<never>>,
  source: ProjectSource,
  secrets: Record<string, unknown>,
): Promise<Partial<Capabilities>> {
  const own: Record<string, unknown> = {}
  const hostingSecrets: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(secrets)) {
    if (key.startsWith(HOSTING_SECRET)) hostingSecrets[key.slice(HOSTING_SECRET.length)] = value
    else own[key] = value
  }
  const provider = contexts.get(source.kind)
  if (!provider) throw new DomainError('error.notFound', { what: source.kind })
  const hostingProvider = source.hosting ? contexts.get(source.hosting.kind) : null
  if (source.hosting && !hostingProvider?.hosting) {
    throw new DomainError('error.notFound', { what: source.hosting.kind })
  }
  if (source.hosting && !provider.remote) {
    throw new DomainError('error.workspace', { detail: 'only a remote source takes a hosting' })
  }
  try {
    const capabilities = await provider.connect({ ...source.settings, ...own } as never)
    if (!source.hosting || !hostingProvider) return capabilities
    const hosting = await hostingProvider.connect({
      ...source.hosting.settings,
      ...hostingSecrets,
    } as never)
    return await withHosting(capabilities, hosting)
  } catch (error) {
    if (error instanceof DomainError) throw error
    throw new DomainError('error.workspace', {
      detail: error instanceof Error ? error.message : String(error),
    })
  }
}

/**
 * A source's settings in one line, as the project header shows them: the provider's
 * `summary` with `{setting}` filled in, or else its first text setting (the path, the
 * repository). Secret settings are never stored with the source, so none can show.
 */
export function sourceSummary(
  template: string | undefined,
  settings: Record<string, unknown>,
): string | null {
  if (template) {
    return template.replace(/\{(\w+)\}/g, (_, key: string) => String(settings[key] ?? '')).trim()
  }
  const value = Object.values(settings).find((v) => typeof v === 'string' && v)
  return typeof value === 'string' ? value : null
}

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
  private readonly queues = new Map<string, Promise<void>>()

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

  /**
   * Whether the project's workspaces are on another machine; a git checkout there pushes
   * nowhere from threads' commands.
   */
  isRemote(projectId: string): boolean {
    const source = this.app.getProject(projectId).source
    return !!source && !!this.host.contexts.get(source.kind)?.remote
  }

  /** Whether the project's source has issues to read. */
  hasTracker(projectId: string): boolean {
    return this.provides(projectId, 'tracker')
  }

  /**
   * The project's source in one line, as the user sees it in the project header: the kind
   * of source and its summary (e.g. `本地目录 D:\work\app`), the hosting first when it
   * has one. `null` without a source.
   */
  describe(projectId: string, locale: Locale): string | null {
    const source = this.app.getProject(projectId).source
    if (!source) return null
    const line = (kind: string, settings: Record<string, unknown>) => {
      const provider = this.host.contexts.get(kind)
      const title = provider ? (provider.title[locale] ?? provider.title['zh-CN']) : kind
      const summary = sourceSummary(provider?.summary, settings)
      return summary ? `${title} ${summary}` : title
    }
    return [
      ...(source.hosting ? [line(source.hosting.kind, source.hosting.settings)] : []),
      line(source.kind, source.settings),
    ].join(' · ')
  }

  /**
   * Whether the coordinator can look at the project's source: its provider offers a view.
   * A source that fails to connect, or does not answer within `waitMs`, counts as one: the
   * turn does not wait on it, and a look says why it failed.
   */
  async viewable(projectId: string, waitMs = VIEWABLE_WAIT_MS): Promise<boolean> {
    if (!this.available(projectId)) return false
    let timer: ReturnType<typeof setTimeout> | undefined
    const late = new Promise<boolean>((done) => {
      timer = setTimeout(() => done(true), waitMs)
    })
    try {
      const connected = this.connect(projectId, this.sourceKey(projectId)).then(
        (capabilities) => !!capabilities.workspace?.view,
        () => true,
      )
      return await Promise.race([connected, late])
    } catch {
      return true
    } finally {
      clearTimeout(timer)
    }
  }

  /** The project's source as it is, read-only; throws a `DomainError` the model can read. */
  async view(projectId: string): Promise<SourceView> {
    const capabilities = await this.connect(projectId, this.sourceKey(projectId))
    const view = capabilities.workspace?.view
    if (!view) {
      throw new DomainError('error.workspace', {
        detail: 'this source cannot be read from here; a thread can read it in its workspace',
      })
    }
    try {
      return await view.call(capabilities.workspace)
    } catch (error) {
      if (error instanceof DomainError) throw error
      throw new DomainError('error.workspace', {
        detail: error instanceof Error ? error.message : String(error),
      })
    }
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
    // With a hosting, the repository and the issues come from it, the workspaces from the source.
    const from = source.hosting && capability !== 'workspace' ? source.hosting.kind : source.kind
    return !!this.host.contexts.get(from)?.provides.includes(capability)
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
    // Not while its worktree is being removed: it would be used, then deleted under it.
    return this.serial(threadId, () => this.openFor(projectId, threadId, key))
  }

  private async openFor(projectId: string, threadId: string, key: string): Promise<Workspace> {
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
   * again. One with changes not committed stays (`dirty`), and so does one whose thread has
   * a turn running (`busy`): it may be using it.
   */
  async removeWorktree(
    projectId: string,
    threadId: string,
    reason: 'merged' | 'user',
  ): Promise<'removed' | 'dirty' | 'busy'> {
    const known = this.app.listThreads(projectId).some((thread) => thread.id === threadId)
    return this.serial(threadId, async () => {
      const branches = await this.branches(projectId)
      if (!branches) return 'dirty'
      if (known && this.app.getConversation(threadId).turn) return 'busy'
      const open = this.open.get(threadId)
      this.open.delete(threadId)
      if (open) await open.workspace.then((workspace) => workspace.dispose()).catch(() => {})
      const removed = await branches.remove(threadId)
      if (!removed) return 'dirty'
      if (known) this.app.recordWorktreeRemoved(threadId, reason)
      return 'removed'
    })
  }

  /**
   * Remove the worktrees of every thread of a project about to be deleted, changes not
   * committed and all; their branches stay in the repository. The project's turns must
   * have ended. Throws when the source cannot be reached or a worktree cannot be removed,
   * so nothing is deleted while worktrees would be left behind. Worktrees no source can
   * reach any more (it was cleared, changed, or its plugin removed) are left where they are.
   */
  async discardProject(projectId: string): Promise<void> {
    let branches: Promise<BranchCapability | null> | null = null
    for (const { id } of this.app.listThreads(projectId)) {
      await this.serial(id, async () => {
        const open = this.open.get(id)
        this.open.delete(id)
        if (open) await open.workspace.then((workspace) => workspace.dispose()).catch(() => {})
        const workspace = this.app.getThread(id).workspace
        if (!workspace || workspace.removed) return
        branches ??= this.branches(projectId)
        const capability = await branches
        if (capability && !(await capability.remove(id, { discard: true }))) {
          throw new DomainError('error.workspace', { detail: workspace.label })
        }
      })
    }
    this.sources.delete(projectId)
  }

  /** Run `task` after the thread's earlier opening or removal has finished. */
  private serial<T>(threadId: string, task: () => Promise<T>): Promise<T> {
    const before = this.queues.get(threadId) ?? Promise.resolve()
    const result = before.then(task, task)
    const done = result.then(
      () => {},
      () => {},
    )
    this.queues.set(threadId, done)
    void done.then(() => {
      if (this.queues.get(threadId) === done) this.queues.delete(threadId)
    })
    return result
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
    const [source, secrets] = JSON.parse(key) as [ProjectSource, Record<string, unknown>]
    const capabilities = connectSource(this.host.contexts, source, secrets)
    this.sources.set(projectId, { key, capabilities })
    capabilities.catch(() => {
      if (this.sources.get(projectId)?.capabilities === capabilities) this.sources.delete(projectId)
    })
    return capabilities
  }
}
