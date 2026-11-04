import type { CommandSandbox } from '@ariadne/plugin-sdk'
import { lstatSync, mkdtempSync, realpathSync, rmSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
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
  /** The server's platform: Windows has no sandbox yet, and says so. */
  platform: NodeJS.Platform
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
      // Chrome, Beta, Dev and Canary all live under Google.
      join(support, 'Google'),
      join(support, 'Chromium'),
      join(support, 'Microsoft Edge'),
      join(support, 'Microsoft Edge Beta'),
      join(support, 'Microsoft Edge Dev'),
      join(support, 'BraveSoftware'),
      join(support, 'Vivaldi'),
      join(support, 'com.operasoftware.Opera'),
      join(support, 'Arc'),
      join(support, 'Firefox'),
      join(home, 'Library', 'Safari'),
      join(home, 'Library', 'Containers', 'com.apple.Safari'),
      join(home, 'Library', 'WebKit'),
    ]
  }
  const config = process.env.XDG_CONFIG_HOME || join(home, '.config')
  return [
    ...[
      'google-chrome',
      'google-chrome-beta',
      'google-chrome-unstable',
      'chromium',
      'microsoft-edge',
      'microsoft-edge-beta',
      'microsoft-edge-dev',
      'BraveSoftware',
      'vivaldi',
      'vivaldi-snapshot',
      'opera',
      'opera-beta',
      'opera-developer',
    ].map((name) => join(config, name)),
    join(home, '.mozilla'),
    // Flatpak and snap keep each browser's profile under the app.
    ...[
      'com.google.Chrome',
      'com.google.ChromeDev',
      'org.chromium.Chromium',
      'com.microsoft.Edge',
      'com.brave.Browser',
      'com.vivaldi.Vivaldi',
      'com.opera.Opera',
      'org.mozilla.firefox',
      'io.gitlab.librewolf-community',
    ].map((app) => join(home, '.var', 'app', app)),
    ...['firefox', 'chromium', 'brave', 'opera', 'vivaldi'].map((app) => join(home, 'snap', app)),
  ]
}

/**
 * One step in making the sandbox's view, in order:
 * - `hold`: keep a handle on a kept path before what holds it is covered;
 * - `tmpfs`: cover a hidden directory with an empty one;
 * - `null`: cover a hidden file with `/dev/null`;
 * - `bind`: put a kept path back inside a covered directory;
 * - `ro`: make a path read-only;
 * - `rw`: make a kept path inside a read-only one writable again.
 */
export type Mount = { kind: 'hold' | 'tmpfs' | 'null' | 'bind' | 'ro' | 'rw'; path: string }

const existing = (path: string) => {
  try {
    return lstatSync(path)
  } catch {
    return null
  }
}

const within = (path: string, dirs: string[]) => dirs.some((dir) => inside(path, dir))
const strictlyWithin = (path: string, dirs: string[]) =>
  dirs.some((dir) => dir !== path && inside(path, dir))
const cover = (path: string): Mount => ({
  kind: existing(path)?.isDirectory() ? 'tmpfs' : 'null',
  path,
})

/**
 * The mounts that hide `hidden`, leave every `keep` path visible, and make `readOnly`
 * paths read-only. A hidden directory is covered whole, so files saved into it later (a
 * settings file replaced by a rename) stay hidden too; kept paths inside it are put back.
 * A kept path that holds a hidden one does not show it: a workspace at the home directory
 * still cannot see `~/.ariadne`.
 */
export function mountPlan(hidden: string[], keep: string[], readOnly: string[] = []): Mount[] {
  const kept = [...new Set(keep.map(realPath))]
  const roots = [...new Set(hidden.map(realPath))].filter((path) => existing(path))
  // Roots no kept path holds, then the kept paths inside them, then the roots inside those.
  const outer = roots.filter((root) => !within(root, kept))
  const restored = kept.filter((path) => strictlyWithin(path, outer) && existing(path))
  const nested = roots.filter((root) => within(root, kept))
  // Read-only paths outside every kept path are locked first, so covers made after stay;
  // kept paths inside them are opened again, and read-only paths inside kept ones (a git
  // directory's config) locked last, where the sandbox shows them.
  const locked = [...new Set(readOnly.map(realPath))].filter((path) => existing(path))
  const outerLocked = locked.filter((path) => !within(path, kept))
  const reopened = kept.filter(
    (path) => strictlyWithin(path, outerLocked) && !restored.includes(path) && existing(path),
  )
  const shown = (path: string) =>
    !within(path, nested) && (!within(path, outer) || within(path, restored))
  const innerLocked = locked.filter((path) => within(path, kept) && shown(path))
  const as =
    (kind: Mount['kind']) =>
    (path: string): Mount => ({ kind, path })
  return [
    ...restored.map(as('hold')),
    ...outerLocked.map(as('ro')),
    ...outer.map(cover),
    ...restored.map(as('bind')),
    ...reopened.map(as('rw')),
    ...nested.map(cover),
    ...innerLocked.map(as('ro')),
  ]
}

/** `bwrap`: the whole filesystem as it is, the hidden parts covered, its own process list. */
export function bwrapArgs(mounts: Mount[], argv: string[]): string[] {
  // bwrap reads bind sources from the filesystem as it was, so nothing needs holding.
  const args = mounts.flatMap((mount) => {
    switch (mount.kind) {
      case 'tmpfs':
        return ['--tmpfs', mount.path]
      case 'null':
        return ['--ro-bind', '/dev/null', mount.path]
      case 'bind':
        return ['--bind', mount.path, mount.path]
      case 'ro':
        return ['--ro-bind', mount.path, mount.path]
      case 'rw':
        return ['--bind', mount.path, mount.path]
      default:
        return []
    }
  })
  return [
    'bwrap',
    '--dev-bind',
    '/',
    '/',
    '--unshare-pid',
    '--proc',
    '/proc',
    '--die-with-parent',
    ...args,
    '--',
    ...argv,
  ]
}

/**
 * Makes the mounts in a new user and mount namespace (as root there, which mounting
 * needs), then runs the command in a nested user namespace as the user again, so files it
 * writes and the ids it sees are the user's own. A kept path is held open before what
 * holds it is covered, and put back from that handle.
 */
const UNSHARE_SETUP = `set -e
declare -A held
while [ "$1" != "--" ]; do
  case "$1" in
    hold) exec {fd}<"$2"; held["$2"]=$fd ;;
    tmpfs) mount -t tmpfs none "$2" ;;
    null) mount --bind /dev/null "$2" ;;
    bind) mkdir -p "$2"; mount --bind --no-canonicalize "/proc/self/fd/\${held["$2"]}" "$2" ;;
    rw) mount --rbind "$2" "$2"; mount -o remount,bind,rw "$2" ;;
    ro) mount --rbind "$2" "$2"
      # A mount's locked flags must be kept when it is made read-only.
      mount -o remount,bind,ro "$2" 2>/dev/null || mount -o remount,bind,ro,nosuid,nodev "$2" ||
        mount -o remount,bind,ro,nosuid,nodev,noexec "$2" ;;
  esac
  shift 2
done
shift
for fd in "\${held[@]}"; do exec {fd}<&-; done
# The working directory was entered before the mounts; enter it again through them, or
# relative paths (and "..") would still reach what is now covered.
cd -- "$PWD"
exec "$@"`

export function unshareArgs(
  mounts: Mount[],
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
    'bash',
    '-c',
    UNSHARE_SETUP,
    'bash',
    ...mounts.flatMap((mount) => [mount.kind, mount.path]),
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

const seatbeltRule = (verb: string, paths: string[]) =>
  paths.length ? `(${verb} ${paths.map((p) => `(subpath ${sbplString(p)})`).join(' ')})` : ''

/**
 * A `sandbox-exec` profile: everything allowed but the hidden paths. Later rules win, so a
 * kept path inside a hidden one (a worktree under Ariadne's home) is opened again, and
 * read-only paths are closed for writing last. A kept path that holds a hidden one (a
 * workspace at the home directory) opens nothing. Rules go by path, so a file saved into
 * a hidden directory later is hidden too.
 */
export function seatbeltProfile(hidden: string[], keep: string[], readOnly: string[] = []): string {
  const roots = hidden.map(realPath)
  const reopen = keep
    .map(realPath)
    .filter((path) => roots.some((root) => root !== path && inside(path, root)))
  const kept = keep.map(realPath)
  const locked = readOnly.map(realPath)
  const outerLocked = locked.filter((path) => !kept.some((k) => inside(path, k)))
  return [
    '(version 1)',
    '(allow default)',
    seatbeltRule('deny file-read* file-write*', roots),
    seatbeltRule('allow file-read* file-write*', reopen),
    seatbeltRule('deny file-write*', outerLocked),
    seatbeltRule(
      'allow file-write*',
      kept.filter((path) => outerLocked.some((p) => p !== path && inside(path, p))),
    ),
    seatbeltRule(
      'deny file-write*',
      locked.filter((path) => !outerLocked.includes(path)),
    ),
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
    case 'unshare': {
      // Every step the setup takes, on a directory of its own: bash's named descriptors,
      // a mount from a held one, and a read-only remount.
      const dir = mkdtempSync(join(tmpdir(), 'ariadne-probe-'))
      const steps: Mount['kind'][] = ['hold', 'tmpfs', 'bind', 'ro']
      return works(
        unshareArgs(
          steps.map((step) => ({ kind: step, path: dir })),
          ['true'],
          ids(),
        ),
      ).finally(() => rmSync(dir, { recursive: true, force: true }))
    }
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
  /**
   * Paths commands may read but never change, such as the code the server runs; asked
   * for each command, so a plugin path added in the settings is locked from then on.
   */
  readOnly?: () => string[]
}): Sandbox {
  const kind = options.mode === 'off' ? null : options.kind
  const hidden = [...new Set(options.hide.map(realPath))]
  const fixed = options.readOnly ?? (() => [])
  return {
    kind,
    status: { kind, mode: options.mode, hidden, platform: process.platform },
    wrap(argv, { keep, readOnly = [] }) {
      const locked = [...fixed(), ...readOnly]
      switch (kind) {
        case 'bwrap':
          return bwrapArgs(mountPlan(hidden, keep, locked), argv)
        case 'unshare':
          return unshareArgs(mountPlan(hidden, keep, locked), argv, ids())
        case 'sandbox-exec':
          return ['sandbox-exec', '-p', seatbeltProfile(hidden, keep, locked), ...argv]
        default:
          return argv
      }
    },
  }
}

/** Commands run as they are; for tests and for plugins loaded without a host sandbox. */
export const noSandbox: Sandbox = createSandbox({ kind: null, mode: 'off', hide: [] })
