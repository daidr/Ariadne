import {
  DomainError,
  installCommands,
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
  ExecResult,
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
import { headAndTail } from './workspace-tools'
import { BackgroundCommands } from './background'
import { referenceRefusal } from './reference-guard'
import type { ReferenceKind } from './reference-tools'

/** How long setting up a new workspace may take, unless the user's limits say. */
const SETUP_TIMEOUT_MS = 600_000
/** What the model reads at most of the setup script's output. */
const MAX_SETUP_OUTPUT = 8000

export interface WorkspaceManagerOptions {
  /** The environment variables the user gave the project's commands; they may be secret. */
  env?: (projectId: string) => Record<string, string>
  /** The user's rules for thread commands: an install Ariadne chose keeps to them too. */
  commandRules?: (projectId: string) => readonly CommandRule[]
  /** How long the project's setup script may run. */
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

const outputOf = (result: ExecResult) =>
  [result.stdout, result.stderr].filter((part) => part.trim()).join('\n')

/** Told to the thread whose new workspace is in a project with no check after edits yet. */
const CHECK_HINT =
  '[Ariadne: this project has no check after edits yet. Once you know how the repository checks its code, note one with note_check.]'

const heldNote = (held: string[]) =>
  `[Ariadne: your workspace is new, and its dependencies were not installed: the user's command rules do not let ${held.map((command) => `\`${command}\``).join(', ')} run unasked. When the work needs them, run it with run_command, which asks the user where the rules say so.]`

/**
 * The install commands the lockfiles at the root of a workspace, or of the source as it
 * is, call for; none when it has none Ariadne knows, or cannot be listed.
 */
async function lockfileInstalls(
  root: Pick<Workspace, 'list' | 'readBytes'>,
  opened = true,
): Promise<string[]> {
  const entries = await root.list('.').catch(() => [])
  const files = entries.filter((entry) => entry.kind === 'file').map((entry) => entry.name)
  let manager: string | undefined
  if (files.includes('package.json')) {
    try {
      const text = new TextDecoder().decode(await root.readBytes('package.json', 1024 * 1024))
      const field = (JSON.parse(text) as { packageManager?: unknown }).packageManager
      if (typeof field === 'string') manager = field
    } catch {
      // Not readable as JSON: the lockfiles alone decide.
    }
  }
  // `npm ci` starts by deleting node_modules: one that came with the workspace (a local
  // worktree gets the source's, copied on write) is kept as it is.
  // Only in a workspace that was opened: whether the source's comes along depends on where.
  const seeded =
    opened && entries.some((entry) => entry.kind === 'directory' && entry.name === 'node_modules')
  return installCommands(files, manager).filter((command) => !(seeded && command === 'npm ci'))
}

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
  /** Threads whose new workspace was opened, until they are told the project wants a check. */
  private readonly hinted = new Set<string>()
  /** Stops the setup script running in a thread's new workspace. */
  private readonly setupStops = new Map<string, AbortController>()
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
    const notes = [this.setups.get(threadId) ?? '']
    this.setups.delete(threadId)
    // Asked only now: the call that opened the workspace may have noted the check itself.
    if (this.hinted.delete(threadId) && this.checkWanted(threadId)) notes.push(CHECK_HINT)
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

  /**
   * The install commands among `commands` the user's command rules let run unasked
   * (`run`), and the others (`held`), which are not run for the thread.
   */
  private installs(projectId: string, commands: string[]): { run: string[]; held: string[] } {
    const rules = this.options.commandRules?.(projectId) ?? []
    const allowed = (command: string) => commandVerdict(command, rules).action === 'allow'
    return {
      run: commands.filter(allowed),
      held: commands.filter((command) => !allowed(command)),
    }
  }

  /**
   * Find out what a new workspace of the project would install, before any was opened, so
   * the settings can show it: read from the source as it is. Nothing when it is known
   * already, the user wrote their own, or the source cannot be looked at.
   */
  async detectSetup(projectId: string): Promise<void> {
    const project = this.app.getProject(projectId)
    if (!project.source || !project.settings.workspace.auto.setup) return
    if (project.detected.setup !== null) return
    const key = this.sourceKey(projectId)
    const { workspace } = await this.connect(projectId, key)
    if (!workspace?.isolated || !workspace.view) return
    const commands = await lockfileInstalls(await workspace.view(), false)
    // The source may have changed while it was read.
    if (this.sourceKey(projectId) !== key) return
    this.app.recordDetectedSetup(projectId, this.installs(projectId, commands).run.join('\n'))
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
   * The thread's workspace; throws a `DomainError` the model can read. A new workspace's
   * setup script stops when `signal` aborts (the thread's turn was stopped), and runs again
   * the next time the workspace is asked for.
   */
  async forThread(projectId: string, threadId: string, signal?: AbortSignal): Promise<Workspace> {
    const key = this.sourceKey(projectId)
    const stopSetup = () => this.setupStops.get(threadId)?.abort('stopped')
    if (signal?.aborted) stopSetup()
    signal?.addEventListener('abort', stopSetup, { once: true })
    try {
      // Not while its worktree is being removed: it would be used, then deleted under it.
      return await this.serial(threadId, () => this.openFor(projectId, threadId, key))
    } finally {
      signal?.removeEventListener('abort', stopSetup)
    }
  }

  /**
   * End what the thread left running: its background commands, and the setup script of
   * its new workspace.
   */
  async stopThread(threadId: string): Promise<void> {
    this.setupStops.get(threadId)?.abort('stopped')
    await this.background.stopThread(threadId)
  }

  private async openFor(projectId: string, threadId: string, key: string): Promise<Workspace> {
    const cached = this.open.get(threadId)
    if (cached?.key === key) return cached.workspace
    // A workspace opened before keeps the base it started from, and its branch.
    const thread = this.app.getThread(threadId)
    const known = thread.workspace
    const { workspace: setup } = this.app.getProject(projectId).settings
    const branch = known?.branch ?? threadBranch(setup.branchPrefix, threadId, thread.branchSlug)
    // A new directory has nothing installed yet: the project's setup script runs first.
    const fresh = !known || known.removed
    const workspace = this.connect(projectId, key).then(async (capabilities) => {
      if (!capabilities.workspace) throw new DomainError('error.noSource')
      const opened = await capabilities.workspace.open({
        threadId,
        branch,
        ...(known ? { base: known.base } : {}),
      })
      if (fresh) {
        const notes: string[] = []
        if (!setup.auto.setup) {
          if (setup.setup) notes.push(await this.runSetup(projectId, threadId, opened, setup.setup))
        } else if (capabilities.workspace.isolated) {
          // Only in a directory of the thread's own: a shared one is the user's, as it is.
          const { run, held } = this.installs(projectId, await lockfileInstalls(opened))
          this.app.recordDetectedSetup(projectId, run.join('\n'))
          if (run.length) notes.push(await this.install(projectId, threadId, opened, run))
          if (held.length) notes.push(heldNote(held))
        }
        if (notes.length) this.setups.set(threadId, notes.join('\n\n'))
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

  /** Run the user's setup script in a new workspace; what the model is told of it. */
  private async runSetup(
    projectId: string,
    threadId: string,
    workspace: Workspace,
    script: string,
  ): Promise<string> {
    const result = await this.setupExec(projectId, threadId, workspace, script)
    const output = headAndTail(outputOf(result), MAX_SETUP_OUTPUT, 'the rest is not kept')
    return `[Ariadne: your workspace is new, so the project's setup script ran in it first, as the user set it: ${result.exitCode === 0 ? 'it succeeded' : `it failed (exit code ${result.exitCode}); what the work needs may be missing`}.]\n${output || '(no output)'}`
  }

  /**
   * Install a new workspace's dependencies with the commands its lockfiles called for, one
   * after the other up to the first that fails; what the model is told of it.
   */
  private async install(
    projectId: string,
    threadId: string,
    workspace: Workspace,
    commands: string[],
  ): Promise<string> {
    const ran: string[] = []
    // One time limit for all of them, as for a setup script.
    const until = Date.now() + (this.options.setupTimeoutMs?.(projectId) ?? SETUP_TIMEOUT_MS)
    for (const command of commands) {
      const left = Math.max(1000, until - Date.now())
      const result = await this.setupExec(projectId, threadId, workspace, command, left)
      if (result.exitCode === 0) {
        ran.push(command)
        continue
      }
      const output = headAndTail(outputOf(result), MAX_SETUP_OUTPUT, 'the rest is not kept')
      const before = ran.length
        ? ` (after ${ran.map((done) => `\`${done}\``).join(', ')} succeeded)`
        : ''
      return `[Ariadne: your workspace is new, so its dependencies were being installed by its lockfiles, and \`${command}\` failed (exit code ${result.exitCode})${before}; what the work needs may be missing.]\n${output || '(no output)'}`
    }
    return `[Ariadne: your workspace is new, so its dependencies were installed first, by its lockfiles: ${ran.map((done) => `\`${done}\``).join(', ')}.]`
  }

  /** Run one command of a new workspace's setup; the thread's stop ends it. */
  private async setupExec(
    projectId: string,
    threadId: string,
    workspace: Workspace,
    command: string,
    timeoutMs = this.options.setupTimeoutMs?.(projectId) ?? SETUP_TIMEOUT_MS,
  ): Promise<ExecResult> {
    const env = this.env(projectId)
    const { argv } = this.shell(projectId)
    const stop = new AbortController()
    this.setupStops.set(threadId, stop)
    const result = await workspace
      .exec(command, {
        ...(argv ? { shell: argv } : {}),
        timeoutMs,
        signal: stop.signal,
        ...(Object.keys(env).length ? { env } : {}),
      })
      .catch((error: unknown) => ({
        exitCode: -1,
        stdout: '',
        stderr: error instanceof Error ? error.message : String(error),
      }))
      .finally(() => {
        if (this.setupStops.get(threadId) === stop) this.setupStops.delete(threadId)
      })
    // Stopped half way: the workspace is not set up, so it is not handed out; the next
    // turn opens it again and the setup runs from the start.
    if (stop.signal.aborted) {
      throw new DomainError('error.workspace', {
        detail: "the workspace's setup was stopped before it finished",
      })
    }
    return result
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
      await this.background.stopThread(threadId)
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
    for (const stop of this.setupStops.values()) stop.abort('stopped')
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
