import { readdir, stat } from 'node:fs/promises'
import { homedir } from 'node:os'
import type { posix } from 'node:path'
import nodePath from 'node:path'

/** One directory a listing shows; `path` is ready to use, so callers never join paths. */
export interface DirectoryEntry {
  name: string
  path: string
}

/**
 * The directories in one directory, for picking a path in a form. `path` is the directory
 * listed, `parent` the one above it (`null` at a root). When what was typed is not a
 * directory, its parent is listed, keeping only the names that start like its last part.
 */
export interface DirectoryListing {
  path: string
  parent: string | null
  directories: DirectoryEntry[]
}

/** A listing shows at most this many directories, in name order. */
export const MAX_DIRECTORIES = 500

/** The path functions of the machine a listing is on. */
export type PathFlavor = Pick<typeof posix, 'dirname' | 'basename' | 'isAbsolute' | 'join' | 'sep'>

/**
 * Lists one directory: its real path and the names of the directories in it, or `null`
 * when `path` is not a directory. An empty `path` means the home directory.
 */
export type ListDirectory = (path: string) => Promise<{ path: string; names: string[] } | null>

/**
 * The listing for what a user typed: the directory itself when it is one (or the home
 * directory when nothing absolute was typed), else its parent, filtered by the last part.
 * Names starting with a dot show only when the typed part starts with one too.
 */
export async function browseTyped(
  typed: string,
  list: ListDirectory,
  flavor: PathFlavor = nodePath,
): Promise<DirectoryListing> {
  const text = typed.trim()
  const absolute = text !== '' && flavor.isAbsolute(text)
  // A lone dot at the end asks for the hidden directories, not for the directory itself.
  const dot = absolute && flavor.basename(text) === '.' && /[\\/]\.$/.test(text)
  let found = dot ? null : await list(absolute ? text : '')
  let prefix = ''
  const endsWithSep = /[\\/]$/.test(text)
  if (!found && absolute && !endsWithSep) {
    prefix = flavor.basename(text)
    found = await list(flavor.dirname(text))
  }
  if (!found) throw new DirectoryNotFound(text)
  const lower = prefix.toLowerCase()
  const directories = found.names
    .filter((name) => name !== '.' && name !== '..')
    .filter((name) => (prefix.startsWith('.') ? true : !name.startsWith('.')))
    .filter((name) => !lower || name.toLowerCase().startsWith(lower))
    .toSorted((a, b) => a.localeCompare(b, undefined, { numeric: true, sensitivity: 'base' }))
    .slice(0, MAX_DIRECTORIES)
    .map((name) => ({ name, path: flavor.join(found.path, name) }))
  const above = flavor.dirname(found.path)
  return { path: found.path, parent: above === found.path ? null : above, directories }
}

/**
 * There is nothing to list yet, which is no error to show: the settings do not name a
 * machine yet (no host typed), or what was typed is not a path there.
 */
export class NothingToBrowse extends Error {
  constructor(reason: string) {
    super(reason)
    this.name = 'NothingToBrowse'
  }
}

/** Neither what was typed nor the directory above it is a directory one can list. */
export class DirectoryNotFound extends NothingToBrowse {
  constructor(readonly path: string) {
    super(`${path || 'the home directory'} is not a directory`)
    this.name = 'DirectoryNotFound'
  }
}

/**
 * Lists directories on this machine. The path stays as typed (only tidied): links in it
 * are not resolved, so picking keeps the prefix the user knows.
 */
export const listLocalDirectory: ListDirectory = async (path) => {
  const real = nodePath.resolve(path || homedir())
  try {
    if (!(await stat(real)).isDirectory()) return null
    const entries = await readdir(real, { withFileTypes: true })
    const names: string[] = []
    for (const entry of entries) {
      if (entry.isDirectory()) names.push(entry.name)
      else if (entry.isSymbolicLink()) {
        // A link to a directory is one to the user.
        const info = await stat(nodePath.join(real, entry.name)).catch(() => null)
        if (info?.isDirectory()) names.push(entry.name)
      }
    }
    return { path: real, names }
  } catch {
    return null
  }
}

/** The listing for what a user typed, on this machine. */
export const browseLocal = (typed: string) => browseTyped(typed, listLocalDirectory)
