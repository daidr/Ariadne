import type { ExecResult } from '@ariadne/plugin-sdk'
import { constants, existsSync } from 'node:fs'
import { randomUUID } from 'node:crypto'
import {
  copyFile,
  cp,
  lstat,
  mkdir,
  readFile,
  readlink,
  rm,
  symlink,
  writeFile,
} from 'node:fs/promises'
import { release } from 'node:os'
import { basename, dirname, join, resolve } from 'node:path'

/**
 * Dependency directories a fresh worktree lacks because git ignores them. A new thread
 * gets them copied from the user's checkout, but only as copy-on-write clones: those take
 * no room until either side changes a file. Without clone support nothing is copied and
 * the thread installs its own, which is what a plain worktree does anyway.
 */
export const SEED_DIRS = new Set(['node_modules', 'vendor', '.pnpm-store'])
// Python environments are left out: their scripts and `activate` name the environment by
// absolute path, so a clone would install into the user's own environment.

/** Clone the file or directory `from` to `to` sharing storage, or fail; never a full copy. */
export type Cloner = (from: string, to: string) => Promise<boolean>

const windows = process.platform === 'win32'

/** The drive a Windows path is on, as `D`; `null` for a network share. */
const driveOf = (path: string) =>
  /^([a-zA-Z]):[\\/]/.exec(resolve(path))?.[1]?.toUpperCase() ?? null

const blockCloning = new Map<string, Promise<boolean>>()

/**
 * Whether copies on a Windows drive share storage: ReFS, which a Dev Drive is, clones the
 * blocks of a file copied within the volume (Windows 11 24H2 and later; before that the
 * copy is a full one, as a checkout would be). NTFS never does.
 */
function clonesBlocks(drive: string): Promise<boolean> {
  let known = blockCloning.get(drive)
  if (!known) {
    known = (async () => {
      // Block cloning on copy came with Windows 11 24H2 (build 26100).
      if (Number(release().split('.')[2]) < 26100) return false
      try {
        const proc = Bun.spawn(
          [
            'powershell.exe',
            '-NoProfile',
            '-NonInteractive',
            '-Command',
            `(Get-Volume -DriveLetter ${drive}).FileSystemType`,
          ],
          { stdout: 'pipe', stderr: 'ignore', stdin: 'ignore', windowsHide: true },
        )
        const [out, code] = await Promise.all([new Response(proc.stdout).text(), proc.exited])
        return code === 0 && out.trim().toLowerCase() === 'refs'
      } catch {
        return false
      }
    })()
    blockCloning.set(drive, known)
  }
  return known
}

async function copies(argv: string[]): Promise<boolean> {
  const proc = Bun.spawn(argv, {
    stdout: 'ignore',
    stderr: 'ignore',
    stdin: 'ignore',
    windowsHide: true,
  })
  return (await proc.exited) === 0
}

/**
 * The platform's clone: reflinks on Linux (btrfs, XFS, bcachefs and other filesystems
 * with them), `clonefile` on macOS (APFS), and on Windows a copy within a ReFS volume
 * (a Dev Drive), which the system turns into a block clone. A file is cloned in this
 * process, a directory by `cp` where there is one.
 */
export const platformCloner: Cloner = async (from, to) => {
  try {
    if (windows) {
      const drive = driveOf(from)
      if (!drive || drive !== driveOf(to) || !(await clonesBlocks(drive))) return false
      await cp(from, to, { recursive: true, verbatimSymlinks: true, errorOnExist: true })
      return true
    }
    if ((await lstat(from)).isDirectory()) {
      return await copies(
        process.platform === 'darwin'
          ? ['cp', '-c', '-R', from, to]
          : ['cp', '-a', '--reflink=always', from, to],
      )
    }
    await copyFile(from, to, constants.COPYFILE_FICLONE_FORCE)
    return true
  } catch {
    return false
  }
}

/**
 * Whether the checkout and the worktree sit on storage that clones: one small file in the
 * repository's git directory is tried, so nothing appears among the user's files.
 */
export async function canClone(clone: Cloner, gitDir: string, targetDir: string): Promise<boolean> {
  // Unique per call: threads open their worktrees at the same time.
  const tag = randomUUID()
  const probe = join(gitDir, `ariadne-clone-probe-${tag}`)
  const copy = join(targetDir, `.ariadne-clone-probe-${tag}`)
  try {
    await mkdir(targetDir, { recursive: true })
    await writeFile(probe, 'probe')
    return await clone(probe, copy)
  } catch {
    return false
  } finally {
    await rm(probe, { force: true })
    await rm(copy, { force: true })
  }
}

/**
 * Clone the ignored dependency directories of `checkout` into the same places in
 * `worktree`. Returns what was cloned; empty when the filesystem does not clone.
 */
export async function seedWorktree(
  checkout: string,
  worktree: string,
  git: (cwd: string, ...args: string[]) => Promise<ExecResult>,
  clone: Cloner = platformCloner,
): Promise<string[]> {
  const listed = await git(
    checkout,
    'ls-files',
    '--others',
    '--ignored',
    '--exclude-standard',
    '--directory',
    '-z',
  )
  if (listed.exitCode !== 0) return []
  const dirs = listed.stdout
    .split('\0')
    .map((entry) => entry.replace(/\/$/, ''))
    .filter((entry) => entry && SEED_DIRS.has(basename(entry)))
  if (!dirs.length) return []
  const gitDir = (
    await git(checkout, 'rev-parse', '--path-format=absolute', '--git-common-dir')
  ).stdout.trim()
  if (!gitDir || !(await canClone(clone, gitDir, worktree))) return []
  const seeded: string[] = []
  for (const dir of dirs) {
    const target = join(worktree, dir)
    if (existsSync(target)) continue
    // A link (say, to another disk) would be copied as the link, and installing in the
    // worktree would write through it into the user's own directory.
    const info = await lstat(join(checkout, dir)).catch(() => null)
    if (!info?.isDirectory()) continue
    await mkdir(dirname(target), { recursive: true })
    if (await clone(join(checkout, dir), target)) seeded.push(dir)
    else await rm(target, { recursive: true, force: true })
  }
  return seeded
}

/** How many files are cloned at a time. */
const CLONES_AT_ONCE = 32

/** Git in `cwd`; `whole` keeps long output whole. */
export type CloneGit = (cwd: string, args: string[], whole?: boolean) => Promise<ExecResult>

/**
 * Check out a worktree that was added with `--no-checkout` by cloning the files its commit
 * tracks from `from` (the user's checkout, or another thread's worktree), so they take no
 * room until changed. Files missing from `from` or different there (changes not
 * committed, another commit) are then written by git, and only those: the index is
 * refreshed against what was cloned before the checkout. Returns how many files were
 * cloned.
 */
export async function checkoutCloned(
  from: string | null,
  worktree: string,
  git: CloneGit,
  clone: Cloner = platformCloner,
): Promise<number> {
  const pending = await pendingMark(worktree)
  if (pending) await writeFile(pending, '')
  const cloned = from ? await cloneTracked(from, worktree, git, clone) : 0
  // Fill the index and compare it with the files; then write what differs.
  await git(worktree, ['reset', '-q'])
  const result = await git(worktree, ['reset', '-q', '--hard'])
  if (result.exitCode !== 0) {
    throw new Error(result.stderr.trim() || `git reset exited ${result.exitCode}`)
  }
  if (pending) await rm(pending, { force: true })
  return cloned
}

/** The worktree's own git directory, read from its `.git` file; `null` when it has none. */
async function gitDirOf(worktree: string): Promise<string | null> {
  const text = await readFile(join(worktree, '.git'), 'utf8').catch(() => '')
  const dir = /^gitdir: (.+)$/m.exec(text)?.[1]?.trim()
  return dir ? resolve(worktree, dir) : null
}

/** The file that says a worktree's files are still being put in place. */
async function pendingMark(worktree: string): Promise<string | null> {
  const dir = await gitDirOf(worktree)
  return dir && join(dir, 'ariadne-checkout-pending')
}

/**
 * Whether a worktree added with `--no-checkout` never got its files: the checkout failed
 * or the server stopped in the middle. It has no index before the checkout begins and the
 * mark until it ends. Used as it is, its commit's files would all read as deleted.
 */
export async function checkoutPending(worktree: string): Promise<boolean> {
  const dir = await gitDirOf(worktree)
  return (
    !!dir && (existsSync(join(dir, 'ariadne-checkout-pending')) || !existsSync(join(dir, 'index')))
  )
}

async function cloneTracked(
  from: string,
  worktree: string,
  git: CloneGit,
  clone: Cloner,
): Promise<number> {
  const listed = await git(worktree, ['ls-tree', '-r', '-z', '--name-only', 'HEAD'], true)
  const paths = listed.exitCode === 0 ? listed.stdout.split('\0').filter(Boolean) : []
  const made = new Set<string>()
  let cloned = 0
  let next = 0
  const work = async () => {
    while (next < paths.length) {
      const path = paths[next++]!
      const source = join(from, path)
      const target = join(worktree, path)
      const info = await lstat(source).catch(() => null)
      // A submodule's directory and anything else that is not a file are git's to make.
      if (!info || !(info.isFile() || info.isSymbolicLink())) continue
      const dir = dirname(target)
      if (!made.has(dir)) {
        await mkdir(dir, { recursive: true }).catch(() => {})
        made.add(dir)
      }
      if (info.isSymbolicLink()) {
        await readlink(source)
          .then((link) => symlink(link, target))
          .catch(() => {})
      } else if (await clone(source, target)) cloned++
    }
  }
  await Promise.all(Array.from({ length: CLONES_AT_ONCE }, work))
  return cloned
}
