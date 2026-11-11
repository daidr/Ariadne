import type { ExecOptions, ExecResult, SearchOptions, SourceView, Workspace } from './capabilities'
import { readOnlyView, WorkspacePathError } from './capabilities'
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

/** Of a file larger than this, a search without a program skips it; so is a `.gitignore`. */
const MAX_SEARCHED_BYTES = 1024 * 1024

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
  workspace: Pick<SourceView, 'list' | 'readBytes'>,
  path: string,
  signal?: AbortSignal,
): Promise<{ files: string[]; more: boolean }> {
  const ignored = new Set(SKIPPED)
  const gitignore = await workspace.readBytes('.gitignore', MAX_SEARCHED_BYTES).then(
    (bytes) => new TextDecoder().decode(bytes),
    () => '',
  )
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

/**
 * The workspace's files under `path`, without what git ignores: from git where the
 * workspace is a checkout (tracked and new files alike), else from ripgrep, else by listing
 * directories. `partial` when a listing by directories stopped at its limit.
 */
export async function listFiles(
  workspace: Workspace,
  path: string,
  signal?: AbortSignal,
): Promise<{ files: string[]; partial: boolean }> {
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
  return { files: files.map((file) => file.replace(/\\/g, '/').replace(/^\.\//, '')), partial }
}

/** Matching lines a search shows at most. */
export const MAX_MATCH_LINES = 300

/**
 * Which `file:line:text` lines a search keeps by its `glob`: every one without a glob. The
 * programs that cannot narrow by a glob themselves are narrowed by this after.
 */
export function searchFilter(glob: string | undefined): (line: string) => boolean {
  if (!glob?.trim()) return () => true
  const match = globToRegExp(glob)
  return (line) => match.test(line.slice(0, line.indexOf(':')))
}

/** Matching lines of one file a search keeps at most; ripgrep and grep stop there by themselves. */
export const MAX_FILE_MATCHES = 50

/** At most `MAX_FILE_MATCHES` lines of each file, and `MAX_MATCH_LINES` in all. */
function capped(lines: string[]): string[] {
  const perFile = new Map<string, number>()
  const kept: string[] = []
  for (const line of lines) {
    const file = line.slice(0, line.indexOf(':'))
    const seen = perFile.get(file) ?? 0
    if (seen >= MAX_FILE_MATCHES) continue
    perFile.set(file, seen + 1)
    kept.push(line)
    if (kept.length >= MAX_MATCH_LINES) break
  }
  return kept
}

/**
 * Lines of the workspace matching the regular expression `pattern` under `path`, as
 * `file:line:text`: by ripgrep when the machine has it, else by git where the workspace
 * is a checkout, else grep, else read file by file. None of them goes through a shell.
 * Hidden files (`.github`, `.env.example`) are searched like any other, git's own
 * directory never.
 */
export async function searchLines(
  workspace: Workspace,
  pattern: string,
  path: string,
  searchOptions: SearchOptions = {},
): Promise<{ lines: string[]; error: string | null }> {
  const { signal, glob, ignoreCase } = searchOptions
  const options = { timeoutMs: 30_000, ...(signal ? { signal } : {}) }
  const caseless = ignoreCase ? ['-i'] : []
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
        '--hidden',
        '-g',
        '!.git',
        // A glob of a file's name means the same to ripgrep; one with a directory is taken
        // from the workspace's root below, which ripgrep would read from `path`.
        ...(glob?.trim() && !glob.includes('/') ? ['-g', glob.trim()] : []),
        ...caseless,
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
      [
        'git',
        'grep',
        '-n',
        '-I',
        '-E',
        ...caseless,
        '--untracked',
        '--no-color',
        '-e',
        pattern,
        '--',
        path,
      ],
      options,
    )
  } else {
    result = await execArgv(
      workspace,
      [
        'grep',
        '-rnIE',
        ...caseless,
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
      .map((line) => line.replace(/^\.\//, ''))
      .filter(searchFilter(glob))
    return { lines: capped(lines), error: null }
  }
  return searchByReading(workspace, pattern, path, searchOptions)
}

/**
 * The same search without a program: every file under `path` read and matched line by
 * line. What a view that cannot search by itself falls back to.
 */
export async function searchByReading(
  workspace: Pick<SourceView, 'list' | 'readBytes'>,
  pattern: string,
  path: string,
  { signal, glob, ignoreCase }: SearchOptions = {},
): Promise<{ lines: string[]; error: string | null }> {
  let regex: RegExp
  const keep = searchFilter(glob)
  try {
    regex = new RegExp(pattern, ignoreCase ? 'i' : '')
  } catch (error) {
    return { lines: [], error: error instanceof Error ? error.message : String(error) }
  }
  const lines: string[] = []
  const { files } = await walk(workspace, path, signal)
  for (const file of files) {
    if (lines.length >= MAX_MATCH_LINES || signal?.aborted) break
    if (!keep(`${file}:`)) continue
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

/** The parts of a path as typed, either separator. */
const partsOf = (path: string) => path.split(/[\\/]/).filter(Boolean)

/**
 * A view's path by its spelling alone: relative, never climbing, and not through git's own
 * directory, which may hold credentials in a remote's address.
 */
export function viewPath(path: string): string {
  const parts = partsOf(path || '.')
  if (/^([\\/]|[A-Za-z]:)/.test(path) || parts.includes('..')) throw new WorkspacePathError(path)
  if (parts.some((part) => part.toLowerCase() === '.git')) {
    throw new WorkspacePathError(path, 'is kept out of reach')
  }
  return path || '.'
}

/** How a view checks paths beyond their spelling. */
export interface ViewChecks {
  /** Refuses a path to look under that the workspace keeps out of reach: links, hidden paths. */
  check?: (path: string) => Promise<unknown>
  /** The same for each file found, where asking is cheap enough to do for every one. */
  checkFound?: (path: string) => Promise<unknown>
  ref?: string
}

/**
 * A workspace to look at without changing it, searched with the machine's own programs:
 * what a source that is a directory hands out as its view. Finding and searching run
 * programs on the path, so it is checked first as reading checks it, by its spelling and
 * by the workspace's own resolving; what they found is checked too.
 */
export function searchableView(workspace: Workspace, checks: ViewChecks = {}): SourceView {
  const inside = async (path: string) => {
    await checks.check?.(viewPath(path))
    return path || '.'
  }
  const shown = async (file: string) => {
    try {
      viewPath(file)
      await checks.checkFound?.(file)
      return true
    } catch {
      return false
    }
  }
  return readOnlyView(
    {
      readBytes: (path, maxBytes) => workspace.readBytes(path, maxBytes),
      list: (path) => workspace.list(path),
      async files(path, options) {
        const found = await listFiles(workspace, await inside(path), options?.signal)
        const kept = await Promise.all(found.files.map(shown))
        return { ...found, files: found.files.filter((_, at) => kept[at]) }
      },
      async search(pattern, path, options) {
        const found = await searchLines(workspace, pattern, await inside(path), options)
        const seen = new Map<string, Promise<boolean>>()
        const kept = await Promise.all(
          found.lines.map((line) => {
            const file = line.slice(0, line.indexOf(':'))
            if (!seen.has(file)) seen.set(file, shown(file))
            return seen.get(file)!
          }),
        )
        return { ...found, lines: found.lines.filter((_, at) => kept[at]) }
      },
    },
    checks.ref,
  )
}

/** A view's path in parts, checked by its spelling. */
const plain = (path: string) => partsOf(viewPath(path)).filter((part) => part !== '.')

/**
 * One file of a directory and nothing else of it: what a source that is a single file (a
 * log, a dump) hands out as its view. The file is listed at the root under its own name
 * and read by that name; every other path is outside.
 */
export function singleFileView(
  directory: Pick<SourceView, 'readBytes'>,
  name: string,
  ref?: string,
): SourceView {
  return readOnlyView(
    {
      async readBytes(path, maxBytes) {
        const parts = plain(path)
        if (parts.length !== 1 || parts[0] !== name) throw new WorkspacePathError(path)
        return directory.readBytes(name, maxBytes)
      },
      async list(path) {
        if (plain(path).length) throw new WorkspacePathError(path)
        return [{ name, kind: 'file' as const }]
      },
    },
    ref,
  )
}
