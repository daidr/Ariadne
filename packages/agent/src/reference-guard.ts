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
  '.config/git/credentials',
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

/**
 * The parts of a path on another machine once `.` and `..` are worked out, either
 * separator, `~` left in and a drive letter dropped; `null` when it climbs out of where
 * it starts, so where it leads cannot be told from its spelling.
 */
function partsOf(path: string): string[] | null {
  const typed = path.replaceAll('\\', '/')
  const rooted = typed.startsWith('/')
  const parts: string[] = []
  for (const part of typed.split('/')) {
    if (!part || part === '.') continue
    if (part !== '..') parts.push(part)
    // Above the root is the root; above `~`, a drive or wherever a relative path starts
    // is somewhere its spelling does not say.
    else if (parts.length && parts.at(-1) !== '~' && !/^[A-Za-z]:$/.test(parts.at(-1)!)) parts.pop()
    else if (!rooted || parts.length) return null
  }
  if (/^[A-Za-z]:$/.test(parts[0] ?? '')) parts.shift()
  return parts
}

/** The directories on the way to a credential path: each holds one. */
const HOLDERS = [
  ...new Set(
    CREDENTIALS.flatMap((name) => {
      const parts = name.split('/')
      return parts.slice(1).map((_, at) => parts.slice(0, at + 1).join('/'))
    }),
  ),
]

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
 * The same for a path on another machine, by its spelling alone, links there not
 * followed: its root, a home directory, a path through one of the credential directories,
 * or a directory that ends where one of them begins (`~/.config`).
 */
export function remoteRefusal(path: string): string | null {
  const bare = partsOf(path.trim())
  if (!bare) {
    return `${path} climbs out of where it starts with "..": spell the directory out`
  }
  const spelled = `/${bare.join('/')}/`
  if (
    !bare.length ||
    (bare.length === 1 && ['~', 'root', 'home', 'Users'].includes(bare[0]!)) ||
    (bare.length === 2 && (bare[0] === 'home' || bare[0] === 'Users')) ||
    HOLDERS.some((name) => spelled.endsWith(`/${name}/`))
  ) {
    return `${path} is too wide: ask for the one directory or file the task needs`
  }
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
