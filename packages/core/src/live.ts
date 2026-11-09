/**
 * A command a thread is running now, as the user watches it: what it is, since when, and
 * the end of its output so far. Kept in memory only: the event log records the command's
 * call and its whole result once it ends, and the trace shows them.
 */
export interface LiveCommand {
  projectId: string
  conversationId: string
  command: string
  /** Where it runs, relative to the workspace; `.` at its root. */
  cwd: string
  startedAt: number
  /** The end of the output so far, as a terminal would show it. */
  tail: string
}

/** A conversation's running command changed: it started, printed more, or ended (`null`). */
export interface LiveUpdate {
  projectId: string
  conversationId: string
  command: LiveCommand | null
}

/** One running command, for the tool that runs it. */
export interface LiveHandle {
  output(text: string): void
  end(): void
}

/** How much of the output's end is kept, in characters. */
export const LIVE_TAIL = 8000
/** Output reaches watchers at most this often. */
const LIVE_INTERVAL_MS = 250

// oxlint-disable-next-line no-control-regex
const ESCAPES = /\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]/g

/**
 * Output as a terminal leaves it on screen, roughly: colour and cursor codes dropped, and
 * a line redrawn after a carriage return (a progress bar) kept as its last drawing.
 */
export function screenText(text: string): string {
  return text
    .replace(ESCAPES, '')
    .replace(/\r\n/g, '\n')
    .split('\n')
    .map((line) => {
      const parts = line.split('\r')
      return parts.findLast((part) => part.length) ?? ''
    })
    .join('\n')
}

/** The commands threads are running now, and who watches them. */
export class LiveCommands {
  private readonly running = new Map<string, { id: number; command: LiveCommand; raw: string }>()
  private readonly listeners = new Set<(update: LiveUpdate) => void>()
  private readonly timers = new Map<string, ReturnType<typeof setTimeout>>()
  private next = 1

  constructor(private readonly now: () => number = Date.now) {}

  /** Watch every change; returns how to stop. */
  subscribe(listener: (update: LiveUpdate) => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  /** The commands running now, of one project or all. */
  list(projectId: string | null = null): LiveCommand[] {
    return [...this.running.values()]
      .map((entry) => entry.command)
      .filter((command) => projectId === null || command.projectId === projectId)
  }

  /**
   * A command starts in a conversation; it takes the place of one still shown there. The
   * handle's calls after `end` change nothing.
   */
  start(input: Omit<LiveCommand, 'startedAt' | 'tail'>): LiveHandle {
    const id = this.next++
    const key = input.conversationId
    const entry = { id, command: { ...input, startedAt: this.now(), tail: '' }, raw: '' }
    this.running.set(key, entry)
    this.emit(key)
    const current = () => this.running.get(key)?.id === id
    return {
      output: (text) => {
        if (!current() || !text) return
        // Kept a little longer than shown, so a line redrawn at the cut stays whole.
        entry.raw = (entry.raw + text).slice(-LIVE_TAIL * 2)
        if (this.timers.has(key)) return
        this.timers.set(
          key,
          setTimeout(() => {
            this.timers.delete(key)
            if (!current()) return
            entry.command = { ...entry.command, tail: screenText(entry.raw).slice(-LIVE_TAIL) }
            this.emit(key)
          }, LIVE_INTERVAL_MS),
        )
      },
      end: () => {
        if (!current()) return
        this.running.delete(key)
        clearTimeout(this.timers.get(key))
        this.timers.delete(key)
        this.emit(key, input.projectId)
      },
    }
  }

  private emit(conversationId: string, projectId?: string) {
    const entry = this.running.get(conversationId)
    const update: LiveUpdate = {
      projectId: entry?.command.projectId ?? projectId ?? '',
      conversationId,
      command: entry?.command ?? null,
    }
    for (const listener of this.listeners) listener(update)
  }
}

/** A command a thread left running in the background, as the user sees it in the status line. */
export interface BackgroundView {
  /** What the model names it by, e.g. `cmd-3`. */
  id: string
  command: string
  startedAt: number
  /** How it ended; `null` while it runs. */
  result: { exitCode: number; endedAt: number; stopped: boolean } | null
}

/** A conversation's background commands changed: one started or ended, or they are gone. */
export interface BackgroundUpdate {
  projectId: string
  conversationId: string
  commands: BackgroundView[]
}

/**
 * The background commands of every thread, as the user watches them. Kept in memory only,
 * like the commands themselves, which end when the server stops.
 */
export class BackgroundCommandsList {
  private readonly lists = new Map<string, BackgroundUpdate>()
  private readonly listeners = new Set<(update: BackgroundUpdate) => void>()

  subscribe(listener: (update: BackgroundUpdate) => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  /** The conversations that have background commands, of one project or all. */
  list(projectId: string | null = null): BackgroundUpdate[] {
    return [...this.lists.values()].filter(
      (update) => projectId === null || update.projectId === projectId,
    )
  }

  set(update: BackgroundUpdate): void {
    if (update.commands.length) this.lists.set(update.conversationId, update)
    else this.lists.delete(update.conversationId)
    for (const listener of this.listeners) listener(update)
  }
}

/** A reply the model is writing now, as far as it has come. */
export interface ReplyDraft {
  /** The id the message gets once posted, so the page keeps showing the same one. */
  messageId: string
  text: string
  startedAt: number
  /** It was posted as `messageId`; the message itself follows in the log. */
  posted?: boolean
}

/** A conversation's reply draft grew, was posted, or was taken back (`null`). */
export interface DraftUpdate {
  projectId: string
  conversationId: string
  draft: ReplyDraft | null
}

/** One step's reply draft, for the turn that writes it. */
export interface DraftHandle {
  readonly messageId: string
  /** More of the step's text. */
  text(piece: string): void
  /** It is not the reply after all (a tool call began, or the attempt failed): forget it. */
  withdraw(): void
  /** It was posted as `messageId`; calls after this change nothing. */
  posted(): void
}

/** Drafts reach watchers at most this often. */
const DRAFT_INTERVAL_MS = 100
/** A single line shorter than this is held back: it may be what the model does next. */
const DRAFT_HOLD = 120

/**
 * Whether text is shown while it is written. The model starts a step that calls tools with
 * one short line on what it does now, which goes to the status line, never to the chat: so
 * text shows once it is more than that, a second line or a long first one.
 */
export function draftShows(text: string): boolean {
  const body = text.trimStart()
  return body.length >= DRAFT_HOLD || /\n\s*\S/.test(body.trimEnd())
}

/**
 * The replies models are writing now, by conversation. Kept in memory only: the reply is
 * logged whole once posted, and text beside tool calls goes to the trace with its step.
 */
export class ReplyDrafts {
  private readonly drafts = new Map<string, { id: number; update: DraftUpdate }>()
  private readonly listeners = new Set<(update: DraftUpdate) => void>()
  private readonly timers = new Map<string, ReturnType<typeof setTimeout>>()
  private next = 1

  constructor(private readonly now: () => number = Date.now) {}

  subscribe(listener: (update: DraftUpdate) => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  /** The drafts shown now, of one project or all. */
  list(projectId: string | null = null): DraftUpdate[] {
    return [...this.drafts.values()]
      .map((entry) => entry.update)
      .filter((update) => projectId === null || update.projectId === projectId)
  }

  /** Take back a conversation's draft, if one is shown. */
  drop(conversationId: string): void {
    const entry = this.drafts.get(conversationId)
    if (!entry) return
    clearTimeout(this.timers.get(conversationId))
    this.timers.delete(conversationId)
    this.drafts.delete(conversationId)
    this.emit({ projectId: entry.update.projectId, conversationId, draft: null })
  }

  /** A step starts writing in a conversation; it takes the place of a draft still there. */
  start(input: { projectId: string; conversationId: string; messageId: string }): DraftHandle {
    const id = this.next++
    const key = input.conversationId
    let text = ''
    let startedAt = 0
    let over = false
    const current = () => this.drafts.get(key)?.id === id
    // Only its own timer: a draft that took its place has one of its own.
    const clear = () => {
      if (!current()) return
      clearTimeout(this.timers.get(key))
      this.timers.delete(key)
    }
    const drop = () => {
      clear()
      if (!current()) return
      this.drafts.delete(key)
      this.emit({ projectId: input.projectId, conversationId: key, draft: null })
    }
    const set = (posted: boolean) => {
      const draft: ReplyDraft = {
        messageId: input.messageId,
        text,
        startedAt,
        ...(posted ? { posted: true } : {}),
      }
      const update = { projectId: input.projectId, conversationId: key, draft }
      if (posted) this.drafts.delete(key)
      else this.drafts.set(key, { id, update })
      this.emit(update)
    }
    // A draft of an earlier step still shown is gone with this one.
    this.drop(key)
    return {
      messageId: input.messageId,
      text: (piece) => {
        if (over || !piece) return
        if (!text) startedAt = this.now()
        text += piece
        if (!draftShows(text)) return
        if (!current()) {
          // Shown from its first lines at once, then as it grows.
          this.drafts.set(key, { id, update: { ...input, draft: null } })
          set(false)
          return
        }
        if (this.timers.has(key)) return
        this.timers.set(
          key,
          setTimeout(() => {
            this.timers.delete(key)
            if (current() && !over) set(false)
          }, DRAFT_INTERVAL_MS),
        )
      },
      withdraw: () => {
        if (over) return
        text = ''
        drop()
      },
      posted: () => {
        if (over) return
        over = true
        clear()
        // Only a draft the user saw needs handing over to its message.
        if (current()) set(true)
      },
    }
  }

  private emit(update: DraftUpdate) {
    for (const listener of this.listeners) listener(update)
  }
}
