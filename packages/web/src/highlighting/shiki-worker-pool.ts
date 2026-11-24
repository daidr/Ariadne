import { resolveShikiLanguage, type ShikiLanguageKey } from './languages'
import type { ShikiHighlightResponse, ShikiWorkerPort } from './shiki-worker-protocol'

export type HighlightPriority = 'high' | 'normal' | 'low'

export interface HighlightOptions {
  signal?: AbortSignal
  priority?: HighlightPriority
}

export interface ShikiWorkerPoolOptions {
  /** Factory for creating a single worker instance. Defaults to a Vite module Web Worker. */
  createWorker?: () => ShikiWorkerPort | null
  /** Fallback highlighter used when Web Workers are unavailable or fail. */
  fallback?: (code: string, lang: ShikiLanguageKey) => Promise<string>
  /** Maximum number of workers in the pool. Defaults to `resolveHighlightPoolSize()`. */
  maxWorkers?: number
  /** Idle timeout before surplus workers are terminated. Defaults to 30_000 ms; 0 disables eviction. */
  idleTimeoutMs?: number
  /** Minimum number of warm workers kept alive across idle periods. Defaults to 1. */
  minIdleWorkers?: number
  /** Maximum entries retained in the LRU cache. Defaults to 200. */
  maxCacheEntries?: number
  /** Maximum cumulative HTML character budget in the LRU cache. Defaults to 2_000_000. */
  maxCacheChars?: number
  /** Largest single HTML entry eligible for caching. Defaults to 250_000. */
  maxSingleEntryChars?: number
  /** Maximum pending queue depth before evicting the stalest low-priority task. Defaults to 64. */
  maxQueueSize?: number
}

export interface ShikiWorkerPoolStats {
  activeWorkers: number
  busyWorkers: number
  queuedTasks: number
  inFlightTasks: number
  cacheEntries: number
  cacheChars: number
  maxWorkers: number
  fallbackMode: boolean
}

export interface ShikiWorkerPool {
  peek(code: string, lang: string): string | null
  highlight(code: string, lang: string, options?: HighlightOptions): Promise<string>
  clearCache(): void
  dispose(): void
  stats(): ShikiWorkerPoolStats
}

const PRIORITY_RANK: Record<HighlightPriority, number> = {
  low: 0,
  normal: 1,
  high: 2,
}

const DEFAULT_MAX_WORKERS_CAP = 4
const DEFAULT_IDLE_TIMEOUT_MS = 30_000
const DEFAULT_MIN_IDLE_WORKERS = 1
const DEFAULT_MAX_CACHE_ENTRIES = 200
const DEFAULT_MAX_CACHE_CHARS = 2_000_000
const DEFAULT_MAX_SINGLE_ENTRY_CHARS = 250_000
const DEFAULT_MAX_QUEUE_SIZE = 64
const MAX_CONSECUTIVE_WORKER_FAILURES = 2

/** Compute a conservative worker pool upper bound from `navigator.hardwareConcurrency`. */
export function resolveHighlightPoolSize(
  hardwareConcurrency?: number | null,
  cap = DEFAULT_MAX_WORKERS_CAP,
): number {
  const safeCap = Math.max(1, Math.floor(cap))
  const detected =
    hardwareConcurrency === undefined
      ? typeof navigator !== 'undefined'
        ? navigator.hardwareConcurrency
        : undefined
      : hardwareConcurrency
  const cores =
    typeof detected === 'number' && Number.isFinite(detected) && detected > 0
      ? Math.floor(detected)
      : 4
  return Math.max(1, Math.min(safeCap, Math.floor(cores / 2)))
}

function createAbortError(): Error {
  if (typeof DOMException !== 'undefined') {
    return new DOMException('Highlight request aborted', 'AbortError')
  }
  const error = new Error('Highlight request aborted')
  error.name = 'AbortError'
  return error
}

function createDefaultShikiWorker(): ShikiWorkerPort | null {
  if (typeof Worker === 'undefined') return null
  return new Worker(new URL('./shiki.worker.ts', import.meta.url), {
    type: 'module',
    name: 'ariadne-shiki',
  }) as unknown as ShikiWorkerPort
}

async function defaultShikiFallback(code: string, lang: ShikiLanguageKey): Promise<string> {
  const { highlightCodeToHtml } = await import('./shiki')
  return highlightCodeToHtml(code, lang)
}

interface Waiter {
  resolve: (html: string) => void
  reject: (reason?: unknown) => void
  signal?: AbortSignal
  onAbort?: () => void
}

interface TaskRecord {
  id: number
  cacheKey: string
  code: string
  lang: ShikiLanguageKey
  priority: number
  seq: number
  state: 'queued' | 'running' | 'settled'
  retries: number
  waiters: Set<Waiter>
}

interface WorkerSlot {
  port: ShikiWorkerPort
  busy: boolean
  healthy: boolean
  currentTask: TaskRecord | null
  loadedLanguages: Set<ShikiLanguageKey>
  idleTimer: ReturnType<typeof setTimeout> | null
  onMessage: (event: MessageEvent<ShikiHighlightResponse>) => void
  onError: (event: ErrorEvent) => void
}

function keyOf(lang: ShikiLanguageKey, code: string): string {
  return `${lang}\0${code}`
}

function detachWaiter(waiter: Waiter): void {
  if (waiter.signal && waiter.onAbort) {
    waiter.signal.removeEventListener('abort', waiter.onAbort)
    waiter.onAbort = undefined
  }
}

export function createShikiWorkerPool(options: ShikiWorkerPoolOptions = {}): ShikiWorkerPool {
  const createWorker = options.createWorker ?? createDefaultShikiWorker
  const fallback = options.fallback ?? defaultShikiFallback
  const maxWorkers = Math.max(1, Math.floor(options.maxWorkers ?? resolveHighlightPoolSize()))
  const idleTimeoutMs = Math.max(0, Math.floor(options.idleTimeoutMs ?? DEFAULT_IDLE_TIMEOUT_MS))
  const minIdleWorkers = Math.max(
    0,
    Math.min(maxWorkers, Math.floor(options.minIdleWorkers ?? DEFAULT_MIN_IDLE_WORKERS)),
  )
  const maxCacheEntries = Math.max(
    0,
    Math.floor(options.maxCacheEntries ?? DEFAULT_MAX_CACHE_ENTRIES),
  )
  const maxCacheChars = Math.max(0, Math.floor(options.maxCacheChars ?? DEFAULT_MAX_CACHE_CHARS))
  const maxSingleEntryChars = Math.min(
    maxCacheChars,
    Math.max(0, Math.floor(options.maxSingleEntryChars ?? DEFAULT_MAX_SINGLE_ENTRY_CHARS)),
  )
  const maxQueueSize = Math.max(1, Math.floor(options.maxQueueSize ?? DEFAULT_MAX_QUEUE_SIZE))

  const cache = new Map<string, string>()
  let cacheChars = 0

  const inFlight = new Map<string, TaskRecord>()
  const queue: TaskRecord[] = []
  const slots: WorkerSlot[] = []

  let nextRequestId = 1
  let nextSeq = 1
  let consecutiveWorkerFailures = 0
  let workerUnavailable = false
  let disposed = false

  function readCache(cacheKey: string): string | null {
    const hit = cache.get(cacheKey)
    if (hit === undefined) return null
    // Refresh recency for LRU eviction.
    cache.delete(cacheKey)
    cache.set(cacheKey, hit)
    return hit
  }

  function writeCache(cacheKey: string, html: string): void {
    if (!html || maxCacheEntries === 0 || maxCacheChars === 0) return
    if (html.length > maxSingleEntryChars || html.length > maxCacheChars) return

    const existing = cache.get(cacheKey)
    if (existing !== undefined) {
      cacheChars -= existing.length
      cache.delete(cacheKey)
    }

    cache.set(cacheKey, html)
    cacheChars += html.length

    while (cache.size > maxCacheEntries || cacheChars > maxCacheChars) {
      const oldestKey = cache.keys().next().value
      if (oldestKey === undefined) break
      const oldestValue = cache.get(oldestKey)
      cache.delete(oldestKey)
      if (oldestValue !== undefined) cacheChars -= oldestValue.length
    }
  }

  function removeFromQueue(task: TaskRecord): void {
    const index = queue.indexOf(task)
    if (index >= 0) queue.splice(index, 1)
  }

  function settleTask(task: TaskRecord, html: string): void {
    if (task.state === 'settled') return
    task.state = 'settled'
    inFlight.delete(task.cacheKey)
    removeFromQueue(task)
    if (html) writeCache(task.cacheKey, html)
    for (const waiter of task.waiters) {
      detachWaiter(waiter)
      waiter.resolve(html)
    }
    task.waiters.clear()
  }

  function clearIdleTimer(slot: WorkerSlot): void {
    if (slot.idleTimer !== null) {
      clearTimeout(slot.idleTimer)
      slot.idleTimer = null
    }
  }

  function destroySlot(slot: WorkerSlot): void {
    clearIdleTimer(slot)
    const index = slots.indexOf(slot)
    if (index >= 0) slots.splice(index, 1)
    slot.port.removeEventListener('message', slot.onMessage)
    slot.port.removeEventListener('error', slot.onError)
    try {
      slot.port.terminate()
    } catch {
      // Ignore termination errors from already-closed workers.
    }
  }

  function scheduleIdleEviction(slot: WorkerSlot): void {
    clearIdleTimer(slot)
    if (idleTimeoutMs <= 0 || disposed) return
    if (slots.length <= minIdleWorkers) return
    slot.idleTimer = setTimeout(() => {
      slot.idleTimer = null
      if (!slot.busy && slots.length > minIdleWorkers) {
        destroySlot(slot)
      }
    }, idleTimeoutMs)
  }

  function runFallback(task: TaskRecord): void {
    if (task.state === 'settled') return
    if (task.waiters.size === 0) {
      settleTask(task, '')
      return
    }
    task.state = 'running'
    void fallback(task.code, task.lang).then(
      (html) => settleTask(task, html),
      () => settleTask(task, ''),
    )
  }

  function spawnSlot(): WorkerSlot | null {
    if (disposed || workerUnavailable || slots.length >= maxWorkers) return null
    let port: ShikiWorkerPort | null = null
    try {
      port = createWorker()
    } catch {
      workerUnavailable = true
      return null
    }
    if (!port) {
      workerUnavailable = true
      return null
    }

    const slot: WorkerSlot = {
      port,
      busy: false,
      healthy: false,
      currentTask: null,
      loadedLanguages: new Set(),
      idleTimer: null,
      onMessage: (event) => handleSlotMessage(slot, event),
      onError: () => handleSlotError(slot),
    }
    port.addEventListener('message', slot.onMessage)
    port.addEventListener('error', slot.onError)
    slots.push(slot)
    return slot
  }

  function handleSlotMessage(slot: WorkerSlot, event: MessageEvent<ShikiHighlightResponse>): void {
    const response = event.data
    if (!response || typeof response.id !== 'number') return
    const task = slot.currentTask
    if (!task || task.id !== response.id) return

    slot.busy = false
    slot.healthy = true
    slot.currentTask = null
    consecutiveWorkerFailures = 0
    workerUnavailable = false

    if (response.ok) {
      slot.loadedLanguages.add(task.lang)
      settleTask(task, response.html)
    } else if (task.waiters.size === 0) {
      settleTask(task, '')
    } else if (task.retries < 1) {
      task.retries++
      runFallback(task)
    } else {
      settleTask(task, '')
    }

    scheduleIdleEviction(slot)
    pump()
  }

  function handleSlotError(slot: WorkerSlot): void {
    if (!slots.includes(slot)) return
    const task = slot.currentTask
    slot.busy = false
    slot.currentTask = null
    destroySlot(slot)

    consecutiveWorkerFailures++
    if (
      consecutiveWorkerFailures >= MAX_CONSECUTIVE_WORKER_FAILURES &&
      !slots.some((activeSlot) => activeSlot.healthy)
    ) {
      workerUnavailable = true
    }

    if (task && task.state === 'running') {
      if (task.waiters.size === 0) {
        settleTask(task, '')
      } else if (task.retries < 1 && !disposed && !workerUnavailable) {
        task.retries++
        task.state = 'queued'
        queue.push(task)
      } else {
        runFallback(task)
      }
    }

    pump()
  }

  /** Pick the highest-priority, most-recently-requested task (LIFO within a priority tier). */
  function pickNextTaskIndex(): number {
    let bestIndex = -1
    let bestTask: TaskRecord | null = null
    for (let i = 0; i < queue.length; i++) {
      const candidate = queue[i]!
      if (
        !bestTask ||
        candidate.priority > bestTask.priority ||
        (candidate.priority === bestTask.priority && candidate.seq > bestTask.seq)
      ) {
        bestIndex = i
        bestTask = candidate
      }
    }
    return bestIndex
  }

  /** Evict the stalest lowest-priority task when the queue reaches capacity. */
  function evictStalestTask(): void {
    let victimIndex = -1
    let victim: TaskRecord | null = null
    for (let i = 0; i < queue.length; i++) {
      const candidate = queue[i]!
      if (
        !victim ||
        candidate.priority < victim.priority ||
        (candidate.priority === victim.priority && candidate.seq < victim.seq)
      ) {
        victimIndex = i
        victim = candidate
      }
    }
    if (victimIndex >= 0 && victim) {
      queue.splice(victimIndex, 1)
      settleTask(victim, '')
    }
  }

  /**
   * Choose an idle worker, preferring one that already loaded `lang` so TextMate grammars
   * do not have to be duplicated unnecessarily across every worker in the pool.
   */
  function pickIdleSlot(lang: ShikiLanguageKey): WorkerSlot | null {
    let fallbackSlot: WorkerSlot | null = null
    for (const slot of slots) {
      if (slot.busy) continue
      if (slot.loadedLanguages.has(lang)) return slot
      if (!fallbackSlot || slot.loadedLanguages.size < fallbackSlot.loadedLanguages.size) {
        fallbackSlot = slot
      }
    }
    return fallbackSlot
  }

  function pump(): void {
    if (disposed) return

    while (queue.length > 0) {
      const nextIndex = pickNextTaskIndex()
      if (nextIndex < 0) break
      const task = queue[nextIndex]!

      let slot = pickIdleSlot(task.lang)
      if (!slot && slots.length < maxWorkers && !workerUnavailable) {
        slot = spawnSlot()
      }

      if (!slot) {
        if (workerUnavailable && slots.length === 0) {
          queue.splice(nextIndex, 1)
          runFallback(task)
          continue
        }
        break
      }

      queue.splice(nextIndex, 1)
      clearIdleTimer(slot)
      slot.busy = true
      slot.currentTask = task
      task.state = 'running'
      task.id = nextRequestId++

      try {
        // Worker.postMessage takes optional transferables rather than Window's targetOrigin.
        // oxlint-disable-next-line unicorn/require-post-message-target-origin
        slot.port.postMessage({ id: task.id, code: task.code, lang: task.lang })
      } catch {
        handleSlotError(slot)
      }
    }
  }

  function attachWaiter(task: TaskRecord, signal?: AbortSignal): Promise<string> {
    return new Promise<string>((resolve, reject) => {
      const waiter: Waiter = { resolve, reject, signal }
      if (signal) {
        const onAbort = () => {
          detachWaiter(waiter)
          task.waiters.delete(waiter)
          reject(createAbortError())
          if (task.waiters.size === 0 && task.state === 'queued') {
            task.state = 'settled'
            inFlight.delete(task.cacheKey)
            removeFromQueue(task)
          }
        }
        waiter.onAbort = onAbort
        signal.addEventListener('abort', onAbort, { once: true })
      }
      task.waiters.add(waiter)
    })
  }

  function peek(code: string, lang: string): string | null {
    if (!code) return ''
    const key = resolveShikiLanguage(lang)
    if (!key) return null
    return readCache(keyOf(key, code))
  }

  function highlight(
    code: string,
    lang: string,
    requestOptions: HighlightOptions = {},
  ): Promise<string> {
    const { signal, priority = 'normal' } = requestOptions
    if (signal?.aborted) return Promise.reject(createAbortError())
    if (!code) return Promise.resolve('')

    const key = resolveShikiLanguage(lang)
    if (!key) return Promise.resolve('')

    const cacheKey = keyOf(key, code)
    const cached = readCache(cacheKey)
    if (cached !== null) return Promise.resolve(cached)

    if (disposed) {
      return fallback(code, key).catch(() => '')
    }

    const rank = PRIORITY_RANK[priority]
    const existing = inFlight.get(cacheKey)
    if (existing && existing.state !== 'settled') {
      if (existing.state === 'queued') {
        existing.priority = Math.max(existing.priority, rank)
        existing.seq = nextSeq++
      }
      return attachWaiter(existing, signal)
    }

    if (queue.length >= maxQueueSize) {
      evictStalestTask()
    }

    const task: TaskRecord = {
      id: 0,
      cacheKey,
      code,
      lang: key,
      priority: rank,
      seq: nextSeq++,
      state: 'queued',
      retries: 0,
      waiters: new Set(),
    }

    inFlight.set(cacheKey, task)
    queue.push(task)
    const promise = attachWaiter(task, signal)
    pump()
    return promise
  }

  function clearCache(): void {
    cache.clear()
    cacheChars = 0
  }

  function dispose(): void {
    disposed = true
    for (const slot of Array.from(slots)) {
      destroySlot(slot)
    }
    for (const task of Array.from(inFlight.values())) {
      settleTask(task, '')
    }
    queue.length = 0
    inFlight.clear()
    clearCache()
  }

  function stats(): ShikiWorkerPoolStats {
    return {
      activeWorkers: slots.length,
      busyWorkers: slots.filter((slot) => slot.busy).length,
      queuedTasks: queue.length,
      inFlightTasks: inFlight.size,
      cacheEntries: cache.size,
      cacheChars,
      maxWorkers,
      fallbackMode: workerUnavailable && slots.length === 0,
    }
  }

  return {
    peek,
    highlight,
    clearCache,
    dispose,
    stats,
  }
}

/** Shared Shiki Web Worker pool used across chat code blocks, documents, and inspectors. */
export const shikiWorkerPool: ShikiWorkerPool = createShikiWorkerPool()
