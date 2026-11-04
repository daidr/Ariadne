import type { Ariadne } from '@ariadne/core'
import type { Locale } from '@ariadne/i18n'
import type {
  DashboardSource,
  RepoCapability,
  ToolSpec,
  TrackerCapability,
  Workspace,
} from '@ariadne/plugin-sdk'

export interface ToolContext {
  app: Ariadne
  projectId: string
  conversationId: string
  role: 'coordinator' | 'thread'
  locale: Locale
  signal: AbortSignal
  /** The thread's workspace, opened on first use. */
  workspace(): Promise<Workspace>
  /** The project's hosted repository, when its source is one. */
  repo(): Promise<RepoCapability>
  /** The project's issue tracker, when its source has one. */
  tracker?: () => Promise<TrackerCapability>
  /** Whether input of this turn came from the user's own words (a message, an answer). */
  userSpoke?: () => boolean
  /** The plugins' live data sources for dashboards, by kind. */
  dashboardSources?: ReadonlyMap<string, DashboardSource<never>>
}

export interface ToolResult {
  content: string
  isError?: boolean
}

export interface Tool {
  spec: ToolSpec
  run(args: Record<string, unknown>, ctx: ToolContext): ToolResult | Promise<ToolResult>
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

export const json = (value: unknown): ToolResult => ({ content: JSON.stringify(value) })
