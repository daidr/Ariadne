import type { ContextProvider } from '@ariadne/plugin-sdk'
import { realPath } from '@ariadne/sandbox'
import { homedir } from 'node:os'
import { join, relative } from 'node:path'

/**
 * Where credentials usually live under a home directory. A conversation never gets one of
 * them, or a directory that holds one, by asking: the user adds such a source by hand.
 */
const CREDENTIALS = [
  '.ssh',
  '.gnupg',
  '.aws',
  '.azure',
  '.kube',
  '.docker',
  '.netrc',
  '.npmrc',
  '.pypirc',
  '.git-credentials',
  '.password-store',
  '.config/gh',
  '.config/glab-cli',
  '.config/gcloud',
  '.config/op',
  '.local/share/keyrings',
  'Library/Keychains',
]

const inside = (path: string, dir: string) => {
  const rel = relative(dir, path)
  return rel === '' || (!rel.startsWith('..') && !/^([\\/]|[A-Za-z]:)/.test(rel))
}

/** The parts of a path as typed, either separator, `~` and drive letters left in. */
const partsOf = (path: string) => path.split(/[\\/]+/).filter(Boolean)

/**
 * Why a conversation may not ask for this path on the server's machine, or `null` when
 * it may: a path in a protected place (what the sandbox hides, where credentials live), or
 * so wide that it holds one, as the home directory and the root do.
 */
export function localRefusal(
  path: string,
  hidden: readonly string[],
  home = homedir(),
): string | null {
  const real = realPath(path)
  const protectedPaths = [
    ...hidden.map(realPath),
    ...CREDENTIALS.map((name) => realPath(join(home, name))),
  ]
  if (protectedPaths.some((kept) => inside(real, kept))) {
    return `${path} is kept out of reach: credentials and Ariadne's own data are never read this way`
  }
  const held = protectedPaths.find((kept) => inside(kept, real))
  // The home directory holds them whether or not they exist yet.
  if (held || inside(realPath(home), real)) {
    return `${path} is too wide: it holds places that are kept out of reach. Ask for the one directory or file the task needs`
  }
  return null
}

/**
 * The same for a path on another machine, by its spelling alone: its root, a home
 * directory, or a path through one of the credential directories.
 */
export function remoteRefusal(path: string): string | null {
  const parts = partsOf(path.trim())
  const bare = parts.filter((part) => part !== '.')
  if (
    !bare.length ||
    (bare.length === 1 && ['~', 'root', 'home', 'Users'].includes(bare[0]!)) ||
    (bare.length === 2 && (bare[0] === 'home' || bare[0] === 'Users'))
  ) {
    return `${path} is too wide: ask for the one directory or file the task needs`
  }
  const spelled = `/${bare.join('/')}/`
  if (CREDENTIALS.some((name) => spelled.includes(`/${name}/`))) {
    return `${path} is kept out of reach: credentials are never read this way`
  }
  return null
}

/**
 * Why a conversation may not ask to read this source, or `null`: every setting of it that
 * is a directory is checked, on this machine by where it really leads and on another by
 * its spelling. What the user adds by hand in the project's settings is not checked.
 */
export function referenceRefusal(
  provider: Pick<ContextProvider<never>, 'settings' | 'remote'>,
  settings: Record<string, unknown>,
  hidden: readonly string[],
): string | null {
  for (const [name, field] of Object.entries(provider.settings.properties)) {
    if (field.type !== 'string' || !field.directory) continue
    const path = settings[name]
    if (typeof path !== 'string' || !path.trim()) continue
    const refusal = provider.remote ? remoteRefusal(path) : localRefusal(path, hidden)
    if (refusal) return refusal
  }
  return null
}
