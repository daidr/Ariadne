import {
  DomainError,
  setupCommand,
  SHELLS,
  threadBranch,
  type Ariadne,
  type ShellChoice,
  type WorktreeRemoval,
} from '@ariadne/core'
import { defaultShell } from '@ariadne/plugin-sdk'
import type {
  BranchCapability,
  Capabilities,
  RepoCapability,
  SourceView,
  TrackerCapability,
  Workspace,
} from '@ariadne/plugin-sdk'
import type { Locale } from '@ariadne/i18n'
import type { ProjectReference, ProjectSource, ThreadReference } from '@ariadne/core'
import type { ContextProvider } from '@ariadne/plugin-sdk'
import { withHosting } from './hosting'
import type { PluginHost } from './plugins'
import { commandVerdict, type CommandRule } from './limits'
import { BackgroundCommands } from './background'
import { referenceRefusal } from './reference-guard'
import type { ReferenceKind } from './reference-tools'

/** How long setting up a new workspace may take, unless the user's limits say. */
const SETUP_TIMEOUT_MS = 600_000

export interface WorkspaceManagerOptions {
  /** The environment variables the user gave the project's commands; they may be secret. */
  env?: (projectId: string) => Record<string, string>
  /** The user's rules for thread commands: a setup a thread noted keeps to them too. */
  commandRules?: (projectId: string) => readonly CommandRule[]
  /** How long a new workspace's setup may run. */
  setupTimeoutMs?: (projectId: string) => number
}

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
    if (capabilities.workspace?.viewOnly) {
      throw new DomainError('error.workspace', {
        detail: 'a single file can only be a reference source; threads work in a directory',
      })
    }
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

/** Secret settings of a reference source are kept under this prefix and its id. */
export const REFERENCE_SECRET = 'reference.'

export const referenceSecretPrefix = (referenceId: string) => `${REFERENCE_SECRET}${referenceId}.`

/**
 * The secret settings a reference source connects with: its own, and for those it has
 * none of, the main source's (or its hosting's) when that is of the same kind and sends
 * them to the same place, so one token reads two repositories of the same host without
 * being typed twice, and never reaches a host it was not given for.
 */
export function referenceSecrets(
  contexts: Map<string, ContextProvider<never>>,
  source: ProjectSource | null,
  reference: { id?: string; kind: string; settings: Record<string, unknown> },
  secrets: Record<string, unknown>,
): Record<string, unknown> {
  const picked: Record<string, unknown> = {}
  const take = (prefix: string, strict: boolean) => {
    for (const [key, value] of Object.entries(secrets)) {
      if (!key.startsWith(prefix)) continue
      const name = key.slice(prefix.length)
      // The main source's own secrets are the keys under no prefix at all.
      if (strict || !name.includes('.')) picked[name] = value
    }
  }
  const lends = (other: { kind: string; settings: Record<string, unknown> } | null | undefined) => {
    if (other?.kind !== reference.kind) return false
    const goes = contexts.get(reference.kind)?.secretsFor
    if (!goes) return true
    try {
      return goes(reference.settings as never) === goes(other.settings as never)
    } catch {
      return false
    }
  }
  if (lends(source?.hosting)) take(HOSTING_SECRET, true)
  if (lends(source)) take('', false)
  if (reference.id) take(referenceSecretPrefix(reference.id), true)
  return picked
}

/** What went wrong with a source, as a `DomainError` the user or the model can read. */
const unreadable = (error: unknown) =>
  error instanceof DomainError
    ? error
    : new DomainError('error.workspace', {
        detail: error instanceof Error ? error.message : String(error),
      })

/**
 * Connect a source that is only read. What comes back hands out its view, fresh each time
 * (a hosted repository fetches when its view is asked for). Throws a `DomainError` the
 * user or the model can read when it cannot be reached or offers nothing to look at.
 */
export async function connectReference(
  contexts: Map<string, ContextProvider<never>>,
  reference: { kind: string; settings: Record<string, unknown> },
  secrets: Record<string, unknown>,
): Promise<() => Promise<SourceView>> {
  const provider = contexts.get(reference.kind)
  if (!provider) throw new DomainError('error.notFound', { what: reference.kind })
  let workspace: Partial<Capabilities>['workspace']
  try {
    workspace = (await provider.connect({ ...reference.settings, ...secrets } as never)).workspace
  } catch (error) {
    throw unreadable(error)
  }
  const view = workspace?.view
  if (!workspace || !view) {
    throw new DomainError('error.workspace', {
      detail: 'this kind of source cannot be read without working in it',
    })
  }
  return () => view.call(workspace).catch((error: unknown) => Promise.reject(unreadable(error)))
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

/** Told to the thread whose new workspace is in a project with no check after edits yet. */
const CHECK_HINT =
  '[Ariadne: this project has no check after edits yet. Once you know how the repository checks its code, note one with note_check.]'

/** Told to the thread whose new workspace is in a project with no setup for new workspaces yet. */
const SETUP_HINT =
  "[Ariadne: nothing was installed in your new workspace: this project has no setup for new workspaces yet. When the work needs its dependencies, install them with note_setup, the way the repository says to: later threads' new workspaces then run the same first.]"

const heldNote = (command: string) =>
  `[Ariadne: your workspace is new, and its setup did not run: the user's command rules do not let \`${command.split('\n')[0]}\` run unasked. When the work needs it, run it with run_command, which asks the user where the rules say so.]`

const startedNote = (id: string, own: boolean) =>
  `[Ariadne: your workspace is new, so ${own ? "the project's setup script, as the user set it," : 'the setup noted for this project'} runs in it now, in the background as ${id}. run_command waits for it to end before running anything; read files and edit meanwhile, or read_command_output ${id} to look at it.]`

const endedNote = (id: string, result: { exitCode: number; stopped: boolean }) =>
  `[Ariadne: ${id}, the setup of your new workspace, ${result.stopped ? 'was stopped before it finished' : `failed (exit code ${result.exitCode})`}; what the work needs may be missing. It does not run again by itself: read_command_output ${id} shows what it printed, and run_command what the work needs.]`

/**
 * Opens each thread's workspace from its project's context source, once, and keeps it
 * for later turns. A source that changes gets connected again.
 */
/** Tells a thread at work again what still runs from before, so it does not start it twice. */
export function keptNote(commands: { id: string; command: string; url: string | null }[]): string {
  const list = commands
    .map((c) => `${c.id}${c.url ? ` at ${c.url}` : ''}: ${c.command.slice(0, 120)}`)
    .join('\n')
  return `Still running in the background from before your last report, kept because the user wanted to try them:\n${list}\nUse them as they are (read_command_output), or stop_command one that needs a restart; do not start a second copy.`
}

export class WorkspaceManager {
  private readonly sources = new Map<
    string,
    { key: string; capabilities: Promise<Partial<Capabilities>> }
  >()
  /** Reference sources as connected, by project and reference id. */
  private readonly referenceViews = new Map<
    string,
    { key: string; view: Promise<() => Promise<SourceView>> }
  >()
  private readonly open = new Map<string, { key: string; workspace: Promise<Workspace> }>()
  private readonly queues = new Map<string, Promise<void>>()
  /** What setting up a thread's new workspace did, until its model is told. */
  private readonly setups = new Map<string, string>()
  /** Threads whose new workspace was opened, until they are told what the project lacks. */
  private readonly hinted = new Set<string>()
  /** Threads whose new workspace got no setup, as the project has none yet. */
  private readonly unset = new Set<string>()
  /** The background command setting up each thread's new workspace, until it is waited for. */
  private readonly setupJobs = new Map<string, string>()
  /** Commands threads left running in the background; the user sees them in the status line. */
  readonly background = new BackgroundCommands((threadId) => {
    let projectId: string
    try {
      projectId = this.app.getConversation(threadId).projectId
    } catch {
      return
    }
    const commands = this.background.list(threadId).map((command) => ({
      id: command.id,
      command: command.command,
      startedAt: command.startedAt,
      url: command.url,
      result: command.result,
    }))
    this.app.background.set({ projectId, conversationId: threadId, commands })
  })

  constructor(
    private readonly app: Ariadne,
    private readonly host: PluginHost,
    /** Secret settings of the project's source, kept in the user's config. */
    private readonly secrets: (projectId: string) => Record<string, unknown> = () => ({}),
    private readonly options: WorkspaceManagerOptions = {},
  ) {}

  /** The environment variables of the project's thread commands. */
  env(projectId: string): Record<string, string> {
    return this.options.env?.(projectId) ?? {}
  }

  /**
   * What the thread should hear with its next tool result, once: what the setup script did
   * in the workspace opened just now, and what it left running when it last reported.
   */
  takeSetup(threadId: string): string | null {
    const notes = [this.setups.get(threadId) ?? '', this.setupEnded(threadId) ?? '']
    this.setups.delete(threadId)
    // Asked only now: the call that opened the workspace may have noted the check itself.
    if (this.hinted.delete(threadId) && this.checkWanted(threadId)) notes.push(CHECK_HINT)
    if (this.unset.delete(threadId) && this.setupWanted(threadId)) notes.push(SETUP_HINT)
    // Back at work after a report: what it left serving is still there to use.
    const kept = this.background.takeKept(threadId)
    if (kept.length) notes.push(keptNote(kept))
    return notes.filter(Boolean).join('\n\n') || null
  }

  private checkWanted(threadId: string): boolean {
    try {
      const { projectId } = this.app.getThread(threadId)
      return this.app.canNoteCheck(threadId) && !this.app.getProject(projectId).detected.check
    } catch {
      return false
    }
  }

  private setupWanted(threadId: string): boolean {
    try {
      const { projectId } = this.app.getThread(threadId)
      return this.app.canNoteSetup(threadId) && !this.app.getProject(projectId).detected.setup
    } catch {
      return false
    }
  }

  /** The background command still setting up the thread's new workspace; `null` when none runs. */
  setupRunning(threadId: string): string | null {
    const id = this.setupJobs.get(threadId)
    if (!id) return null
    const job = this.background.list(threadId).find((command) => command.id === id)
    return job && !job.result ? id : null
  }

  /**
   * How the setup of the thread's new workspace ended, once, when it ended badly and no
   * command has waited on it yet; `null` while it runs or when it went well.
   */
  private setupEnded(threadId: string): string | null {
    const id = this.setupJobs.get(threadId)
    if (!id) return null
    const job = this.background.list(threadId).find((command) => command.id === id)
    if (job && !job.result) return null
    this.setupJobs.delete(threadId)
    if (!job?.result || (job.result.exitCode === 0 && !job.result.stopped)) return null
    return endedNote(id, job.result)
  }

  /**
   * Wait while the setup of the thread's new workspace runs, unless `signal` aborts first;
   * how it ended when the thread should hear it (it failed, or was stopped), once.
   */
  async setupDone(threadId: string, signal: AbortSignal): Promise<string | null> {
    const id = this.setupJobs.get(threadId)
    if (!id) return null
    const job = await this.background.wait(threadId, id, signal)
    if (!job?.result) {
      // Forgotten (the thread was stopped, its worktree went): there is nothing to wait for.
      if (!job) this.setupJobs.delete(threadId)
      return null
    }
    this.setupJobs.delete(threadId)
    return job.result.exitCode === 0 && !job.result.stopped ? null : endedNote(id, job.result)
  }

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

  /**
   * The shell the project's thread commands run in: its name, as the model is told, and
   * the command line to hand them to; `argv` is `null` for the machine's own. Another
   * machine (SSH, a daemon) is POSIX, and its own is bash.
   */
  shell(projectId: string): { name: string; argv: readonly string[] | null } {
    const chosen: ShellChoice = this.app.getProject(projectId).settings.workspace.shell
    if (chosen !== 'auto') return { name: chosen, argv: SHELLS[chosen] }
    return { name: this.isRemote(projectId) ? 'bash' : defaultShell().name, argv: null }
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
   * The sources the project only reads, each with the name conversations call it by and
   * where it is in one line, like `describe`.
   */
  references(
    projectId: string,
    locale: Locale,
    /** A thread also gets the sources it alone reads, marked as its own. */
    threadId?: string,
  ): { name: string; line: string; own?: true }[] {
    const line = (reference: ProjectReference) => {
      const provider = this.host.contexts.get(reference.kind)
      const title = provider ? (provider.title[locale] ?? provider.title['zh-CN']) : reference.kind
      const summary = sourceSummary(provider?.summary, reference.settings)
      return { name: reference.name, line: summary ? `${title} ${summary}` : title }
    }
    return [
      ...this.app.getProject(projectId).references.map(line),
      ...this.threadReferences(threadId).map((reference) => ({
        ...line(reference),
        own: true as const,
      })),
    ]
  }

  /** The sources a thread alone reads; none for the coordinator or a thread that is gone. */
  private threadReferences(threadId: string | undefined): ThreadReference[] {
    if (!threadId) return []
    try {
      return this.app.getThread(threadId).references
    } catch {
      return []
    }
  }

  /**
   * Why a conversation may not ask to read this source, or `null`: see `referenceRefusal`.
   */
  referenceRefusal(reference: { kind: string; settings: Record<string, unknown> }): string | null {
    const provider = this.host.contexts.get(reference.kind)
    if (!provider) return null
    return referenceRefusal(provider, reference.settings, this.host.kept)
  }

  /**
   * A reference source by its name, read-only; throws a `DomainError` the model can read.
   * It is connected once per configuration, and again after a failure.
   */
  async referenceView(projectId: string, name: string, threadId?: string): Promise<SourceView> {
    const project = this.app.getProject(projectId)
    // A thread's own come first: their names were told apart from the project's when they
    // were allowed, and a reference the project took since must not take their place.
    const reference =
      this.threadReferences(threadId).find((known) => known.name === name) ??
      project.references.find((known) => known.name === name)
    if (!reference) throw new DomainError('error.noReference', { name })
    const secrets = referenceSecrets(
      this.host.contexts,
      project.source,
      reference,
      this.secrets(projectId),
    )
    const key = JSON.stringify([reference.kind, reference.settings, secrets])
    const id = `${projectId}:${reference.id}`
    let known = this.referenceViews.get(id)
    if (known?.key !== key) {
      const connected = connectReference(this.host.contexts, reference, secrets)
      known = { key, view: connected }
      this.referenceViews.set(id, known)
      connected.catch(() => {
        if (this.referenceViews.get(id)?.view === connected) this.referenceViews.delete(id)
      })
    }
    try {
      return await (
        await known.view
      )()
    } catch (error) {
      const detail =
        error instanceof DomainError
          ? String(error.params?.detail ?? error.params?.what ?? error.code)
          : String(error)
      throw new DomainError('error.referenceUnreadable', { name, detail })
    }
  }

  /**
   * Whether a source to be added as a reference can be read, before it is saved: connects
   * it (with `secrets`, or else the main source's when it is of the same kind) and lists
   * its root. Returns where it is in one line, which its name is made from.
   */
  async checkReference(
    projectId: string,
    reference: { kind: string; settings: Record<string, unknown> },
    secrets?: Record<string, unknown>,
  ): Promise<{ where: string }> {
    const project = this.app.getProject(projectId)
    const connected = await connectReference(this.host.contexts, reference, {
      ...referenceSecrets(this.host.contexts, project.source, reference, this.secrets(projectId)),
      ...secrets,
    })
    try {
      await (await connected()).list('.')
    } catch (error) {
      if (error instanceof DomainError) throw error
      throw new DomainError('error.workspace', {
        detail: error instanceof Error ? error.message : String(error),
      })
    }
    const provider = this.host.contexts.get(reference.kind)
    return { where: sourceSummary(provider?.summary, reference.settings) ?? reference.kind }
  }

  /** The kinds of source a reference can be, for the coordinator to pick among. */
  referenceKinds(locale: Locale): ReferenceKind[] {
    return [...this.host.contexts.values()].map((provider) => {
      // Secrets are the user's to type in the settings, never the model's.
      const settings = Object.entries(provider.settings.properties)
        .filter(([, field]) => !(field.type === 'string' && field.secret))
        .map(([key]) => key)
      return {
        kind: provider.kind,
        title: provider.title[locale] ?? provider.title['zh-CN'],
        settings,
        required: (provider.settings.required ?? []).filter((key) => settings.includes(key)),
      }
    })
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

  /**
   * The thread's workspace; throws a `DomainError` the model can read. A new one is handed
   * out at once, its setup running in the background (`setupDone` waits for it).
   */
  async forThread(projectId: string, threadId: string): Promise<Workspace> {
    const key = this.sourceKey(projectId)
    // Not while its worktree is being removed: it would be used, then deleted under it.
    return await this.serial(threadId, () => this.openFor(projectId, threadId, key))
  }

  /**
   * End what the thread left running: its background commands, and the setup script of
   * its new workspace.
   */
  async stopThread(threadId: string): Promise<void> {
    this.noteSetupGone(threadId)
    await this.background.stopThread(threadId)
  }

  /**
   * The thread's background commands are about to be forgotten: one still setting up its
   * new workspace is told of as stopped with the next tool result, and not run again.
   */
  private noteSetupGone(threadId: string): void {
    const id = this.setupJobs.get(threadId)
    if (!id) return
    this.setupJobs.delete(threadId)
    const job = this.background.list(threadId).find((command) => command.id === id)
    if (job && !job.result) {
      this.setups.set(threadId, endedNote(id, { exitCode: -1, stopped: true }))
    }
  }

  private async openFor(projectId: string, threadId: string, key: string): Promise<Workspace> {
    const cached = this.open.get(threadId)
    if (cached?.key === key) return cached.workspace
    // A workspace opened before keeps the base it started from, and its branch.
    const thread = this.app.getThread(threadId)
    const known = thread.workspace
    const project = this.app.getProject(projectId)
    const { workspace: setup } = project.settings
    const branch = known?.branch ?? threadBranch(setup.branchPrefix, threadId, thread.branchSlug)
    // A new directory has nothing installed yet: the project's setup starts in it.
    const fresh = !known || known.removed
    const workspace = this.connect(projectId, key).then(async (capabilities) => {
      if (!capabilities.workspace) throw new DomainError('error.noSource')
      const opened = await capabilities.workspace.open({
        threadId,
        branch,
        ...(known ? { base: known.base } : {}),
      })
      if (fresh) {
        const own = !setup.auto.setup
        // A noted one only in a directory of the thread's own: a shared one is the user's,
        // as it is. The user's own runs where they set it.
        const command = own || capabilities.workspace.isolated ? setupCommand(project) : ''
        const note = command ? this.startSetup(projectId, threadId, opened, command, own) : null
        if (note) this.setups.set(threadId, note)
        if (!own && capabilities.workspace.isolated && !project.detected.setup) {
          this.unset.add(threadId)
        }
        this.hinted.add(threadId)
      }
      return opened
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

  /**
   * Start the setup of a new workspace in the background, where the user sees it running
   * and can stop it; what the model is told of it.
   */
  private startSetup(
    projectId: string,
    threadId: string,
    workspace: Workspace,
    command: string,
    own: boolean,
  ): string {
    // The user's own script is theirs to run; one a thread noted keeps to their rules.
    const rules = this.options.commandRules?.(projectId) ?? []
    if (!own && commandVerdict(command, rules).action !== 'allow') return heldNote(command)
    const env = this.env(projectId)
    const { argv } = this.shell(projectId)
    const timeoutMs = this.options.setupTimeoutMs?.(projectId) ?? SETUP_TIMEOUT_MS
    const started = this.background.start(
      threadId,
      command,
      (options) =>
        workspace.exec(command, {
          ...(argv ? { shell: argv } : {}),
          ...(Object.keys(env).length ? { env } : {}),
          ...options,
          timeoutMs,
        }),
      { cwd: '.' },
    )
    this.setupJobs.set(threadId, started.id)
    return startedNote(started.id, own)
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
    reason: WorktreeRemoval,
  ): Promise<'removed' | 'dirty' | 'busy'> {
    const known = this.app.listThreads(projectId).some((thread) => thread.id === threadId)
    return this.serial(threadId, async () => {
      const branches = await this.branches(projectId)
      if (!branches) return 'dirty'
      if (known && this.app.getConversation(threadId).turn) return 'busy'
      // What the user asked to keep running to try is not ended to make room.
      if (reason === 'idle' && this.background.running(threadId)) return 'busy'
      this.noteSetupGone(threadId)
      await this.background.stopThread(threadId)
      const open = this.open.get(threadId)
      this.open.delete(threadId)
      if (open) await open.workspace.then((workspace) => workspace.dispose()).catch(() => {})
      const removed = await branches.remove(threadId, { spare: reason === 'idle' })
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
        await this.background.stopThread(id)
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
    for (const id of this.referenceViews.keys()) {
      if (id.startsWith(`${projectId}:`)) this.referenceViews.delete(id)
    }
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
    await this.background.stopAll()
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
