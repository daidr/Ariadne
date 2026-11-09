import { DomainError } from './errors'

/**
 * The shells a project's commands can run in, with how each takes a command line: what
 * threads run, the setup script and the check after edits. Ariadne's own commands
 * (searching, finding files, git) run their programs without a shell.
 */
export const SHELLS = {
  bash: ['bash', '-c'],
  zsh: ['zsh', '-c'],
  sh: ['sh', '-c'],
  pwsh: ['pwsh', '-NoProfile', '-NonInteractive', '-Command'],
  powershell: ['powershell', '-NoProfile', '-NonInteractive', '-Command'],
  cmd: ['cmd', '/d', '/s', '/c'],
} as const satisfies Record<string, readonly string[]>

export type ShellName = keyof typeof SHELLS
/**
 * A project's choice: one of the shells, or `auto` for the machine's own (bash; on Windows
 * PowerShell 7, else 5.1).
 */
export type ShellChoice = ShellName | 'auto'
export const SHELL_NAMES = Object.keys(SHELLS) as ShellName[]
export const SHELL_CHOICES: ShellChoice[] = ['auto', ...SHELL_NAMES]
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
    /**
     * What is left to Ariadne. `setup`: a new workspace gets its dependencies installed by
     * the lockfiles in it (`installCommands`), and nothing when it has none it knows.
     * `check`: the check after edits is the one the first thread to work in the repository
     * noted. Either is off once the user wrote their own, or emptied the field.
     */
    auto: { setup: boolean; check: boolean }
    /**
     * The user's own, in place of the install by lockfile: run in a thread's workspace
     * once it is new; `''` runs nothing. Not read while `auto.setup`.
     */
    setup: string
    /** What threads' commands, the setup script and the check run in. */
    shell: ShellChoice
    /**
     * Run after a thread's edit_file or write_file, e.g. a type check or a linter; `{file}`
     * stands for the files changed. Its failure is told to the model with the edit's
     * result. `''` is none. The user's own; not read while `auto.check`.
     */
    check: string
    /** Threads' branches start with it, e.g. `ariadne/`. */
    branchPrefix: string
  }
  /** Prompts that wake the coordinator on a schedule, e.g. sorting new issues on workdays. */
  routines: Routine[]
  /**
   * Before changes a thread reports `ready` reach the user, another thread reviews them,
   * at most `MAX_REVIEW_ROUNDS` times. Only for branches with something to merge.
   */
  review: { enabled: boolean }
  /**
   * Autopilot: changes that passed the AI review, with green checks and no conflict, are
   * merged with `method`; plan nodes whose dependencies are done get threads, as many at
   * work as the project's slots allow; a milestone waits for the user's yes. Needs the
   * review on.
   */
  autopilot: { enabled: boolean; method: MergeMethod }
}

export type MergeMethod = 'squash' | 'merge'
export const MERGE_METHODS: MergeMethod[] = ['squash', 'merge']

/**
 * Something the coordinator does on a schedule: at `time` (local, `HH:MM`) on the given
 * weekdays (0 is Sunday; all seven for every day), it is woken with `prompt`.
 */
export interface Routine {
  id: string
  prompt: string
  days: number[]
  time: string
  enabled: boolean
}

export const MAX_ROUTINES = 20
export const MAX_ROUTINE_PROMPT = 2000
const TIME = /^([01]\d|2[0-3]):([0-5]\d)$/
const ROUTINE_ID = /^[A-Za-z0-9_-]{1,40}$/

/**
 * When `routine` runs next after `after` (epoch ms), in the server's local time; `null`
 * when it is off or has no days. A run is due at the minute, so one at exactly `after`
 * counts as past.
 */
export function nextRun(routine: Routine, after: number): number | null {
  if (!routine.enabled || !routine.days.length) return null
  const match = TIME.exec(routine.time)
  if (!match) return null
  const [hours, minutes] = [Number(match[1]), Number(match[2])]
  const day = new Date(after)
  for (let i = 0; i <= 7; i++) {
    const at = new Date(day.getFullYear(), day.getMonth(), day.getDate() + i, hours, minutes)
    if (at.getTime() > after && routine.days.includes(at.getDay())) return at.getTime()
  }
  return null
}

/** Review rounds of one delivery before it goes to the user whatever the last verdict. */
export const MAX_REVIEW_ROUNDS = 2

export const DEFAULT_BRANCH_PREFIX = 'ariadne/'
export const MAX_PROJECT_INSTRUCTIONS = 16_000
export const MAX_SETUP_SCRIPT = 8000
export const MAX_CHECK_COMMAND = 2000

export const defaultProjectSettings = (): ProjectSettings => ({
  instructions: '',
  tools: [],
  budget: { thread: null, project: null },
  workspace: {
    auto: { setup: true, check: true },
    setup: '',
    shell: 'auto',
    check: '',
    branchPrefix: DEFAULT_BRANCH_PREFIX,
  },
  routines: [],
  review: { enabled: true },
  autopilot: { enabled: false, method: 'squash' },
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
    const shell = workspace.shell ?? 'auto'
    if (!SHELL_CHOICES.includes(shell as ShellChoice)) throw bad('workspace.shell')
    const check = workspace.check ?? ''
    if (typeof check !== 'string' || check.length > MAX_CHECK_COMMAND) {
      throw bad('workspace.check')
    }
    const prefix = workspace.branchPrefix ?? DEFAULT_BRANCH_PREFIX
    if (typeof prefix !== 'string' || !validBranchPrefix(prefix.trim())) {
      throw bad('workspace.branchPrefix')
    }
    // Left out, a field with words in it is the user's own and an empty one is Ariadne's.
    const auto = (workspace.auto ?? {}) as Record<string, unknown>
    const autoSetup = auto.setup ?? !setup.trim()
    const autoCheck = auto.check ?? !check.trim()
    if (typeof autoSetup !== 'boolean') throw bad('workspace.auto.setup')
    if (typeof autoCheck !== 'boolean') throw bad('workspace.auto.check')
    out.workspace = {
      auto: { setup: autoSetup, check: autoCheck },
      setup: autoSetup ? '' : setup.trim(),
      shell: shell as ShellChoice,
      check: autoCheck ? '' : check.trim(),
      branchPrefix: prefix.trim(),
    }
  }
  if (body.routines !== undefined) out.routines = parseRoutines(body.routines)
  if (body.review !== undefined) {
    const enabled = ((body.review ?? {}) as Record<string, unknown>).enabled ?? true
    if (typeof enabled !== 'boolean') throw bad('review.enabled')
    out.review = { enabled }
  }
  if (body.autopilot !== undefined) {
    const autopilot = (body.autopilot ?? {}) as Record<string, unknown>
    const enabled = autopilot.enabled ?? false
    if (typeof enabled !== 'boolean') throw bad('autopilot.enabled')
    // What it merges is what the review let through; without the review it has nothing.
    if (enabled && !out.review.enabled) throw bad('autopilot.enabled')
    const method = autopilot.method ?? 'squash'
    if (!MERGE_METHODS.includes(method as MergeMethod)) throw bad('autopilot.method')
    out.autopilot = { enabled, method: method as MergeMethod }
  }
  return out
}

function parseRoutines(raw: unknown): Routine[] {
  if (!Array.isArray(raw) || raw.length > MAX_ROUTINES) throw bad('routines')
  const ids = new Set<string>()
  return raw.map((item, i) => {
    const at = `routines.${i}`
    const routine = (item ?? {}) as Record<string, unknown>
    const { id, prompt, days, time, enabled } = routine
    if (typeof id !== 'string' || !ROUTINE_ID.test(id) || ids.has(id)) throw bad(`${at}.id`)
    ids.add(id)
    if (typeof prompt !== 'string' || !prompt.trim() || prompt.length > MAX_ROUTINE_PROMPT) {
      throw bad(`${at}.prompt`)
    }
    if (!Array.isArray(days) || days.some((day) => !Number.isInteger(day) || day < 0 || day > 6)) {
      throw bad(`${at}.days`)
    }
    if (typeof time !== 'string' || !TIME.test(time)) throw bad(`${at}.time`)
    return {
      id,
      prompt: prompt.trim(),
      days: [...new Set(days as number[])].toSorted(),
      time,
      enabled: enabled !== false,
    }
  })
}

/** Stored settings read back; a project that never set any has the defaults. */
export function storedProjectSettings(raw: string | null): ProjectSettings {
  if (!raw) return defaultProjectSettings()
  const settings = { ...defaultProjectSettings(), ...(JSON.parse(raw) as ProjectSettings) }
  const { workspace } = settings
  // Settings saved before `auto`: a script or a check the user wrote stays theirs.
  settings.workspace = {
    ...workspace,
    auto: workspace.auto ?? { setup: !workspace.setup, check: !workspace.check },
  }
  return settings
}

/**
 * What Ariadne found out about a project's repository by itself, shown in the settings in
 * place of what the user would otherwise have to write. A new source starts it over.
 */
export interface ProjectDetected {
  /**
   * The install commands the lockfiles of the last new workspace called for, one a line;
   * `null` before any workspace was opened, `''` when it had no lockfile Ariadne knows.
   */
  setup: string | null
  /** The check after edits a thread noted, and which; `null` until one did. */
  check: { command: string; threadId: string } | null
}

/** The check run after a thread's edits: the user's own, else the one a thread noted. */
export function checkCommand(project: {
  settings: ProjectSettings
  detected: ProjectDetected
}): string {
  const { auto, check } = project.settings.workspace
  return auto.check ? (project.detected.check?.command ?? '') : check
}

/**
 * The dependency managers a new workspace is set up for, by the lockfile each leaves at
 * the root of a repository. Installs keep to the lockfile, so the workspace stays clean.
 */
const INSTALLS: { files: string[]; command: string; js?: string }[] = [
  { files: ['bun.lock', 'bun.lockb'], command: 'bun install --frozen-lockfile', js: 'bun' },
  { files: ['pnpm-lock.yaml'], command: 'pnpm install --frozen-lockfile', js: 'pnpm' },
  { files: ['yarn.lock'], command: 'yarn install --frozen-lockfile', js: 'yarn' },
  { files: ['package-lock.json', 'npm-shrinkwrap.json'], command: 'npm ci', js: 'npm' },
  { files: ['uv.lock'], command: 'uv sync --locked' },
  { files: ['poetry.lock'], command: 'poetry install --no-interaction' },
  { files: ['Cargo.lock'], command: 'cargo fetch --locked' },
  { files: ['go.sum'], command: 'go mod download' },
  { files: ['Gemfile.lock'], command: 'bundle install' },
  { files: ['composer.lock'], command: 'composer install --no-interaction' },
]

/**
 * The commands that install a repository's dependencies, from the names of the files at
 * its root; none when it has no lockfile listed above. With lockfiles of several
 * JavaScript package managers, `packageManager` (package.json's field) picks one, and
 * without it none is run: guessing would rewrite the other's lockfile.
 */
export function installCommands(files: readonly string[], packageManager?: string): string[] {
  const names = new Set(files)
  const found = INSTALLS.filter((install) => install.files.some((file) => names.has(file)))
  const js = found.filter((install) => install.js)
  const wanted = packageManager?.trim().split('@')[0]
  const chosen = js.length > 1 ? js.filter((install) => install.js === wanted) : js
  return found
    .filter((install) => !install.js || chosen.includes(install))
    .map((install) =>
      // Yarn 2 and later name the flag differently, and say so with their own config file.
      install.js === 'yarn' && names.has('.yarnrc.yml')
        ? 'yarn install --immutable'
        : install.command,
    )
}
