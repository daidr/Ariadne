import { createHash } from 'node:crypto'
import { posix } from 'node:path'

/**
 * What each thread last saw of the files it read or wrote, by a hash of their text: so a
 * write over a file that changed since (by the user, a command, another tool) is caught
 * instead of undoing that change unseen. Kept in memory: after a restart nothing is known,
 * and nothing is refused.
 */
const seen = new Map<string, Map<string, string>>()
/** Threads remembered at most; the longest unused go first. */
const MAX_THREADS = 200

const hash = (text: string) => createHash('sha256').update(text).digest('hex')
const key = (path: string) => posix.normalize(path.replace(/\\/g, '/')).replace(/^\.\//, '')

/** The thread saw `text` as the content of `path`, by reading or writing it. */
export function noteFile(threadId: string, path: string, text: string): void {
  let files = seen.get(threadId)
  if (files) seen.delete(threadId)
  else files = new Map()
  seen.set(threadId, files)
  files.set(key(path), hash(text))
  if (seen.size > MAX_THREADS) seen.delete(seen.keys().next().value!)
}

/**
 * Whether `path` holds something else now (`text`) than when the thread last read or
 * wrote it; `false` when it never did.
 */
export function changedSince(threadId: string, path: string, text: string): boolean {
  const known = seen.get(threadId)?.get(key(path))
  return known !== undefined && known !== hash(text)
}
