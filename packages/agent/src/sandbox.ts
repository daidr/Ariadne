import type { CommandSandbox } from '@ariadne/plugin-sdk'
import { existsSync, lstatSync, readdirSync, realpathSync } from 'node:fs'
import { homedir } from 'node:os'
import { join, relative, resolve } from 'node:path'

/** How thread commands are isolated: picked at start, or turned off in the settings. */
export type SandboxKind = 'bwrap' | 'unshare' | 'sandbox-exec'

export interface SandboxStatus {
  /** What isolates commands; `null` when they run as they are. */
  kind: SandboxKind | null
  /** `off` when the user turned the sandbox off. */
  mode: 'auto' | 'off'
  /** The paths hidden from commands. */
  hidden: string[]
}

export interface Sandbox extends CommandSandbox {
  readonly kind: SandboxKind | null
  readonly status: SandboxStatus
}

/** A path made absolute, `~/` expanded and symbolic links followed where it exists. */
export function realPath(path: string): string {
  const full = resolve(path.startsWith('~/') ? join(homedir(), path.slice(2)) : path)
  try {
    return realpathSync(full)
  } catch {
    return full
  }
}

const inside = (path: string, dir: string) => {
  const rel = relative(dir, path)
  return rel === '' || (!rel.startsWith('..') && !rel.startsWith('/'))
}

/** Browser profiles, where the page keeps Ariadne's token in its storage. */
export function browserProfiles(platform = process.platform): string[] {
  const home = homedir()
  if (platform === 'darwin') {
    const support = join(home, 'Library', 'Application Support')
    return [
      join(support, 'Google', 'Chrome'),
      join(support, 'Chromium'),
      join(support, 'Microsoft Edge'),
      join(support, 'BraveSoftware'),
      join(support, 'Firefox'),
      join(home, 'Library', 'Safari'),
      join(home, 'Library', 'Containers', 'com.apple.Safari'),
      join(home, 'Library', 'WebKit'),
    ]
  }
  const config = process.env.XDG_CONFIG_HOME || join(home, '.config')
  return [
    join(config, 'google-chrome'),
    join(config, 'chromium'),
    join(config, 'microsoft-edge'),
    join(config, 'BraveSoftware'),
    join(home, '.mozilla'),
    join(home, 'snap', 'firefox'),
    join(home, 'snap', 'chromium'),
  ]
}

export type Cover = { kind: 'dir' | 'file'; path: string }

/**
 * The mounts that hide `roots` while leaving every `keep` path visible: a hidden root that
 * holds no kept path is covered whole; one that does is gone through entry by entry, so
 * only what lies beside the kept paths is covered. A kept path that holds a hidden root
 * does not show it: a workspace at the home directory still cannot see `~/.ariadne`.
 */
export function coverPaths(roots: string[], keep: string[]): Cover[] {
  const kept = keep.map(realPath)
  const covers: Cover[] = []
  const visit = (path: string) => {
    if (kept.includes(path)) return
    let info
    try {
      info = lstatSync(path)
    } catch {
      return
    }
    if (info.isSymbolicLink()) return
    const within = kept.filter((k) => inside(k, path))
    if (!info.isDirectory()) {
      if (!within.length) covers.push({ kind: 'file', path })
      return
    }
    if (!within.length) {
      covers.push({ kind: 'dir', path })
      return
    }
    for (const entry of readdirSync(path)) visit(join(path, entry))
  }
  for (const root of new Set(roots.map(realPath))) if (existsSync(root)) visit(root)
  return covers
}

/** `bwrap`: the whole filesystem as it is, the hidden parts covered, its own process list. */
export function bwrapArgs(covers: Cover[], argv: string[]): string[] {
  const mounts = covers.flatMap((cover) =>
    cover.kind === 'dir' ? ['--tmpfs', cover.path] : ['--ro-bind', '/dev/null', cover.path],
  )
  return [
    'bwrap',
    '--dev-bind',
    '/',
    '/',
    '--unshare-pid',
    '--proc',
    '/proc',
    '--die-with-parent',
    ...mounts,
    '--',
    ...argv,
  ]
}

/**
 * Covers the hidden paths in a new user and mount namespace (as root there, which mounting
 * needs), then runs the command in a nested user namespace as the user again, so files it
 * writes and the ids it sees are the user's own.
 */
const UNSHARE_SETUP = `set -e
while [ "$1" != "--" ]; do
  if [ "$1" = file ]; then mount --bind /dev/null "$2"; else mount -t tmpfs none "$2"; fi
  shift 2
done
shift
exec "$@"`

export function unshareArgs(
  covers: Cover[],
  argv: string[],
  ids: { uid: number; gid: number },
): string[] {
  return [
    'unshare',
    '--user',
    '--map-root-user',
    '--mount',
    '--pid',
    '--fork',
    '--mount-proc',
    '--',
    'sh',
    '-c',
    UNSHARE_SETUP,
    'sh',
    ...covers.flatMap((cover) => [cover.kind, cover.path]),
    '--',
    'unshare',
    '--user',
    `--map-user=${ids.uid}`,
    `--map-group=${ids.gid}`,
    '--',
    ...argv,
  ]
}

const sbplString = (text: string) => `"${text.replaceAll('\\', '\\\\').replaceAll('"', '\\"')}"`

const seatbeltRule = (verb: 'allow' | 'deny', paths: string[]) =>
  paths.length
    ? `(${verb} file-read* file-write* ${paths.map((p) => `(subpath ${sbplString(p)})`).join(' ')})`
    : ''

/**
 * A `sandbox-exec` profile: everything allowed but the hidden paths. Later rules win, so a
 * kept path inside a hidden one (a worktree under Ariadne's home) is opened again; a kept
 * path that holds a hidden one (a workspace at the home directory) opens nothing.
 */
export function seatbeltProfile(hidden: string[], keep: string[]): string {
  const roots = hidden.map(realPath)
  const reopen = keep
    .map(realPath)
    .filter((path) => roots.some((root) => root !== path && inside(path, root)))
  return [
    '(version 1)',
    '(allow default)',
    seatbeltRule('deny', roots),
    seatbeltRule('allow', reopen),
  ]
    .filter(Boolean)
    .join('\n')
}

async function works(argv: string[]): Promise<boolean> {
  try {
    const proc = Bun.spawn(argv, { stdout: 'ignore', stderr: 'ignore', stdin: 'ignore' })
    return (await proc.exited) === 0
  } catch {
    return false
  }
}

const ids = () => ({ uid: process.getuid?.() ?? 0, gid: process.getgid?.() ?? 0 })

/** Whether `kind` runs on this machine: a command in it starts and exits cleanly. */
export function sandboxWorks(kind: SandboxKind): Promise<boolean> {
  switch (kind) {
    case 'bwrap':
      return works(bwrapArgs([], ['true']))
    case 'unshare':
      return works(unshareArgs([], ['true'], ids()))
    case 'sandbox-exec':
      return works(['sandbox-exec', '-p', '(version 1)(allow default)', 'true'])
  }
}

/** Which sandbox this machine can run, tried in order of preference. */
export async function detectSandbox(platform = process.platform): Promise<SandboxKind | null> {
  const order: SandboxKind[] =
    platform === 'linux' ? ['bwrap', 'unshare'] : platform === 'darwin' ? ['sandbox-exec'] : []
  for (const kind of order) if (await sandboxWorks(kind)) return kind
  return null
}

/**
 * The sandbox thread commands run in. `hide` are the paths they must not see: Ariadne's
 * home directory and, by default, the browsers' profiles.
 */
export function createSandbox(options: {
  kind: SandboxKind | null
  mode: 'auto' | 'off'
  hide: string[]
}): Sandbox {
  const kind = options.mode === 'off' ? null : options.kind
  const hidden = [...new Set(options.hide.map(realPath))]
  return {
    kind,
    status: { kind, mode: options.mode, hidden },
    wrap(argv, { keep }) {
      switch (kind) {
        case 'bwrap':
          return bwrapArgs(coverPaths(hidden, keep), argv)
        case 'unshare':
          return unshareArgs(coverPaths(hidden, keep), argv, ids())
        case 'sandbox-exec':
          return ['sandbox-exec', '-p', seatbeltProfile(hidden, keep), ...argv]
        default:
          return argv
      }
    },
  }
}

/** Commands run as they are; for tests and for plugins loaded without a host sandbox. */
export const noSandbox: Sandbox = createSandbox({ kind: null, mode: 'off', hide: [] })
