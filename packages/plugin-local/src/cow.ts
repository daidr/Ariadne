import type { ExecResult } from '@ariadne/plugin-sdk'
import { existsSync } from 'node:fs'
import { randomUUID } from 'node:crypto'
import { lstat, mkdir, rm, writeFile } from 'node:fs/promises'
import { basename, dirname, join } from 'node:path'

/**
 * Dependency directories a fresh worktree lacks because git ignores them. A new thread
 * gets them copied from the user's checkout, but only as copy-on-write clones: those take
 * no room until either side changes a file. Without clone support nothing is copied and
 * the thread installs its own, which is what a plain worktree does anyway.
 */
export const SEED_DIRS = new Set(['node_modules', 'vendor', '.pnpm-store'])
// Python environments are left out: their scripts and `activate` name the environment by
// absolute path, so a clone would install into the user's own environment.

/** Clone `from` to `to` sharing storage, or fail; never a full copy. */
export type Cloner = (from: string, to: string) => Promise<boolean>

/**
 * The platform's clone: `cp --reflink=always` (btrfs, XFS, bcachefs and other Linux
 * filesystems with reflinks) or `cp -c` (APFS clonefile on macOS).
 */
export const platformCloner: Cloner = async (from, to) => {
  const argv =
    process.platform === 'darwin'
      ? ['cp', '-c', '-R', from, to]
      : ['cp', '-a', '--reflink=always', from, to]
  const proc = Bun.spawn(argv, { stdout: 'ignore', stderr: 'ignore', stdin: 'ignore' })
  return (await proc.exited) === 0
}

/**
 * Whether the checkout and the worktree sit on storage that clones: one small file in the
 * repository's git directory is tried, so nothing appears among the user's files.
 */
async function canClone(clone: Cloner, gitDir: string, targetDir: string): Promise<boolean> {
  // Unique per call: threads open their worktrees at the same time.
  const tag = randomUUID()
  const probe = join(gitDir, `ariadne-clone-probe-${tag}`)
  const copy = join(targetDir, `.ariadne-clone-probe-${tag}`)
  try {
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
