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

/**
 * The reads whose results each conversation still has in its context, by file, range and
 * a hash of the text: reading the same again while nothing changed hands back a line in
 * place of the same text. Forgotten when older results are cleared or summarised, since
 * the model no longer has them then.
 */
const reads = new Map<string, Map<string, string>>()

const readKey = (path: string, offset: number, limit: number) => `${key(path)}\0${offset}\0${limit}`

/**
 * Note that the conversation now has the text of `path` from `offset`, `limit` lines, in its
 * context; `true` when it already had exactly that text from the same read.
 */
export function noteRead(
  conversationId: string,
  path: string,
  offset: number,
  limit: number,
  text: string,
): boolean {
  let files = reads.get(conversationId)
  if (files) reads.delete(conversationId)
  else files = new Map()
  reads.set(conversationId, files)
  const at = readKey(path, offset, limit)
  const sum = hash(text)
  const again = files.get(at) === sum
  files.set(at, sum)
  if (reads.size > MAX_THREADS) reads.delete(reads.keys().next().value!)
  return again
}

/** What each conversation's check after edits printed when it last failed. */
const failures = new Map<string, string>()

/**
 * Note how the check after edits ended for the conversation: its output when it failed,
 * `null` when it passed. `true` when it failed with the same output the last time too.
 */
export function noteCheckFailure(conversationId: string, output: string | null): boolean {
  const again = output !== null && failures.get(conversationId) === output
  failures.delete(conversationId)
  if (output !== null) failures.set(conversationId, output)
  if (failures.size > MAX_THREADS) failures.delete(failures.keys().next().value!)
  return again
}

/**
 * The conversation's older results were cleared or summarised: it no longer has what it
 * read, nor the last failing check's output.
 */
export function forgetReads(conversationId: string): void {
  reads.delete(conversationId)
  failures.delete(conversationId)
}
