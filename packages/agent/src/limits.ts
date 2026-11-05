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
 * `a; b`, `a | b`, one per line) is matched part by part, each also as what it runs:
 * `FOO=1 git -C . push` matches `git push*`, and so does `bash -c 'git push'`.
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

/** One simple command of a command line: as written, and its words with quotes undone. */
interface SimpleCommand {
  raw: string
  words: string[]
}

/** A shell's own words that run what follows them; they say nothing of what runs. */
const WRAPPERS = new Set(['command', 'exec', 'nohup', 'time', 'builtin', 'nice'])
/** Shells whose `-c` (or PowerShell's `-Command`) takes a command line to read too. */
const SHELL_PROGRAMS = new Set(['bash', 'sh', 'zsh', 'dash', 'ksh', 'fish', 'pwsh', 'powershell'])
/** git's options before its subcommand that take the next word as their value. */
const GIT_VALUED = new Set(['-C', '-c', '--git-dir', '--work-tree', '--namespace', '--exec-path'])

/**
 * Split a command line into its simple commands, the way a POSIX shell reads it closely
 * enough for rules: at `;`, `&&`, `||`, `|`, `&`, line breaks and parentheses outside
 * quotes, with redirections (`2>&1`, `> out.txt`, `&> log`) set aside rather than taken
 * for separators. Command substitutions (`$(…)`, backquotes) are read as commands too.
 */
function simpleCommands(line: string, depth = 0): SimpleCommand[] {
  const out: SimpleCommand[] = []
  let raw = ''
  let words: string[] = []
  let word: string | null = null
  /** The next word is a redirection's target, not part of the command. */
  let target = false
  const endWord = () => {
    if (word === null) return
    if (target) target = false
    else words.push(word)
    word = null
  }
  const endCommand = () => {
    endWord()
    if (raw.trim() || words.length) out.push({ raw: raw.trim(), words })
    raw = ''
    words = []
    target = false
  }
  const inner = (text: string) => {
    if (depth < 4) out.push(...simpleCommands(text, depth + 1))
  }
  /** Where the `$(` at `i` closes, counting nested parentheses. */
  const closing = (i: number) => {
    let open = 0
    for (let j = i; j < line.length; j++) {
      if (line[j] === '(') open++
      else if (line[j] === ')' && --open === 0) return j
    }
    return line.length
  }
  for (let i = 0; i < line.length; i++) {
    const char = line[i]!
    if (char === "'") {
      const end = line.indexOf("'", i + 1)
      const stop = end === -1 ? line.length : end
      word = (word ?? '') + line.slice(i + 1, stop)
      raw += line.slice(i, stop + 1)
      i = stop
      continue
    }
    if (char === '"') {
      let value = ''
      let j = i + 1
      for (; j < line.length && line[j] !== '"'; j++) {
        if (line[j] === '\\' && j + 1 < line.length) value += line[++j]
        else if (line[j] === '$' && line[j + 1] === '(') {
          const close = closing(j + 1)
          inner(line.slice(j + 2, close))
          value += line.slice(j, close + 1)
          j = close
        } else value += line[j]
      }
      word = (word ?? '') + value
      raw += line.slice(i, j + 1)
      i = j
      continue
    }
    if (char === '\\' && i + 1 < line.length) {
      if (line[i + 1] !== '\n') word = (word ?? '') + line[i + 1]
      raw += char + line[++i]
      continue
    }
    if (char === '$' && line[i + 1] === '(') {
      const close = closing(i + 1)
      inner(line.slice(i + 2, close))
      word = (word ?? '') + line.slice(i, close + 1)
      raw += line.slice(i, close + 1)
      i = close
      continue
    }
    if (char === '`') {
      const end = line.indexOf('`', i + 1)
      const stop = end === -1 ? line.length : end
      inner(line.slice(i + 1, stop))
      word = (word ?? '') + line.slice(i, stop + 1)
      raw += line.slice(i, stop + 1)
      i = stop
      continue
    }
    // A redirection: `>`, `>>`, `<`, `2>`, `>&2`, `2>&1`, `&>`, `&>>`. Its target goes too.
    const fd = word !== null && /^\d+$/.test(word)
    if (char === '>' || char === '<' || (char === '&' && line[i + 1] === '>')) {
      if (fd) word = null
      else endWord()
      let j = i + 1
      while (line[j] === '>' || line[j] === '<' || line[j] === '|') j++
      if (line[j] === '&' && /[\d-]/.test(line[j + 1] ?? '')) {
        j += 2
        while (/\d/.test(line[j] ?? '')) j++
      } else target = true
      raw += line.slice(i, j)
      i = j - 1
      continue
    }
    if (
      char === ';' ||
      char === '\n' ||
      char === '|' ||
      char === '&' ||
      char === '(' ||
      char === ')'
    ) {
      endCommand()
      // `&&`, `||` and `|&` are one separator.
      if ((char === '&' || char === '|') && (line[i + 1] === char || line[i + 1] === '&')) i++
      continue
    }
    if (char === ' ' || char === '\t') {
      endWord()
      raw += char
      continue
    }
    if (char === '#' && word === null && /(^|[\s;&|()])$/.test(raw.slice(-1) || ' ')) {
      // A comment, to the end of the line.
      while (i + 1 < line.length && line[i + 1] !== '\n') i++
      continue
    }
    word = (word ?? '') + char
    raw += char
  }
  endCommand()
  return out
}

/**
 * The forms a simple command is matched in: as written, and as what it runs, without
 * leading variable assignments (`FOO=1 cmd`), wrappers (`env`, `nohup`, `sudo`, …), a
 * program's directory (`/usr/bin/git`), or git's own options before its subcommand
 * (`git -C dir push`). `nested` is the command line a shell is handed (`bash -c '…'`,
 * `pwsh -Command …`, `cmd /c …`), whose commands are parts of their own.
 */
function forms(command: SimpleCommand): { forms: string[]; nested: string | null } {
  const found = [command.raw]
  let words = [...command.words]
  for (;;) {
    const first = words[0]
    if (first === undefined) break
    if (/^[A-Za-z_][A-Za-z0-9_]*=/.test(first)) words = words.slice(1)
    else if (first === 'env') {
      words = words.slice(1)
      while (words[0] && (words[0].startsWith('-') || /^[A-Za-z_][A-Za-z0-9_]*=/.test(words[0]))) {
        words = words.slice(1)
      }
    } else if (first === 'sudo' || first === 'doas') {
      words = words.slice(1)
      while (words[0]?.startsWith('-')) words = words.slice(1)
    } else if (WRAPPERS.has(first)) words = words.slice(1)
    else break
  }
  if (!words.length) return { forms: found, nested: null }
  const program = words[0]!
    .split(/[\\/]/)
    .pop()!
    .replace(/\.exe$/i, '')
  words = [program, ...words.slice(1)]
  found.push(words.join(' '))
  if (program === 'git') {
    const rest = words.slice(1)
    while (rest[0]?.startsWith('-')) {
      const option = rest.shift()!
      if (GIT_VALUED.has(option)) rest.shift()
    }
    found.push(['git', ...rest].join(' '))
  }
  let nested: string | null = null
  const name = program.toLowerCase()
  if (SHELL_PROGRAMS.has(name)) {
    const at = words.findIndex(
      (word, i) => i > 0 && (/^-[a-z]*c$/.test(word) || /^-c(ommand)?$/i.test(word)),
    )
    nested = (at > 0 && words.slice(at + 1).join(' ')) || null
  } else if (name === 'cmd') {
    const at = words.findIndex((word) => /^\/[ck]$/i.test(word))
    nested = (at > 0 && words.slice(at + 1).join(' ')) || null
  }
  return { forms: found, nested }
}

/**
 * The parts of a command line, each the forms it is matched in: every simple command,
 * and those of the command lines handed to a shell inside it.
 */
function commandForms(command: string, depth = 0): string[][] {
  return simpleCommands(command).flatMap((part) => {
    const { forms: own, nested } = forms(part)
    return [own, ...(nested && depth < 4 ? commandForms(nested, depth + 1) : [])]
  })
}

/**
 * The simple commands a command line is made of, each as written and trimmed: split at
 * `&&`, `||`, `;`, `|`, `&`, parentheses and line breaks outside quotes, not at a
 * redirection such as `2>&1`. Good enough to find `git push` inside `make && git push`;
 * the sandbox, not this, is what keeps a command in the workspace.
 */
export function commandParts(command: string): string[] {
  return simpleCommands(command).map((part) => part.raw)
}

/**
 * What to do with `command`, part by part: within one part a `deny` rule that matches
 * wins, then an `allow` (so a narrower allow can lift a wide ask), then an `ask`; a part
 * is matched as written and as what it runs (see `forms`). The whole line counts as a
 * part too. The command is denied when any part is, asked about when any part is, and
 * else allowed. No rule that matches allows it.
 */
export function commandVerdict(
  command: string,
  rules: readonly CommandRule[],
): { action: CommandAction; pattern: string | null } {
  const parts = [[command.trim()], ...commandForms(command)]
  const verdicts = parts.map((candidates) => {
    for (const action of ['deny', 'allow', 'ask'] as const) {
      const rule = rules.find(
        (r) => r.action === action && candidates.some((text) => globTest(r.pattern, text)),
      )
      if (rule) return { action, pattern: rule.pattern }
    }
    return null
  })
  for (const action of ['deny', 'ask', 'allow'] as const) {
    const found = verdicts.find((verdict) => verdict?.action === action)
    if (found) return found
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
