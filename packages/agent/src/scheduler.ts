import type { SharedArtifacts } from './shared-artifacts'
import type { DashboardSource, SearchProvider } from '@ariadne/plugin-sdk'
import type { Ariadne, ConversationView } from '@ariadne/core'
import type { Locale } from '@ariadne/i18n'
import { overBudget, runTurn, type ResolvedModel } from './runner'
import { DomainError } from '@ariadne/core'
import type { TurnSettings } from './limits'
import type { McpHub } from './mcp'
import type { ModelChoice } from './tools'
import type { WorkspaceManager } from './workspaces'

export interface SchedulerOptions {
  app: Ariadne
  /** The model for a conversation; `null` when none is configured. Throwing fails the turn. */
  resolveModel(conversation: ConversationView): ResolvedModel | null
  locale(): Locale
  /** Turns that may run at once. */
  slots?: number
  /** Extra slots only a turn answering the user may take, so the user never waits in line. */
  reservedSlots?: number
  /** Input size, in tokens, at which a conversation's older history is summarised. */
  compactAt?: number
  /** First wait before a failed model request is retried. */
  retryBaseMs?: number
  /** The plugins' live data sources for dashboards. */
  dashboardSources?: ReadonlyMap<string, DashboardSource<never>>
  /** Documents and whiteboards edited together. */
  shared?: SharedArtifacts
  searchProviders?: ReadonlyMap<string, SearchProvider>
  /** The user's external tool servers. */
  mcp?: McpHub
  workspaces?: WorkspaceManager
  /** The models the user set up, which the coordinator may give a thread. */
  models?: () => ModelChoice[]
  /** The user's limits, instructions and command rules, and the project's environment. */
  settings?: (projectId: string) => TurnSettings
  /** The model researches run on; `null` for the asking conversation's own. */
  researchModel?: () => ResolvedModel | null
  /** The user's own skills for every project, `<home>/skills`. */
  skillsDir?: string
}

/** Reports after which a thread's background commands have nothing more to do. */
const FINAL_REPORTS = new Set(['ready', 'done', 'failed', 'blocked'])

interface Running {
  controller: AbortController
  projectId: string
  reserved: boolean
  done: Promise<void>
}

/**
 * Starts a turn for every conversation with unseen input, within the slot limits, and
 * keeps doing so as events arrive. One turn per conversation at a time.
 */
export class Scheduler {
  private readonly running = new Map<string, Running>()
  /** Projects whose turns may not start, e.g. while one is being deleted. */
  private readonly held = new Set<string>()
  private unsubscribe: (() => void) | null = null
  private scheduled = false
  private stopped = false
  private slots: number
  private reservedSlots: number

  constructor(private readonly options: SchedulerOptions) {
    this.slots = options.slots ?? 3
    this.reservedSlots = options.reservedSlots ?? 1
  }

  /** Change the slot limits. Running turns finish; new ones start within the new limits. */
  setSlots(slots: number, reservedSlots: number): void {
    this.slots = slots
    this.reservedSlots = reservedSlots
    this.schedule()
  }

  /** Close turns the last process left running, then follow the log. */
  start(): void {
    const { app } = this.options
    for (const conversation of app.listRunningConversations()) {
      if (conversation.turn) {
        app.finishTurn(conversation.id, conversation.turn.id, 'interrupted', {
          key: 'error.interrupted',
        })
      }
    }
    this.unsubscribe = app.subscribe((events) => {
      // A held thread's turn ends now, with whatever command it runs; that frees its slot.
      for (const event of events) {
        if (event.type === 'thread.stopped') {
          this.cancel(event.data.threadId)
          void this.options.workspaces?.stopThread(event.data.threadId)
        }
        // Its work is finished: what it left running in the background ends.
        if (event.type === 'thread.reported' && FINAL_REPORTS.has(event.data.report)) {
          void this.options.workspaces?.background.stopThread(event.data.threadId)
        }
      }
      this.schedule()
    })
    this.schedule()
  }

  isRunning(conversationId: string): boolean {
    return this.running.has(conversationId)
  }

  /** Stop a conversation's turn on the user's request. */
  cancel(conversationId: string): boolean {
    const running = this.running.get(conversationId)
    if (!running) return false
    running.controller.abort('cancelled')
    return true
  }

  /**
   * End the project's turns and start none until the returned release is called. Resolves
   * once every turn of the project has ended. They end as interrupted, so they go on if
   * the project stays after all.
   */
  async holdProject(projectId: string): Promise<() => void> {
    this.held.add(projectId)
    const ending = [...this.running.values()].filter((running) => running.projectId === projectId)
    for (const running of ending) running.controller.abort('interrupted')
    await Promise.allSettled(ending.map((running) => running.done))
    return () => {
      this.held.delete(projectId)
      this.schedule()
    }
  }

  /** Abort every turn as interrupted, so each resumes on the next start. */
  async stop(): Promise<void> {
    this.stopped = true
    this.unsubscribe?.()
    const all = [...this.running.values()]
    for (const running of all) running.controller.abort('interrupted')
    await Promise.allSettled(all.map((running) => running.done))
  }

  /** Wait until no turn runs and nothing is pending; for tests. */
  async idle(): Promise<void> {
    for (;;) {
      await new Promise((resolve) => setTimeout(resolve, 0))
      if (this.scheduled) continue
      if (!this.running.size) return
      await Promise.allSettled([...this.running.values()].map((r) => r.done))
    }
  }

  // Appends call listeners synchronously; starting turns from inside one would nest appends.
  private schedule(): void {
    if (this.scheduled || this.stopped) return
    this.scheduled = true
    queueMicrotask(() => {
      this.scheduled = false
      this.fill()
    })
  }

  private fill(): void {
    const { app } = this.options
    const { slots, reservedSlots } = this
    const caps = new Map<string, number | null>()
    const capOf = (projectId: string) => {
      if (!caps.has(projectId)) {
        let cap: number | null = null
        try {
          cap = app.getProject(projectId).slots
        } catch {
          // A project removed meanwhile: no cap to keep.
        }
        caps.set(projectId, cap)
      }
      return caps.get(projectId) ?? null
    }
    for (const conversation of app.listPendingConversations()) {
      if (this.stopped) return
      if (this.running.has(conversation.id) || this.held.has(conversation.projectId)) continue
      const used = [...this.running.values()]
      const general = used.filter((r) => !r.reserved).length
      const reserved = used.length - general
      const cap = capOf(conversation.projectId)
      // A project's cap counts its general slots only: the user's own message is never held.
      const projectFull =
        cap !== null &&
        used.filter((r) => !r.reserved && r.projectId === conversation.projectId).length >= cap
      if (general < slots && !projectFull) this.launch(conversation, false)
      else if (reserved < reservedSlots && app.pendingIsUserMessage(conversation.id)) {
        this.launch(conversation, true)
      }
    }
  }

  private launch(conversation: ConversationView, reserved: boolean): void {
    const { app } = this.options
    const controller = new AbortController()
    const done = (async () => {
      // A conversation over its token budget does not start: it would only stop again.
      const over = overBudget(app, conversation.projectId, conversation.id)
      if (over) {
        app.failBeforeTurn(conversation.id, new DomainError(over.key, over.params))
        return
      }
      let model: ResolvedModel | null
      try {
        model = this.options.resolveModel(conversation)
      } catch (error) {
        app.failBeforeTurn(conversation.id, error)
        return
      }
      if (!model) {
        app.failBeforeTurn(conversation.id, null)
        return
      }
      await runTurn({
        app,
        conversation,
        model,
        locale: this.options.locale(),
        signal: controller.signal,
        ...(this.options.compactAt ? { compactAt: this.options.compactAt } : {}),
        ...(this.options.retryBaseMs !== undefined
          ? { retryBaseMs: this.options.retryBaseMs }
          : {}),
        ...(this.options.workspaces ? { workspaces: this.options.workspaces } : {}),
        ...(this.options.models ? { models: this.options.models() } : {}),
        ...(this.options.dashboardSources
          ? { dashboardSources: this.options.dashboardSources }
          : {}),
        ...(this.options.shared ? { shared: this.options.shared } : {}),
        ...(this.options.searchProviders ? { searchProviders: this.options.searchProviders } : {}),
        ...(this.options.mcp ? { mcp: this.options.mcp } : {}),
        ...(this.options.settings
          ? { settings: this.options.settings(conversation.projectId) }
          : {}),
        ...(this.options.researchModel ? { researchModel: this.options.researchModel() } : {}),
        ...(this.options.skillsDir ? { skillsDir: this.options.skillsDir } : {}),
      })
    })()
      .catch((error) => console.error('turn crashed', error))
      .finally(() => {
        this.running.delete(conversation.id)
        this.schedule()
      })
    this.running.set(conversation.id, {
      controller,
      projectId: conversation.projectId,
      reserved,
      done,
    })
  }
}
