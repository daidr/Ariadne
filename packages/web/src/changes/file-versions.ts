import type { FileVersions } from '@ariadne/plugin-sdk'
import { api } from '../api'

const MAX_BYTES = 8 * 1024 * 1024
const MAX_FILES = 32
const TTL = 2 * 60 * 1000

/** A cache belongs to one immutable comparison in one view. */
export function fileVersionsFor(threadId: string, from: string, head: string) {
  const cache = new Map<string, { file: FileVersions; bytes: number; expires: number }>()
  let bytes = 0
  function remove(key: string) {
    const entry = cache.get(key)
    if (!entry) return
    bytes -= entry.bytes
    cache.delete(key)
  }

  return async (path: string, previousPath: string | null, signal: AbortSignal) => {
    const key = JSON.stringify([path, previousPath])
    const now = Date.now()
    for (const [id, entry] of cache) if (entry.expires <= now) remove(id)
    const cached = cache.get(key)
    if (cached) {
      cache.delete(key)
      cache.set(key, cached)
      return cached.file
    }

    const { file } = await api.getChangedFile(threadId, { from, head, path, previousPath }, signal)
    if (signal.aborted || (!file.skipped && file.before === null && file.after === null))
      return file
    const size = 2 * ((file.before?.length ?? 0) + (file.after?.length ?? 0) + key.length)
    if (size > MAX_BYTES) return file
    remove(key)
    while (cache.size >= MAX_FILES || bytes + size > MAX_BYTES) {
      const oldest = cache.keys().next()
      if (oldest.done) break
      remove(oldest.value)
    }
    bytes += size
    cache.set(key, { file, bytes: size, expires: Date.now() + TTL })
    return file
  }
}
