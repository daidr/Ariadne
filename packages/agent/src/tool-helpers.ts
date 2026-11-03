import type { Ariadne } from '@ariadne/core'
import type { Locale } from '@ariadne/i18n'
import type { ToolSpec, Workspace } from '@ariadne/plugin-sdk'

export interface ToolContext {
  app: Ariadne
  projectId: string
  conversationId: string
  role: 'coordinator' | 'thread'
  locale: Locale
  signal: AbortSignal
  /** The thread's workspace, opened on first use. */
  workspace(): Promise<Workspace>
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
