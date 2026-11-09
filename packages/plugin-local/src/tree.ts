import {
  FileTooLargeError,
  MAX_MATCH_LINES,
  WorkspacePathError,
  type SourceView,
} from '@ariadne/plugin-sdk'
import type { GitRun } from './branches'

/**
 * A path inside the tree, as git names it: `/`-separated, no `.` or empty parts. One that
 * climbs out (`..`) or starts at a root is refused.
 */
export function treePath(path: string): string {
  if (/^([a-zA-Z]:)?[\\/]/.test(path)) throw new WorkspacePathError(path)
  const parts = path.split(/[\\/]/).filter((part) => part && part !== '.')
  if (parts.includes('..')) throw new WorkspacePathError(path)
  return parts.join('/')
}

/**
 * The files of one commit of a repository, read with git without a checkout: what the
 * coordinator sees of a hosted repository's default branch. `bytes` runs git and gives its
 * output as it is, for files that are not text.
 */
export class GitTreeView implements SourceView {
  constructor(
    private readonly git: GitRun,
    private readonly bytes: (args: string[]) => Promise<Uint8Array>,
    /** The commit, e.g. `refs/remotes/origin/main`. */
    private readonly rev: string,
    readonly ref: string,
  ) {}

  private spec(path: string): string {
    return `${this.rev}:${treePath(path)}`
  }

  private async type(path: string): Promise<string> {
    const result = await this.git(['cat-file', '-t', this.spec(path)])
    if (result.exitCode !== 0) throw new Error(`${path || '.'} does not exist in ${this.ref}`)
    return result.stdout.trim()
  }

  private async blob(path: string): Promise<void> {
    if ((await this.type(path)) !== 'blob') throw new Error(`${path} is not a file`)
  }

  async readBytes(path: string, maxBytes: number): Promise<Uint8Array> {
    await this.blob(path)
    const size = Number.parseInt(
      (await this.git(['cat-file', '-s', this.spec(path)])).stdout.trim(),
      10,
    )
    if (Number.isFinite(size) && size > maxBytes) throw new FileTooLargeError(path, size, maxBytes)
    const data = await this.bytes(['cat-file', 'blob', this.spec(path)])
    if (data.length > maxBytes) throw new FileTooLargeError(path, data.length, maxBytes)
    return data
  }

  async list(path: string): Promise<{ name: string; kind: 'file' | 'directory' }[]> {
    if ((await this.type(path)) !== 'tree') throw new Error(`${path} is not a directory`)
    const result = await this.git(['ls-tree', '-z', this.spec(path)], { whole: true })
    if (result.exitCode !== 0) throw new Error(result.stderr.trim() || `cannot list ${path}`)
    return parseTree(result.stdout)
  }

  async files(path: string): Promise<{ files: string[]; partial: boolean }> {
    const under = treePath(path)
    const result = await this.git(
      ['ls-tree', '-r', '-z', '--name-only', this.rev, ...(under ? ['--', under] : [])],
      { whole: true },
    )
    if (result.exitCode !== 0) throw new Error(result.stderr.trim() || `cannot list ${path}`)
    return { files: result.stdout.split('\0').filter(Boolean), partial: false }
  }

  async search(pattern: string, path: string): Promise<{ lines: string[]; error: string | null }> {
    const under = treePath(path)
    const result = await this.git(
      [
        'grep',
        '-n',
        '-I',
        '-E',
        '--no-color',
        '-e',
        pattern,
        this.rev,
        ...(under ? ['--', under] : []),
      ],
      { whole: true },
    )
    // 1 is "nothing matched".
    if (result.exitCode !== 0 && result.exitCode !== 1) {
      return { lines: [], error: result.stderr.trim() || `exit code ${result.exitCode}` }
    }
    // git names the commit before each file: `<rev>:<file>:<line>:<text>`.
    const prefix = `${this.rev}:`
    const perFile = new Map<string, number>()
    const lines: string[] = []
    for (const raw of result.stdout.split('\n')) {
      if (!raw) continue
      const line = raw.startsWith(prefix) ? raw.slice(prefix.length) : raw
      const file = line.slice(0, line.indexOf(':'))
      const seen = perFile.get(file) ?? 0
      if (seen >= MAX_FILE_MATCHES) continue
      perFile.set(file, seen + 1)
      lines.push(line)
      if (lines.length >= MAX_MATCH_LINES) break
    }
    return { lines, error: null }
  }
}

/** Matching lines of one file a search shows at most, as a search of a workspace does. */
const MAX_FILE_MATCHES = 50

/** `ls-tree -z` output: `<mode> <type> <sha>\t<name>`, each ended by NUL. */
export function parseTree(text: string): { name: string; kind: 'file' | 'directory' }[] {
  return text
    .split('\0')
    .filter(Boolean)
    .map((entry) => {
      const tab = entry.indexOf('\t')
      const type = entry.slice(0, tab).split(' ')[1]
      return {
        name: entry.slice(tab + 1),
        // A submodule (`commit`) shows as a directory with nothing to read inside.
        kind: type === 'blob' ? ('file' as const) : ('directory' as const),
      }
    })
    .toSorted((a, b) => a.name.localeCompare(b.name))
}
