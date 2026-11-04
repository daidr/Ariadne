import type { DashboardSource } from '@ariadne/plugin-sdk'
import type { Ariadne, ConversationView } from '@ariadne/core'
import type { Locale } from '@ariadne/i18n'
import { runTurn, type ResolvedModel } from './runner'
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
  maxSteps?: number
  /** Input size, in tokens, at which a conversation's older history is summarised. */
  compactAt?: number
  /** First wait before a failed model request is retried. */
  retryBaseMs?: number
  /** The plugins' live data sources for dashboards. */
  dashboardSources?: ReadonlyMap<string, DashboardSource<never>>
  workspaces?: WorkspaceManager
}

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
        if (event.type === 'thread.stopped') this.cancel(event.data.threadId)
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
      if (this.running.has(conversation.id)) continue
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
        maxSteps: this.options.maxSteps ?? 40,
        ...(this.options.compactAt ? { compactAt: this.options.compactAt } : {}),
        ...(this.options.retryBaseMs !== undefined
          ? { retryBaseMs: this.options.retryBaseMs }
          : {}),
        ...(this.options.workspaces ? { workspaces: this.options.workspaces } : {}),
        ...(this.options.dashboardSources
          ? { dashboardSources: this.options.dashboardSources }
          : {}),
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
