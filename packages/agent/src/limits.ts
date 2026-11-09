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
 * `FOO=1 git -C . push` matches `git push*`, and so do `bash -c 'git push'` and
 * `timeout 10 git push` (see `commandVerdict`).
 */
export interface CommandRule {
  pattern: string
  action: CommandAction
}

/**
 * The rules a fresh install starts with, the user's to change or remove like any other:
 * commands whose effect leaves the machine and cannot be taken back, and that a thread's
 * own work never needs. Pushing is Ariadne's or the user's, publishing a package takes its
 * version for good, and merging or releasing with `gh` goes around the review.
 */
export const DEFAULT_COMMAND_RULES: readonly CommandRule[] = [
  'git push*',
  'npm publish*',
  'pnpm publish*',
  'yarn publish*',
  'bun publish*',
  'cargo publish*',
  'gh pr merge*',
  'gh release create*',
].map((pattern) => ({ pattern, action: 'ask' }))

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
/**
 * Programs whose arguments are text they print or look for, never a command they run. A
 * phrase inside them (`rg "git push"`, `echo npm publish`) is no reason to ask.
 */
const TEXT_PROGRAMS = new Set([
  'echo',
  'printf',
  'grep',
  'egrep',
  'fgrep',
  'rg',
  'ag',
  'ack',
  'findstr',
  'select-string',
  'sls',
  'write-output',
  'write-host',
])
/** git's subcommands that take a message or a pattern and run nothing of it. */
const GIT_TEXT = new Set(['commit', 'tag', 'log', 'grep', 'notes', 'stash'])
/** Programs that run text they are handed, also through a pipe (`echo … | sh`). */
const TEXT_RUNNERS = new Set(['eval', 'source', '.', 'xargs', 'iex', 'invoke-expression'])
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

/** A word's program name: without its directory (`/usr/bin/git`) or `.exe`. */
const programOf = (word: string) =>
  word
    .split(/[\\/]/)
    .pop()!
    .replace(/\.exe$/i, '')

/** `git …` without git's own options before its subcommand: `git -C dir push` → `git push`. */
function gitForm(words: string[]): string {
  const rest = words.slice(1)
  while (rest[0]?.startsWith('-')) {
    const option = rest.shift()!
    if (GIT_VALUED.has(option)) rest.shift()
  }
  return ['git', ...rest].join(' ')
}

/**
 * One part of a command line as rules see it. `forms` are what the part runs, spelled the
 * ways an `allow` may name it: as written, and without leading variable assignments
 * (`FOO=1 cmd`), wrappers (`env`, `nohup`, `sudo`, …), a program's directory, or git's own
 * options (`git -C dir push`). `loose` are the part from each later word on, each at its
 * offset in `forms[1]`: whatever runs another command (`then`, `{`, `eval`, `xargs`,
 * `timeout 10`, `sudo -u root`, …) leaves that command as one of them, so `deny` and `ask`
 * rules are tried on all of them too. `nested` are the command lines inside the part: what
 * a shell is handed (`bash -c '…'`, `cmd /c …`) and every word with a space in it, which
 * may be one (`eval "git push"`, `bash -ce "…"`, `pwsh -Com "…"`).
 */
interface RulePart {
  forms: string[]
  loose: { text: string; offset: number }[]
  nested: string[]
  /**
   * The part's program only takes text (`TEXT_PROGRAMS`, `GIT_TEXT`): `ask` rules look at
   * what it runs and not at what its words mention.
   */
  text?: boolean
  /** Read out of the words of such a part: it runs nothing. */
  quoted?: boolean
  /** The part runs text it is handed: a shell, `eval`, `xargs`. */
  runner?: boolean
}

function rulePart(command: SimpleCommand): RulePart {
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
  const nested = command.words.filter((word) => /\s/.test(word))
  if (!words.length) return { forms: found, loose: [], nested }
  const program = programOf(words[0]!)
  words = [program, ...words.slice(1)]
  found.push(words.join(' '))
  if (program === 'git') found.push(gitForm(words))
  const loose: RulePart['loose'] = []
  let offset = 0
  for (let k = 1; k < words.length; k++) {
    offset += words[k - 1]!.length + 1
    const rest = [programOf(words[k]!), ...words.slice(k + 1)]
    loose.push({ text: rest.join(' '), offset })
    if (rest[0] === 'git') loose.push({ text: gitForm(rest), offset })
  }
  const name = program.toLowerCase()
  const text =
    TEXT_PROGRAMS.has(name) || (name === 'git' && GIT_TEXT.has(gitForm(words).split(' ')[1] ?? ''))
  // Any word may be the program that runs text, behind a word that is not known to wrap
  // one (`timeout 5 sh`, `busybox sh`): on the side of asking, as `loose` is.
  // A later `.` is the directory, not the shell's `source`.
  const runner = words.some((word, at) => {
    const runs = programOf(word).toLowerCase()
    if (at > 0 && runs === '.') return false
    return SHELL_PROGRAMS.has(runs) || TEXT_RUNNERS.has(runs) || runs === 'cmd'
  })
  if (SHELL_PROGRAMS.has(name)) {
    const at = words.findIndex((word, i) => i > 0 && /^-[a-z]*c[a-z]*$|^-com/i.test(word))
    if (at > 0 && words.length > at + 1) nested.push(words.slice(at + 1).join(' '))
    // PowerShell's `-EncodedCommand` (any prefix from `-e`) takes base64 of UTF-16LE.
    const encoded = words.findIndex(
      (word, i) => i > 0 && /^-e(n(c(o(d(e(d(c(o(m(m(a(n(d)?)?)?)?)?)?)?)?)?)?)?)?)?$/i.test(word),
    )
    const value = encoded > 0 ? words[encoded + 1] : undefined
    if (value) nested.push(Buffer.from(value, 'base64').toString('utf16le'))
  } else if (name === 'cmd') {
    const at = words.findIndex((word) => /^\/[ck]$/i.test(word))
    if (at > 0 && words.length > at + 1) nested.push(words.slice(at + 1).join(' '))
  }
  return { forms: found, loose, nested, ...(text ? { text } : {}), ...(runner ? { runner } : {}) }
}

/**
 * The parts of a command line: every simple command, and those of the command lines inside
 * them. PowerShell takes a backquote for an escape (``g`i`t`` is `git`), so a line with
 * backquotes is read once more without them.
 */
function ruleParts(command: string, depth = 0, quoted = false): RulePart[] {
  const lines =
    command.includes('`') && depth === 0 ? [command, command.replaceAll('`', '')] : [command]
  return lines.flatMap((line) =>
    simpleCommands(line).flatMap((simple) => {
      const own = rulePart(simple)
      // What a text-only part quotes is text all the way down.
      const part = quoted ? { ...own, quoted } : own
      return [
        part,
        ...(depth < 4
          ? part.nested.flatMap((inner) => ruleParts(inner, depth + 1, quoted || !!own.text))
          : []),
      ]
    }),
  )
}

/**
 * The simple commands a command line is made of, each as written and trimmed: split at
 * `&&`, `||`, `;`, `|`, `&`, parentheses and line breaks outside quotes, not at a
 * redirection such as `2>&1`. Good enough to find `git push` inside `make && git push`;
 * it does not keep a command in the workspace.
 */
export function commandParts(command: string): string[] {
  return simpleCommands(command).map((part) => part.raw)
}

/** How much of a command an `allow` pattern spells out before its first `*`. */
const spelled = (pattern: string) => pattern.trim().split('*')[0]!.length

/**
 * What to do with `command`, part by part (see `RulePart`). A part is denied when a `deny`
 * rule matches any way of reading it. Otherwise an `allow` that matches what it runs wins,
 * so a narrower allow can lift a wide ask, unless an `ask` matches a command further along
 * than the allow spells out: `allow timeout *` does not lift `ask git push*` from
 * `timeout 10 git push`. Otherwise an `ask` that matches any way of reading it asks. The
 * whole line counts as a part too. The command is denied when any part is, asked about
 * when any part is, and else allowed. No rule that matches allows it. This errs towards
 * asking, with one exception: text a command only prints or looks for (a commit message, a
 * search pattern, `echo`) is not asked about, unless the line also has something that
 * runs text (a shell, `eval`, `xargs`). `deny` rules make no such exception.
 */
export function commandVerdict(
  command: string,
  rules: readonly CommandRule[],
): { action: CommandAction; pattern: string | null } {
  const whole: RulePart = { forms: [command.trim()], loose: [], nested: [] }
  const parts = [whole, ...ruleParts(command)]
  const matching = (action: CommandAction, texts: string[]) =>
    rules.find((r) => r.action === action && texts.some((text) => globTest(r.pattern, text)))
  const runsText = parts.some((part) => part.runner)
  const verdicts = parts.map((part) => {
    const every = [...part.forms, ...part.loose.map((loose) => loose.text)]
    const mentioned = (!!part.text || !!part.quoted) && !runsText
    const deny = matching('deny', every)
    if (deny) return { action: 'deny' as const, pattern: deny.pattern }
    // The allow that spells out most of the command, should several match.
    const allow = rules
      .filter((r) => r.action === 'allow' && part.forms.some((text) => globTest(r.pattern, text)))
      .toSorted((a, b) => spelled(b.pattern) - spelled(a.pattern))[0]
    if (allow) {
      const reach = spelled(allow.pattern)
      const beyond = part.loose.filter((loose) => loose.offset >= reach).map((loose) => loose.text)
      const ask = mentioned ? undefined : matching('ask', beyond)
      return ask
        ? { action: 'ask' as const, pattern: ask.pattern }
        : { action: 'allow' as const, pattern: allow.pattern }
    }
    // A part read out of quoted text runs nothing itself; one that quotes runs its own form.
    const ask = matching('ask', mentioned ? (part.quoted ? [] : part.forms) : every)
    return ask ? { action: 'ask' as const, pattern: ask.pattern } : null
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
