import type { ExecOptions, ExecResult, Workspace } from '@ariadne/plugin-sdk'
import { globToRegExp } from './glob'

/** Quote one argument for a POSIX shell. */
const posixQuote = (value: string) => `'${value.replaceAll("'", `'\\''`)}'`

/**
 * Run a program with its arguments in the workspace, without a shell where the workspace
 * can: the same on Windows, whatever shell the project uses.
 */
export function execArgv(
  workspace: Workspace,
  argv: string[],
  options: Omit<ExecOptions, 'shell'> = {},
): Promise<ExecResult> {
  if (workspace.execFile) return workspace.execFile(argv, options)
  return workspace.exec(argv.map(posixQuote).join(' '), { ...options, shell: ['bash', '-c'] })
}

/** What each workspace was found to have, asked once. */
const probes = new WeakMap<Workspace, Map<string, Promise<boolean>>>()

function probe(workspace: Workspace, key: string, argv: string[]): Promise<boolean> {
  let known = probes.get(workspace)
  if (!known) probes.set(workspace, (known = new Map()))
  let answer = known.get(key)
  if (!answer) {
    answer = execArgv(workspace, argv, { timeoutMs: 10_000 }).then(
      (result) => result.exitCode === 0,
      () => false,
    )
    known.set(key, answer)
  }
  return answer
}

/** Whether ripgrep is on the workspace's machine. */
export const hasRipgrep = (workspace: Workspace) => probe(workspace, 'rg', ['rg', '--version'])

/** Whether the workspace is inside a git checkout, so git knows what it ignores. */
export const inGit = (workspace: Workspace) =>
  probe(workspace, 'git', ['git', 'rev-parse', '--is-inside-work-tree'])

/** Directories a walk without git skips: what nobody searches on purpose. */
const SKIPPED = new Set(['.git', 'node_modules', '.ariadne'])
/** Files a walk without git looks at, at most. */
const MAX_WALKED = 20_000

/**
 * The files under `path`, relative to the workspace root, by listing directories: for a
 * workspace that is no git checkout. Skips `SKIPPED` and the plain names in the root's
 * `.gitignore`. `more` when it stopped at `MAX_WALKED`.
 */
export async function walk(
  workspace: Pick<Workspace, 'list' | 'readFile'>,
  path: string,
  signal?: AbortSignal,
): Promise<{ files: string[]; more: boolean }> {
  const ignored = new Set(SKIPPED)
  const gitignore = await workspace.readFile('.gitignore').catch(() => '')
  for (const line of gitignore.split('\n')) {
    const name = line.trim().replace(/^\//, '').replace(/\/$/, '')
    if (name && !/[*?[!#/]/.test(name)) ignored.add(name)
  }
  const root = path
    .replace(/\\/g, '/')
    .replace(/^\.\/?/, '')
    .replace(/\/$/, '')
  const files: string[] = []
  const dirs = [root]
  while (dirs.length) {
    if (signal?.aborted) break
    const dir = dirs.shift()!
    let entries
    try {
      entries = await workspace.list(dir || '.')
    } catch {
      // A file given as the path, or a directory that went meanwhile.
      if (dir === root && root) files.push(root)
      continue
    }
    for (const entry of entries) {
      if (ignored.has(entry.name)) continue
      const full = dir ? `${dir}/${entry.name}` : entry.name
      if (entry.kind === 'directory') dirs.push(full)
      else files.push(full)
      if (files.length >= MAX_WALKED) return { files, more: true }
    }
  }
  return { files, more: false }
}

/** Paths found at most, before the list says how many more there are. */
export const MAX_FOUND = 500

/**
 * The workspace's files matching `pattern` under `path`, sorted, without what git ignores:
 * from git where the workspace is a checkout (tracked and new files alike), else from
 * ripgrep, else by listing directories.
 */
export async function findFiles(
  workspace: Workspace,
  pattern: string,
  path: string,
  signal?: AbortSignal,
): Promise<{ found: string[]; total: number; partial: boolean }> {
  const match = globToRegExp(pattern)
  let files: string[] | null = null
  let partial = false
  const options = { timeoutMs: 60_000, ...(signal ? { signal } : {}) }
  if (await inGit(workspace)) {
    const result = await execArgv(
      workspace,
      ['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard', '--', path],
      options,
    )
    if (result.exitCode === 0) files = [...new Set(result.stdout.split('\0').filter(Boolean))]
  }
  if (!files && (await hasRipgrep(workspace))) {
    const result = await execArgv(workspace, ['rg', '--files', '--', path], options)
    // 1 is "no files": nothing there, not a failure.
    if (result.exitCode === 0 || result.exitCode === 1) {
      files = result.stdout
        .split('\n')
        .map((line) => line.trim())
        .filter(Boolean)
    }
  }
  if (!files) {
    const walked = await walk(workspace, path, signal)
    files = walked.files
    partial = walked.more
  }
  const found = files
    .map((file) => file.replace(/\\/g, '/').replace(/^\.\//, ''))
    .filter((file) => match.test(file))
    .toSorted()
  return { found: found.slice(0, MAX_FOUND), total: found.length, partial }
}

/** Matching lines a search shows at most. */
export const MAX_MATCH_LINES = 300
/** Of a file larger than this, a search without a program skips it. */
const MAX_SEARCHED_BYTES = 1024 * 1024

/**
 * Lines of the workspace matching the regular expression `pattern` under `path`, as
 * `file:line:text`: by ripgrep when the machine has it, else by git where the workspace
 * is a checkout, else grep, else read file by file. None of them goes through a shell.
 */
export async function searchLines(
  workspace: Workspace,
  pattern: string,
  path: string,
  signal?: AbortSignal,
): Promise<{ lines: string[]; error: string | null }> {
  const options = { timeoutMs: 30_000, ...(signal ? { signal } : {}) }
  let result: ExecResult | null = null
  if (await hasRipgrep(workspace)) {
    result = await execArgv(
      workspace,
      [
        'rg',
        '-n',
        '--no-heading',
        '--color',
        'never',
        '--max-count',
        '50',
        '-e',
        pattern,
        '--',
        path,
      ],
      options,
    )
  } else if (await inGit(workspace)) {
    result = await execArgv(
      workspace,
      ['git', 'grep', '-n', '-I', '-E', '--untracked', '--no-color', '-e', pattern, '--', path],
      options,
    )
  } else {
    result = await execArgv(
      workspace,
      [
        'grep',
        '-rnIE',
        '--exclude-dir=.git',
        '--exclude-dir=node_modules',
        '-m',
        '50',
        '-e',
        pattern,
        '--',
        path,
      ],
      options,
    )
    // No grep either (Windows without git or ripgrep): look file by file.
    if (result.exitCode === 127) result = null
  }
  if (result) {
    // 1 is "nothing matched" for all three.
    if (result.exitCode !== 0 && result.exitCode !== 1) {
      return { lines: [], error: result.stderr.trim() || `exit code ${result.exitCode}` }
    }
    const lines = result.stdout
      .split('\n')
      .filter(Boolean)
      .slice(0, MAX_MATCH_LINES)
      .map((line) => line.replace(/^\.\//, ''))
    return { lines, error: null }
  }
  let regex: RegExp
  try {
    regex = new RegExp(pattern)
  } catch (error) {
    return { lines: [], error: error instanceof Error ? error.message : String(error) }
  }
  const lines: string[] = []
  const { files } = await walk(workspace, path, signal)
  for (const file of files) {
    if (lines.length >= MAX_MATCH_LINES || signal?.aborted) break
    let data: Uint8Array
    try {
      data = await workspace.readBytes(file, MAX_SEARCHED_BYTES)
    } catch {
      continue
    }
    if (data.includes(0)) continue
    let inFile = 0
    for (const [i, line] of new TextDecoder().decode(data).split('\n').entries()) {
      if (!regex.test(line)) continue
      lines.push(`${file}:${i + 1}:${line.replace(/\r$/, '')}`)
      if (++inFile >= 50 || lines.length >= MAX_MATCH_LINES) break
    }
  }
  return { lines, error: null }
}
