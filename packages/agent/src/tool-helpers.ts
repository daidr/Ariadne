import type { SharedArtifacts } from './shared-artifacts'
import type { Ariadne, Attachment } from '@ariadne/core'
import type { Locale } from '@ariadne/i18n'
import type {
  BranchCapability,
  DashboardSource,
  RepoCapability,
  SourceView,
  ToolSpec,
  TrackerCapability,
  Workspace,
} from '@ariadne/plugin-sdk'
import { keepMessageImages } from './message-images'
import type { BackgroundCommands } from './background'
import type { CommandRule, Limits } from './limits'
import type { Researcher } from './research'
import type { OpenPath } from './partial-json'
import type { SkillSet } from './skills'

export interface ToolContext {
  app: Ariadne
  projectId: string
  conversationId: string
  role: 'coordinator' | 'thread'
  locale: Locale
  signal: AbortSignal
  /** The running turn, and the call being run. */
  turnId?: string
  toolCallId?: string | undefined
  /**
   * The call runs for a research, not for the conversation itself: what it reads is not
   * what the conversation saw.
   */
  sub?: boolean
  /** The thread's workspace, opened on first use. */
  workspace(): Promise<Workspace>
  /** The project's hosted repository, when its source is one. */
  repo(): Promise<RepoCapability>
  /** The project's source as it is, read-only: what the coordinator looks at. */
  source?: () => Promise<SourceView>
  /** The project's threads' branches, read without their worktrees. */
  branches?: () => Promise<BranchCapability | null>
  /** The project's issue tracker, when its source has one. */
  tracker?: () => Promise<TrackerCapability>
  /** Whether input of this turn came from the user's own words (a message, an answer). */
  userSpoke?: () => boolean
  /** The plugins' live data sources for dashboards, by kind. */
  dashboardSources?: ReadonlyMap<string, DashboardSource<never>>
  /**
   * Documents and whiteboards edited together: an agent writes them into their room,
   * where people may be editing at the same time. Without it they are saved whole.
   */
  shared?: SharedArtifacts
  /** The models the user set up, which the coordinator may give a thread. */
  models?: ModelChoice[]
  /** The user's limits; the defaults without. */
  limits?: Limits
  /**
   * How the project's thread commands run: the user's rules, the shell (its name, and its
   * command line, `null` for the machine's own), the environment, and the check the user
   * wants run after each edit (`''` for none).
   */
  commands?: {
    rules: CommandRule[]
    shell: readonly string[] | null
    shellName?: string
    env: Record<string, string>
    check?: string
  }
  /** Commands threads left running in the background. */
  background?: BackgroundCommands
  /** Asks a cheaper model, in a context of its own, to look into something read-only. */
  research?: Researcher | undefined
  /** The skills the conversation can read, as loaded last. */
  skills?: () => SkillSet
}

/** A model the user set up, as the coordinator picks among them for a thread. */
export interface ModelChoice {
  id: string
  /** e.g. `provider / model`. */
  label: string
  /** What the user said it is for. */
  note: string
  /** Threads use it unless given another. */
  threadDefault: boolean
}

export interface ToolResult {
  content: string
  isError?: boolean
  /** Images for the model to look at, already kept in the project's files. */
  files?: Attachment[]
}

export interface Tool {
  spec: ToolSpec
  /**
   * Only looks: it changes nothing the model or the user could see. Calls of such tools
   * made in one step run at the same time; the others run one by one, in the model's order.
   */
  readOnly?: boolean
  run(args: Record<string, unknown>, ctx: ToolContext): ToolResult | Promise<ToolResult>
  /**
   * Shows the call at work while the model is still writing its arguments, for a tool
   * whose change the user can watch land (a document, a whiteboard). The call still runs
   * once whole, and takes over from what was shown.
   */
  live?(ctx: ToolContext): LiveCall
}

/** One tool call shown while it is written. */
export interface LiveCall {
  /**
   * The arguments as far as they are written: `open` is the path of the value the text
   * stops in (see `readPartialJson`), so a string still growing can be told from a whole one.
   */
  input(args: Record<string, unknown>, open: OpenPath | null): void
  /** The call will not run: take back what was shown. A call that ran is left alone. */
  cancel(): void
}

/** A bad argument the model can fix; it gets the message back as an error result. */
export class ToolInputError extends Error {}

export function str(args: Record<string, unknown>, key: string): string {
  const value = args[key]
  if (typeof value !== 'string' || !value.trim()) {
    throw new ToolInputError(`"${key}" must be a non-empty string`)
  }
  return value
}

export function optionalInt(args: Record<string, unknown>, key: string): number | null {
  const value = args[key]
  if (value === undefined || value === null) return null
  if (!Number.isInteger(value)) throw new ToolInputError(`"${key}" must be an integer`)
  return value as number
}

export function optionalStr(args: Record<string, unknown>, key: string): string | undefined {
  const value = args[key]
  if (value === undefined || value === null) return undefined
  if (typeof value !== 'string') throw new ToolInputError(`"${key}" must be a string`)
  return value
}

export function optionalBool(args: Record<string, unknown>, key: string): boolean {
  const value = args[key]
  if (value === undefined || value === null) return false
  if (typeof value !== 'boolean') throw new ToolInputError(`"${key}" must be true or false`)
  return value
}

export const json = (value: unknown): ToolResult => ({ content: JSON.stringify(value) })

/**
 * A message the agent writes, with the images of its workspace it links kept in the
 * project's files (see `keepMessageImages`), when its workspace is open. The coordinator
 * has no workspace: its own paths stay text, and it shows images by the `file:` links threads wrote.
 */
export function withImages(ctx: ToolContext, content: string): Promise<string> {
  // Only a workspace already there: a message alone does not open a worktree or a remote.
  const known = ctx.role === 'thread' ? ctx.app.getThread(ctx.conversationId).workspace : null
  if (!known || known.removed) return Promise.resolve(content)
  return keepMessageImages(content, ctx.workspace, (file) => ctx.app.saveFile(ctx.projectId, file))
}
