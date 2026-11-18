import type { UiBlock } from './blocks'

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

/** A command a thread left running in the background, as the user sees it in the status line. */
export interface BackgroundView {
  /** What the model names it by, e.g. `cmd-3`. */
  id: string
  command: string
  startedAt: number
  /** The address on this machine it says it serves at, e.g. a dev server's. */
  url: string | null
  /** How it ended; `null` while it runs. */
  result: { exitCode: number; endedAt: number; stopped: boolean } | null
}

/** A conversation's background commands changed: one started or ended, or they are gone. */
export interface BackgroundUpdate {
  projectId: string
  conversationId: string
  commands: BackgroundView[]
}

/** A reply the model is writing now, as far as it has come. */
export interface ReplyDraft {
  /** The id the message gets once posted, so the page keeps showing the same one. */
  messageId: string
  text: string
  /** Display-only cards. No attention exists until the message is stored. */
  blocks?: ReplyDraftBlock[]
  startedAt: number
  /** It was posted as `messageId`; the message itself follows in the log. */
  posted?: boolean
}

export type ReplyDraftBlock =
  UiBlock | { type: 'preview'; title: string; description: string; labels: string[] | null }

/** A conversation's reply draft grew, was posted, or was taken back (`null`). */
export interface DraftUpdate {
  projectId: string
  conversationId: string
  draft: ReplyDraft | null
}
