import { DomainError } from './errors'

/**
 * The shells a project's commands can run in, with how each takes a command line. Ariadne's
 * own commands (searching, git) keep bash; this is for what threads run and the setup script.
 */
export const SHELLS = {
  bash: ['bash', '-c'],
  zsh: ['zsh', '-c'],
  sh: ['sh', '-c'],
  pwsh: ['pwsh', '-NoProfile', '-Command'],
  powershell: ['powershell', '-NoProfile', '-Command'],
  cmd: ['cmd', '/d', '/s', '/c'],
} as const satisfies Record<string, readonly string[]>

export type ShellName = keyof typeof SHELLS
export const SHELL_NAMES = Object.keys(SHELLS) as ShellName[]
/** Shells a POSIX machine (SSH, daemon) has: the others only exist on Windows, mostly. */
export const POSIX_SHELLS: ShellName[] = ['bash', 'zsh', 'sh']

/**
 * What the user set for one project beyond its source: words every conversation of it
 * follows, how its threads' workspaces are set up, the built-in tools it goes without, and
 * how many tokens it may spend. Secrets (environment variables) are kept in the user's
 * settings file, not here.
 */
export interface ProjectSettings {
  /** Instructions for this project that are not in its repository, for every conversation. */
  instructions: string
  /** Groups of built-in tools its conversations do not get, e.g. `fetch` for fetch_url. */
  tools: string[]
  /** Tokens (read and written) one thread, and the whole project, may use; `null` is no cap. */
  budget: { thread: number | null; project: number | null }
  workspace: {
    /** Run in a thread's workspace once it is new, e.g. `bun install`; `''` is none. */
    setup: string
    /** What threads' commands and the setup script run in. */
    shell: ShellName
    /** Threads' branches start with it, e.g. `ariadne/`. */
    branchPrefix: string
  }
}

export const DEFAULT_BRANCH_PREFIX = 'ariadne/'
export const MAX_PROJECT_INSTRUCTIONS = 16_000
export const MAX_SETUP_SCRIPT = 8000

export const defaultProjectSettings = (): ProjectSettings => ({
  instructions: '',
  tools: [],
  budget: { thread: null, project: null },
  workspace: { setup: '', shell: 'bash', branchPrefix: DEFAULT_BRANCH_PREFIX },
})

/**
 * Whether `prefix` can start a git branch name: plain characters and `/`, no part that
 * starts with a dot or `-`, no `..`, and nothing git refuses in a ref. Empty is fine.
 */
export function validBranchPrefix(prefix: string): boolean {
  if (prefix === '') return true
  if (prefix.length > 40 || !/^[A-Za-z0-9._/-]+$/.test(prefix)) return false
  if (prefix.startsWith('/') || prefix.startsWith('-') || prefix.includes('..')) return false
  if (prefix.includes('//') || prefix.endsWith('.lock') || prefix.endsWith('.')) return false
  return prefix
    .split('/')
    .every((part, i, all) => (i === all.length - 1 && !part) || !/^[.-]/.test(part))
}

/**
 * A short readable branch slug from what the coordinator suggested: lowercase words joined
 * by `-`, at most 40 characters; `null` when nothing usable is left.
 */
export function branchSlug(raw: unknown): string | null {
  if (typeof raw !== 'string') return null
  const slug = raw
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40)
    .replace(/-+$/, '')
  return slug || null
}

/** A thread's branch: the prefix, the slug when there is one, and the id's tail. */
export function threadBranch(prefix: string, threadId: string, slug: string | null): string {
  return slug ? `${prefix}${slug}-${threadId.slice(-6)}` : `${prefix}${threadId.slice(-12)}`
}

const budgetValue = (raw: unknown, at: string): number | null => {
  if (raw === null || raw === undefined || raw === '') return null
  if (!Number.isInteger(raw) || (raw as number) < 1000) {
    throw new DomainError('error.invalidBody', { reason: at })
  }
  return raw as number
}

const bad = (reason: string) => new DomainError('error.invalidBody', { reason })

/**
 * Settings checked and filled in from what the user sent. `tools` are the groups of tools
 * that can be turned off; the input may name only those.
 */
export function parseProjectSettings(input: unknown, tools: readonly string[]): ProjectSettings {
  const body = (input ?? {}) as Partial<Record<keyof ProjectSettings, unknown>>
  const out = defaultProjectSettings()
  if (body.instructions !== undefined) {
    if (typeof body.instructions !== 'string') throw bad('instructions')
    if (body.instructions.length > MAX_PROJECT_INSTRUCTIONS) throw bad('instructions')
    out.instructions = body.instructions.trim()
  }
  if (body.tools !== undefined) {
    if (!Array.isArray(body.tools) || body.tools.some((name) => !tools.includes(name))) {
      throw bad('tools')
    }
    out.tools = [...new Set(body.tools as string[])].toSorted()
  }
  if (body.budget !== undefined) {
    const budget = (body.budget ?? {}) as Record<string, unknown>
    out.budget = {
      thread: budgetValue(budget.thread, 'budget.thread'),
      project: budgetValue(budget.project, 'budget.project'),
    }
  }
  if (body.workspace !== undefined) {
    const workspace = (body.workspace ?? {}) as Record<string, unknown>
    const setup = workspace.setup ?? ''
    if (typeof setup !== 'string' || setup.length > MAX_SETUP_SCRIPT) throw bad('workspace.setup')
    const shell = workspace.shell ?? 'bash'
    if (!SHELL_NAMES.includes(shell as ShellName)) throw bad('workspace.shell')
    const prefix = workspace.branchPrefix ?? DEFAULT_BRANCH_PREFIX
    if (typeof prefix !== 'string' || !validBranchPrefix(prefix.trim())) {
      throw bad('workspace.branchPrefix')
    }
    out.workspace = { setup: setup.trim(), shell: shell as ShellName, branchPrefix: prefix.trim() }
  }
  return out
}

/** Stored settings read back; a project that never set any has the defaults. */
export const storedProjectSettings = (raw: string | null): ProjectSettings =>
  raw ? (JSON.parse(raw) as ProjectSettings) : defaultProjectSettings()
