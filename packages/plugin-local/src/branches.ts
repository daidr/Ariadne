import {
  MergeRefused,
  type BranchChanges,
  type ChangedFile,
  type ChangedFileStatus,
  type ExecResult,
  type FileVersions,
  type MergeMethod,
  type MergeOutcome,
} from '@ariadne/plugin-sdk'

/**
 * Runs git on the repository's machine, in the repository unless `cwd` names another
 * directory (a worktree). `whole` keeps all of the output, for file contents.
 */
export type GitRun = (
  args: string[],
  options?: { cwd?: string; whole?: boolean; env?: Record<string, string> },
) => Promise<ExecResult>

/** Files larger than this are not shown side by side. */
export const MAX_SHOWN_BYTES = 512 * 1024

const MAX_COMMITS = 100

function fail(result: ExecResult, what: string): never {
  throw new Error(`${what} failed: ${result.stderr.trim() || `exit ${result.exitCode}`}`)
}

async function sha(git: GitRun, ref: string, cwd?: string): Promise<string | null> {
  const result = await git(
    ['rev-parse', '--verify', '--quiet', `${ref}^{commit}`],
    cwd ? { cwd } : undefined,
  )
  return result.exitCode === 0 ? result.stdout.trim() : null
}

const STATUS: Record<string, ChangedFileStatus> = {
  A: 'added',
  M: 'modified',
  D: 'deleted',
  R: 'renamed',
  C: 'copied',
  T: 'type',
}

/** `git diff --name-status -z` and `--numstat -z` of the same range, together. */
export function parseDiff(nameStatus: string, numstat: string): ChangedFile[] {
  const counts = new Map<string, { added: number | null; removed: number | null }>()
  const stats = numstat.split('\0')
  for (let i = 0; i < stats.length; i++) {
    const line = stats[i]!
    if (!line) continue
    const [added = '-', removed = '-', path = ''] = line.split('\t')
    // A rename leaves the path empty and gives the old and new path as the next two fields.
    const key = path || stats[(i += 2)]!
    counts.set(key, {
      added: added === '-' ? null : Number(added),
      removed: removed === '-' ? null : Number(removed),
    })
  }
  const files: ChangedFile[] = []
  const fields = nameStatus.split('\0')
  for (let i = 0; i < fields.length; i++) {
    const code = fields[i]!
    if (!code) continue
    const status = STATUS[code[0]!] ?? 'modified'
    const twoPaths = status === 'renamed' || status === 'copied'
    const previousPath = twoPaths ? fields[++i]! : null
    const path = fields[++i]!
    files.push({
      path,
      previousPath,
      status,
      ...(counts.get(path) ?? { added: null, removed: null }),
    })
  }
  return files
}

/** Paths of `git status --porcelain=v1 -z`; a rename gives its new path. */
export function parseStatus(output: string): string[] {
  const paths: string[] = []
  const fields = output.split('\0')
  for (let i = 0; i < fields.length; i++) {
    const entry = fields[i]!
    if (entry.length < 4) continue
    paths.push(entry.slice(3))
    // The source of a rename or copy follows as its own field.
    if (entry[0] === 'R' || entry[0] === 'C') i++
  }
  return paths
}

/**
 * What `branch` changed since it left `baseRef`, compared as a pull request would be: from
 * the newest commit the two share to the branch's newest commit. `worktree` is checked
 * for files not committed yet. `null` when the branch does not exist.
 */
export async function branchChanges(
  git: GitRun,
  input: { branch: string; base: string; baseRef: string; worktree: string | null },
): Promise<BranchChanges | null> {
  const head = await sha(git, `refs/heads/${input.branch}`)
  if (!head) return null
  const baseSha = await sha(git, input.baseRef)
  if (!baseSha) throw new MergeRefused('no-base', `${input.base} does not exist`)
  const mergeBase = await git(['merge-base', baseSha, head])
  // Unrelated histories share nothing: everything on the branch counts.
  const from = mergeBase.exitCode === 0 ? mergeBase.stdout.trim() : baseSha
  const [nameStatus, numstat, log, status] = await Promise.all([
    git(['diff', '--name-status', '-z', '-M', from, head], { whole: true }),
    git(['diff', '--numstat', '-z', '-M', from, head], { whole: true }),
    git(['log', '-z', `--max-count=${MAX_COMMITS}`, '--format=%H %s', `${baseSha}..${head}`], {
      whole: true,
    }),
    input.worktree
      ? git(['status', '--porcelain=v1', '-z', '--untracked-files=normal'], {
          cwd: input.worktree,
          whole: true,
        })
      : null,
  ])
  if (nameStatus.exitCode !== 0) fail(nameStatus, 'git diff')
  if (numstat.exitCode !== 0) fail(numstat, 'git diff')
  const commits = log.stdout
    .split('\0')
    .filter((line) => line.trim())
    .map((line) => {
      const space = line.indexOf(' ')
      return { sha: line.slice(0, space).trim(), subject: line.slice(space + 1) }
    })
  return {
    branch: input.branch,
    base: input.base,
    from,
    head,
    commits,
    files: parseDiff(nameStatus.stdout, numstat.stdout),
    // A worktree that is gone has nothing uncommitted.
    uncommitted: status?.exitCode === 0 ? parseStatus(status.stdout) : [],
  }
}

/** What changed from one tree or commit to another, file by file. */
export async function treeDiff(git: GitRun, from: string, to: string): Promise<ChangedFile[]> {
  const [nameStatus, numstat] = await Promise.all([
    git(['diff', '--name-status', '-z', '-M', from, to], { whole: true }),
    git(['diff', '--numstat', '-z', '-M', from, to], { whole: true }),
  ])
  if (nameStatus.exitCode !== 0) fail(nameStatus, 'git diff')
  if (numstat.exitCode !== 0) fail(numstat, 'git diff')
  return parseDiff(nameStatus.stdout, numstat.stdout)
}

/**
 * The index snapshots are written with, in the worktree's own git directory beside its
 * real index. It stays from one snapshot to the next, so git reads again only the files
 * whose size or time changed since.
 */
export const SNAPSHOT_INDEX = 'ariadne-snapshot-index'

/** Where a thread's snapshots are kept from git's cleanup, one ref per tree. */
export const snapshotRefs = (threadId: string) => `refs/ariadne/turns/${threadId}/`

/**
 * The files of the worktree `cwd` as they are now, committed or not and ignored ones left
 * out, as a git tree. Written through an index of its own, so the worktree, its index and
 * its branch stay as they are; a ref under `snapshotRefs(threadId)` keeps the tree, so a
 * turn's changes still open once `git gc` has pruned what nothing points at.
 */
export async function worktreeSnapshot(
  git: GitRun,
  cwd: string,
  threadId: string,
): Promise<string> {
  const dir = await git(['rev-parse', '--absolute-git-dir'], { cwd })
  if (dir.exitCode !== 0) fail(dir, 'git rev-parse')
  const env = { GIT_INDEX_FILE: `${dir.stdout.trim()}/${SNAPSHOT_INDEX}` }
  const add = await git(['add', '--all'], { cwd, env })
  if (add.exitCode !== 0) fail(add, 'git add')
  const tree = await git(['write-tree'], { cwd, env })
  if (tree.exitCode !== 0) fail(tree, 'git write-tree')
  const written = tree.stdout.trim()
  const kept = await git(['update-ref', `${snapshotRefs(threadId)}${written}`, written], { cwd })
  if (kept.exitCode !== 0) fail(kept, 'git update-ref')
  return written
}

/** Let git clean up a thread's snapshots: its project is going away. */
export async function dropSnapshots(git: GitRun, threadId: string): Promise<void> {
  const refs = await git(['for-each-ref', '--format=%(refname)', snapshotRefs(threadId)])
  if (refs.exitCode !== 0) fail(refs, 'git for-each-ref')
  for (const ref of refs.stdout.split('\n').filter(Boolean)) {
    const result = await git(['update-ref', '-d', ref])
    if (result.exitCode !== 0) fail(result, 'git update-ref')
  }
}

async function blob(git: GitRun, commit: string, path: string) {
  const spec = `${commit}:${path}`
  const size = await git(['cat-file', '-s', spec])
  if (size.exitCode !== 0) return null
  if (Number(size.stdout.trim()) > MAX_SHOWN_BYTES) return 'large' as const
  const content = await git(['cat-file', 'blob', spec], { whole: true })
  if (content.exitCode !== 0) fail(content, 'git cat-file')
  return content.stdout
}

/** git calls a file binary when its first 8000 bytes hold a NUL. */
const binary = (text: string | null) => !!text && text.slice(0, 8000).includes('\0')

/** A file at `from` (under its old path when renamed) and at `head`. */
export async function fileVersions(
  git: GitRun,
  input: { from: string; head: string; path: string; previousPath: string | null },
): Promise<FileVersions> {
  const [before, after] = await Promise.all([
    blob(git, input.from, input.previousPath ?? input.path),
    blob(git, input.head, input.path),
  ])
  if (before === 'large' || after === 'large') {
    return { before: null, after: null, skipped: 'large' }
  }
  if (binary(before) || binary(after)) return { before: null, after: null, skipped: 'binary' }
  return { before, after, skipped: null }
}

/** Whether `baseRef` already holds the branch's newest commit. */
export async function branchMerged(git: GitRun, branch: string, baseRef: string) {
  if (!(await sha(git, `refs/heads/${branch}`))) return false
  const result = await git(['merge-base', '--is-ancestor', `refs/heads/${branch}`, baseRef])
  return result.exitCode === 0
}

/** The author git would write commits as, read from the user's own configuration. */
export type Identity = () => Promise<{ name: string; email: string } | null>

/** `user.name` and `user.email` as `config` reads them; only these two values are used. */
export function gitIdentity(config: (key: string) => Promise<ExecResult>): Identity {
  return async () => {
    const [name, email] = (await Promise.all([config('user.name'), config('user.email')])).map(
      (result) => (result.exitCode === 0 ? result.stdout.trim() : ''),
    )
    return name && email ? { name, email } : null
  }
}

/**
 * Merge `branch` into the local branch `base` without a working tree: the result is
 * computed with `git merge-tree`, written as one commit (squash) or a merge commit, and
 * the base moves to it only if it is still where it was. When the base is checked out
 * somewhere, that checkout fast-forwards to it, which updates its files and refuses when
 * changes there would be overwritten. A conflict changes nothing.
 */
export async function mergeBranch(
  git: GitRun,
  identity: Identity,
  input: { branch: string; base: string; method: MergeMethod; message: string },
): Promise<MergeOutcome> {
  const baseRef = `refs/heads/${input.base}`
  const head = await sha(git, `refs/heads/${input.branch}`)
  if (!head) throw new Error(`${input.branch} does not exist`)
  const baseSha = await sha(git, baseRef)
  if (!baseSha) throw new MergeRefused('no-base', `${input.base} does not exist`)
  if ((await git(['merge-base', '--is-ancestor', head, baseSha])).exitCode === 0) {
    return { kind: 'empty' }
  }
  const author = await identity()
  if (!author) throw new MergeRefused('identity', 'user.name or user.email is not set')

  const merged = await git(
    ['merge-tree', '--write-tree', '--name-only', '--no-messages', '-z', baseSha, head],
    { whole: true },
  )
  const lines = merged.stdout.split('\0').filter(Boolean)
  if (merged.exitCode === 1) return { kind: 'conflict', files: [...new Set(lines.slice(1))] }
  if (merged.exitCode !== 0) {
    if (/usage|unknown option/i.test(merged.stderr)) {
      throw new MergeRefused('git-version', merged.stderr.trim())
    }
    fail(merged, 'git merge-tree')
  }
  const tree = lines[0]!
  const baseTree = (await git(['rev-parse', `${baseSha}^{tree}`])).stdout.trim()
  // The base already holds the same files, e.g. the branch was squashed into it before.
  if (tree === baseTree) return { kind: 'empty' }

  const parents = input.method === 'merge' ? ['-p', baseSha, '-p', head] : ['-p', baseSha]
  const commit = await git([
    '-c',
    `user.name=${author.name}`,
    '-c',
    `user.email=${author.email}`,
    'commit-tree',
    tree,
    ...parents,
    '-m',
    input.message,
  ])
  if (commit.exitCode !== 0) fail(commit, 'git commit-tree')
  const result = commit.stdout.trim()

  const checkout = await checkoutOf(git, baseRef)
  if (checkout) {
    // A commit made in the checkout meanwhile: the merge started from an older base.
    if ((await sha(git, 'HEAD', checkout)) !== baseSha) {
      throw new MergeRefused('base-moved', `${input.base} moved in ${checkout}`)
    }
    // The checkout's files are written without the user's global config, so filters set
    // there (Git LFS, for one) would not run on them.
    if (await usesFilters(git, tree)) {
      throw new MergeRefused('filters', 'the repository uses .gitattributes filters')
    }
    const moved = await git(['merge', '--ff-only', '-q', result], { cwd: checkout })
    if (moved.exitCode !== 0) throw new MergeRefused('checkout', moved.stderr.trim())
  } else {
    // Moves only from where the merge started: a commit made meanwhile is not lost.
    const moved = await git(['update-ref', baseRef, result, baseSha])
    if (moved.exitCode !== 0) throw new MergeRefused('base-moved', moved.stderr.trim())
  }
  return { kind: 'merged', sha: result, head }
}

/**
 * Take back the merge that left the local branch `base` at `sha`: the base goes back to
 * the commit it was on before, and a checkout of it gets its files back, keeping changes
 * not committed there (and refusing when they are to the same files). Only while the
 * merge is the base's newest commit and no remote branch has it: later commits would be
 * lost, and a pushed merge would come back with the next pull.
 */
export async function undoMerge(git: GitRun, input: { base: string; sha: string }): Promise<void> {
  const baseRef = `refs/heads/${input.base}`
  const baseSha = await sha(git, baseRef)
  if (!baseSha) throw new MergeRefused('no-base', `${input.base} does not exist`)
  const before = await sha(git, `${input.sha}^1`)
  if (baseSha !== input.sha || !before) {
    throw new MergeRefused('undo-moved', `${input.base} is at ${baseSha.slice(0, 12)}`)
  }
  const pushed = await git([
    'for-each-ref',
    '--count=1',
    '--format=%(refname:short)',
    '--contains',
    input.sha,
    'refs/remotes',
  ])
  if (pushed.exitCode !== 0) fail(pushed, 'git for-each-ref')
  if (pushed.stdout.trim()) throw new MergeRefused('undo-pushed', pushed.stdout.trim())

  const checkout = await checkoutOf(git, baseRef)
  if (!checkout) {
    const moved = await git(['update-ref', baseRef, before, input.sha])
    if (moved.exitCode !== 0) throw new MergeRefused('undo-moved', moved.stderr.trim())
    return
  }
  // A commit made in the checkout meanwhile.
  if ((await sha(git, 'HEAD', checkout)) !== input.sha) {
    throw new MergeRefused('undo-moved', `${input.base} moved in ${checkout}`)
  }
  const tree = (await git(['rev-parse', `${before}^{tree}`])).stdout.trim()
  if (await usesFilters(git, tree)) {
    throw new MergeRefused('filters', 'the repository uses .gitattributes filters')
  }
  const moved = await git(['reset', '--keep', '-q', before], { cwd: checkout })
  if (moved.exitCode !== 0) throw new MergeRefused('checkout', moved.stderr.trim())
}

/** Whether any `.gitattributes` in `tree` sets a filter. */
async function usesFilters(git: GitRun, tree: string): Promise<boolean> {
  const found = await git([
    'grep',
    '-q',
    '-E',
    '[[:space:]]filter=',
    tree,
    '--',
    '.gitattributes',
    '**/.gitattributes',
  ])
  return found.exitCode === 0
}

/** The worktree that has `ref` checked out, if any. */
async function checkoutOf(git: GitRun, ref: string): Promise<string | null> {
  const list = await git(['worktree', 'list', '--porcelain', '-z'], { whole: true })
  if (list.exitCode !== 0) fail(list, 'git worktree list')
  let path: string | null = null
  for (const field of list.stdout.split('\0')) {
    if (field.startsWith('worktree ')) path = field.slice('worktree '.length)
    else if (field === `branch ${ref}` && path) return path
  }
  return null
}

/**
 * Remove a worktree with `git worktree remove`, keeping its branch. One with changes not
 * committed (untracked files included; ignored ones such as `node_modules` go) stays and
 * gives `false`, unless `discard` drops them too. A directory already gone is just forgotten.
 */
export async function removeWorktree(
  git: GitRun,
  dir: string,
  exists: (dir: string) => Promise<boolean>,
  options: { discard?: boolean } = {},
): Promise<boolean> {
  if (!(await exists(dir))) {
    await git(['worktree', 'prune'])
    return true
  }
  // Twice: a locked worktree goes as well.
  const force = options.discard ? ['--force', '--force'] : []
  const result = await git(['worktree', 'remove', ...force, dir])
  if (result.exitCode === 0) return true
  if (/modified or untracked|use --force/i.test(result.stderr)) return false
  fail(result, 'git worktree remove')
}
