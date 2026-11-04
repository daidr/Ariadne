/**
 * What the user may tune about how a conversation works, each with the value Ariadne uses
 * when left alone. Timeouts are in seconds, the output in characters.
 */
export interface Limits {
  /** How long a thread's command may run when it does not say. */
  commandTimeout: number
  /** The longest a command may ask for. */
  commandTimeoutMax: number
  /** What run_command hands the model of a command's output; the rest goes to a file. */
  commandOutput: number
  /** Share of the model's context at which older history makes room. */
  compactShare: number
  /** How often a passing model error (rate limit, overload, a cut connection) is retried. */
  modelRetries: number
  /** How often the same step may come back in a turn before the turn stops. */
  loopRepeats: number
}

export const DEFAULT_LIMITS: Readonly<Limits> = {
  commandTimeout: 120,
  commandTimeoutMax: 600,
  commandOutput: 30_000,
  compactShare: 0.6,
  modelRetries: 4,
  loopRepeats: 4,
}

/** The range each limit takes; outside it a model would not get far, or not stop. */
export const LIMIT_RANGES: Readonly<Record<keyof Limits, readonly [number, number]>> = {
  commandTimeout: [5, 3600],
  commandTimeoutMax: [5, 7200],
  commandOutput: [2000, 200_000],
  compactShare: [0.3, 0.9],
  modelRetries: [0, 10],
  loopRepeats: [2, 20],
}

/** What `run_command` does with a command, by the user's rules. */
export type CommandAction = 'allow' | 'ask' | 'deny'
export const COMMAND_ACTIONS: CommandAction[] = ['allow', 'ask', 'deny']

/**
 * A pattern a command is matched against, whole and trimmed: `*` stands for anything,
 * including nothing; the rest is taken as written. A command made of several (`a && b`,
 * `a; b`, `a | b`, one per line) matches when any of its parts does.
 */
export interface CommandRule {
  pattern: string
  action: CommandAction
}

function globTest(pattern: string, text: string): boolean {
  const source = pattern
    .trim()
    .split('*')
    .map((part) => part.replace(/[.+?^${}()|[\]\\]/g, '\\$&'))
    .join('[\\s\\S]*')
  return new RegExp(`^${source}$`).test(text)
}

/**
 * The parts a command line is made of, split at `&&`, `||`, `;`, `|`, `&` and line breaks
 * outside quotes, each trimmed. Good enough to find `git push` inside `make && git push`;
 * the sandbox, not this, is what keeps a command in the workspace.
 */
export function commandParts(command: string): string[] {
  const parts: string[] = []
  let current = ''
  let quote: '"' | "'" | null = null
  for (let i = 0; i < command.length; i++) {
    const char = command[i]!
    if (quote) {
      current += char
      if (char === quote) quote = null
      else if (char === '\\' && quote === '"' && i + 1 < command.length) current += command[++i]!
      continue
    }
    if (char === '"' || char === "'") {
      quote = char
      current += char
      continue
    }
    if (char === '\\' && i + 1 < command.length) {
      current += char + command[++i]!
      continue
    }
    if (char === ';' || char === '\n' || char === '|' || char === '&') {
      if (current.trim()) parts.push(current.trim())
      current = ''
      // `&&` and `||` are one separator.
      if ((char === '&' || char === '|') && command[i + 1] === char) i++
      continue
    }
    current += char
  }
  if (current.trim()) parts.push(current.trim())
  return parts
}

/**
 * What to do with `command`: a `deny` rule that matches wins, then an `allow` (so a
 * narrower allow can lift a wide ask), then an `ask`. No rule that matches allows it.
 */
export function commandVerdict(
  command: string,
  rules: readonly CommandRule[],
): { action: CommandAction; pattern: string | null } {
  const candidates = [command.trim(), ...commandParts(command)]
  const matching = (action: CommandAction) =>
    rules.find(
      (rule) => rule.action === action && candidates.some((part) => globTest(rule.pattern, part)),
    )
  for (const action of ['deny', 'allow', 'ask'] as const) {
    const rule = matching(action)
    if (rule) return { action, pattern: rule.pattern }
  }
  return { action: 'allow', pattern: null }
}

/**
 * How a project's conversations are set up beyond the model: the user's limits, the
 * instructions they gave every project, their command rules, and the environment
 * variables of the project's commands (which may hold secrets).
 */
export interface TurnSettings {
  limits: Limits
  /** The user's own instructions for every project, e.g. how they like answers. */
  instructions: string
  commandRules: CommandRule[]
  /** Added to the environment of the project's thread commands and its setup script. */
  env: Record<string, string>
}

export const defaultTurnSettings = (): TurnSettings => ({
  limits: { ...DEFAULT_LIMITS },
  instructions: '',
  commandRules: [],
  env: {},
})
