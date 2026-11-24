import type { FileVersions } from '@ariadne/plugin-sdk'
import { api } from '../api'

const MAX_BYTES = 24 * 1024 * 1024
const MAX_FILES = 128
const TTL = 2 * 60 * 1000
const BATCH_SIZE = 24

export type FileVersionLoader = ((
  path: string,
  previousPath: string | null,
  signal: AbortSignal,
) => Promise<FileVersions>) & {
  peek: (path: string, previousPath: string | null) => FileVersions | null
  prefetch: (files: readonly { path: string; previousPath?: string | null }[]) => void
}

interface QueuedRequest {
  key: string
  path: string
  previousPath: string | null
  resolve: (file: FileVersions) => void
  reject: (error: unknown) => void
}

function abortReason(signal: AbortSignal): unknown {
  return signal.reason ?? new DOMException('Aborted', 'AbortError')
}

function awaitWithSignal<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  if (signal.aborted) return Promise.reject(abortReason(signal))
  return new Promise<T>((resolve, reject) => {
    const onAbort = () => reject(abortReason(signal))
    signal.addEventListener('abort', onAbort, { once: true })
    promise.then(
      (value) => {
        signal.removeEventListener('abort', onAbort)
        resolve(value)
      },
      (error) => {
        signal.removeEventListener('abort', onAbort)
        reject(error)
      },
    )
  })
}

/** A cache belongs to one immutable comparison in one view. */
export function fileVersionsFor(
  threadId: string,
  from: string,
  head: string,
  scope?: { sourceId?: string; repoPath?: string },
): FileVersionLoader {
  const cache = new Map<string, { file: FileVersions; bytes: number; expires: number }>()
  const inFlight = new Map<string, Promise<FileVersions>>()
  let pending: QueuedRequest[] = []
  let scheduled = false
  let bytes = 0

  function remove(key: string) {
    const entry = cache.get(key)
    if (!entry) return
    bytes -= entry.bytes
    cache.delete(key)
  }

  function pruneExpired() {
    const now = Date.now()
    for (const [id, entry] of cache) if (entry.expires <= now) remove(id)
  }

  function peek(path: string, previousPath: string | null): FileVersions | null {
    pruneExpired()
    const key = JSON.stringify([path, previousPath])
    const cached = cache.get(key)
    if (!cached) return null
    cache.delete(key)
    cache.set(key, cached)
    return cached.file
  }

  function remember(key: string, file: FileVersions) {
    if (!file.skipped && file.before === null && file.after === null) return
    const size = 2 * ((file.before?.length ?? 0) + (file.after?.length ?? 0) + key.length)
    if (size > MAX_BYTES) return
    remove(key)
    while (cache.size >= MAX_FILES || bytes + size > MAX_BYTES) {
      const oldest = cache.keys().next()
      if (oldest.done) break
      remove(oldest.value)
    }
    bytes += size
    cache.set(key, { file, bytes: size, expires: Date.now() + TTL })
  }

  function scheduleFlush() {
    if (scheduled) return
    scheduled = true
    queueMicrotask(flush)
  }

  function flush() {
    scheduled = false
    const queued = pending
    pending = []
    if (!queued.length) return

    for (let start = 0; start < queued.length; start += BATCH_SIZE) {
      const chunk = queued.slice(start, start + BATCH_SIZE)
      if (chunk.length === 1) {
        const item = chunk[0]!
        void api
          .getChangedFile(threadId, {
            from,
            head,
            path: item.path,
            previousPath: item.previousPath,
            ...(scope?.sourceId ? { sourceId: scope.sourceId } : {}),
            ...(scope?.repoPath ? { repoPath: scope.repoPath } : {}),
          })
          .then(({ file }) => {
            remember(item.key, file)
            item.resolve(file)
          })
          .catch((error) => {
            item.reject(error)
          })
          .finally(() => {
            inFlight.delete(item.key)
          })
        continue
      }

      void api
        .getChangedFiles(threadId, {
          from,
          head,
          files: chunk.map((item) => ({ path: item.path, previousPath: item.previousPath })),
          ...(scope?.sourceId ? { sourceId: scope.sourceId } : {}),
          ...(scope?.repoPath ? { repoPath: scope.repoPath } : {}),
        })
        .then(({ files }) => {
          chunk.forEach((item, index) => {
            const file = files[index] ?? { before: null, after: null, skipped: null }
            remember(item.key, file)
            item.resolve(file)
          })
        })
        .catch((error) => {
          chunk.forEach((item) => item.reject(error))
        })
        .finally(() => {
          chunk.forEach((item) => inFlight.delete(item.key))
        })
    }
  }

  function enqueue(path: string, previousPath: string | null) {
    const key = JSON.stringify([path, previousPath])
    const existing = inFlight.get(key)
    if (existing) return existing

    const promise = new Promise<FileVersions>((resolve, reject) => {
      pending.push({ key, path, previousPath, resolve, reject })
      scheduleFlush()
    })
    inFlight.set(key, promise)
    return promise
  }

  const load = (async (path: string, previousPath: string | null, signal: AbortSignal) => {
    if (signal.aborted) throw abortReason(signal)
    const cached = peek(path, previousPath)
    if (cached) return cached
    return await awaitWithSignal(enqueue(path, previousPath), signal)
  }) as FileVersionLoader

  load.peek = peek
  load.prefetch = (files) => {
    if (!from || !head || !files.length) return
    pruneExpired()
    for (const file of files) {
      const previousPath = file.previousPath ?? null
      const key = JSON.stringify([file.path, previousPath])
      if (cache.has(key) || inFlight.has(key)) continue
      void enqueue(file.path, previousPath).catch(() => undefined)
    }
  }

  return load
}
