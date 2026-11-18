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

/**
 * What the user set for one project beyond its source: words every conversation of it
 * follows, how its threads' workspaces are set up, the built-in tools it goes without, and
 * how many tokens it may spend. Secrets (environment variables) are kept in the user's
 * settings file, not here.
 */
export interface ProjectSettings {
  /** Instructions for this project that are not in its repository, for every conversation. */
  instructions: string
  /** How work is handed over; completion does not require merging its branch. */
  delivery: { mode: 'merge' | 'external'; instructions: string }
  /** Groups of built-in tools its conversations do not get, e.g. `fetch` for fetch_url. */
  tools: string[]
  /** Tokens (read and written) one thread, and the whole project, may use; `null` is no cap. */
  budget: { thread: number | null; project: number | null }
  workspace: {
    /** Automatic cleanup, only after integration, or only on an explicit user request. */
    retention: 'auto' | 'merged' | 'keep'
    /**
     * What is left to Ariadne. `setup`: a new workspace is set up with the command the
     * first thread to install the repository's dependencies noted, and nothing before.
     * `check`: the check after edits is the one the first thread to work in the repository
     * noted. Either is off once the user wrote their own, or emptied the field.
     */
    auto: { setup: boolean; check: boolean }
    /**
     * The user's own, in place of the one a thread noted: run in a thread's workspace once
     * it is new; `''` runs nothing. Not read while `auto.setup`.
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

export const DEFAULT_BRANCH_PREFIX = 'ariadne/'

export const MAX_PROJECT_INSTRUCTIONS = 16_000

export const MAX_SETUP_SCRIPT = 8000

export const MAX_CHECK_COMMAND = 2000

/**
 * What Ariadne found out about a project's repository by itself, shown in the settings in
 * place of what the user would otherwise have to write. A new source starts it over.
 */
export interface ProjectDetected {
  /**
   * The setup of a new workspace a thread noted once it had installed what the work needs,
   * and which; `null` until one did, `''` as its command when the repository needs none.
   */
  setup: { command: string; threadId: string } | null
  /** The check after edits a thread noted, and which; `null` until one did. */
  check: { command: string; threadId: string } | null
}
