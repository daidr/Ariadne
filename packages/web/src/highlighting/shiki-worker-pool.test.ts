import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { createShikiService } from './shiki'
import { createShikiWorkerPool, resolveHighlightPoolSize } from './shiki-worker-pool'
import type {
  ShikiHighlightRequest,
  ShikiHighlightResponse,
  ShikiWorkerPort,
  ShikiWorkerScope,
} from './shiki-worker-protocol'
import { attachShikiWorker } from './shiki-worker-runtime'

class FakeWorkerPort implements ShikiWorkerPort {
  readonly received: ShikiHighlightRequest[] = []
  terminated = false
  private readonly messageListeners = new Set<
    (event: MessageEvent<ShikiHighlightResponse>) => void
  >()
  private readonly errorListeners = new Set<(event: ErrorEvent) => void>()

  postMessage(message: ShikiHighlightRequest): void {
    this.received.push(message)
  }

  addEventListener(
    type: 'message',
    listener: (event: MessageEvent<ShikiHighlightResponse>) => void,
  ): void
  addEventListener(type: 'error', listener: (event: ErrorEvent) => void): void
  addEventListener(type: 'message' | 'error', listener: unknown): void {
    if (type === 'message') {
      this.messageListeners.add(listener as (event: MessageEvent<ShikiHighlightResponse>) => void)
    } else {
      this.errorListeners.add(listener as (event: ErrorEvent) => void)
    }
  }

  removeEventListener(
    type: 'message',
    listener: (event: MessageEvent<ShikiHighlightResponse>) => void,
  ): void
  removeEventListener(type: 'error', listener: (event: ErrorEvent) => void): void
  removeEventListener(type: 'message' | 'error', listener: unknown): void {
    if (type === 'message') {
      this.messageListeners.delete(
        listener as (event: MessageEvent<ShikiHighlightResponse>) => void,
      )
    } else {
      this.errorListeners.delete(listener as (event: ErrorEvent) => void)
    }
  }

  terminate(): void {
    this.terminated = true
  }

  emitMessage(data: ShikiHighlightResponse): void {
    const event = { data } as MessageEvent<ShikiHighlightResponse>
    for (const listener of Array.from(this.messageListeners)) listener(event)
  }

  emitError(message = 'worker crashed'): void {
    const event = { message } as ErrorEvent
    for (const listener of Array.from(this.errorListeners)) listener(event)
  }
}

function createInProcessShikiPort(): ShikiWorkerPort {
  const shiki = createShikiService(() => createJavaScriptRegexEngine())
  const workerMessageListeners = new Set<(event: MessageEvent<ShikiHighlightRequest>) => void>()
  const hostMessageListeners = new Set<(event: MessageEvent<ShikiHighlightResponse>) => void>()

  const scope: ShikiWorkerScope = {
    postMessage(message) {
      queueMicrotask(() => {
        const event = { data: message } as MessageEvent<ShikiHighlightResponse>
        for (const listener of Array.from(hostMessageListeners)) listener(event)
      })
    },
    addEventListener(_type, listener) {
      workerMessageListeners.add(listener)
    },
  }

  attachShikiWorker(scope, shiki)

  return {
    postMessage(message) {
      queueMicrotask(() => {
        const event = { data: message } as MessageEvent<ShikiHighlightRequest>
        for (const listener of Array.from(workerMessageListeners)) listener(event)
      })
    },
    addEventListener(type: 'message' | 'error', listener: unknown) {
      if (type === 'message') {
        hostMessageListeners.add(listener as (event: MessageEvent<ShikiHighlightResponse>) => void)
      }
    },
    removeEventListener(type: 'message' | 'error', listener: unknown) {
      if (type === 'message') {
        hostMessageListeners.delete(
          listener as (event: MessageEvent<ShikiHighlightResponse>) => void,
        )
      }
    },
    terminate() {
      workerMessageListeners.clear()
      hostMessageListeners.clear()
    },
  }
}

afterEach(() => {
  vi.useRealTimers()
})

describe('Shiki Web Worker pool', () => {
  test('computes a conservative pool size from hardwareConcurrency', () => {
    expect(resolveHighlightPoolSize(1)).toBe(1)
    expect(resolveHighlightPoolSize(2)).toBe(1)
    expect(resolveHighlightPoolSize(4)).toBe(2)
    expect(resolveHighlightPoolSize(6)).toBe(3)
    expect(resolveHighlightPoolSize(8)).toBe(4)
    expect(resolveHighlightPoolSize(32)).toBe(4)
    expect(resolveHighlightPoolSize(null)).toBe(2)
    expect(resolveHighlightPoolSize(0)).toBe(2)
    expect(resolveHighlightPoolSize(Number.NaN)).toBe(2)
  })

  test('lazily spawns workers up to maxWorkers and does not start any for empty or unsupported languages', async () => {
    const workers: FakeWorkerPort[] = []
    const pool = createShikiWorkerPool({
      maxWorkers: 2,
      idleTimeoutMs: 0,
      createWorker: () => {
        const port = new FakeWorkerPort()
        workers.push(port)
        return port
      },
    })

    expect(pool.stats().activeWorkers).toBe(0)
    expect(await pool.highlight('', 'ts')).toBe('')
    expect(await pool.highlight('plain', 'unknown-lang')).toBe('')
    expect(workers).toHaveLength(0)

    const p1 = pool.highlight('const a = 1', 'ts')
    expect(workers).toHaveLength(1)

    const p2 = pool.highlight('const b = 2', 'ts')
    expect(workers).toHaveLength(2)

    const p3 = pool.highlight('const c = 3', 'ts')
    expect(workers).toHaveLength(2)
    expect(pool.stats().queuedTasks).toBe(1)

    workers[0]!.emitMessage({
      id: workers[0]!.received[0]!.id,
      ok: true,
      html: '<span class="line">a</span>',
    })
    expect(await p1).toBe('<span class="line">a</span>')
    expect(workers[0]!.received).toHaveLength(2)

    workers[1]!.emitMessage({
      id: workers[1]!.received[0]!.id,
      ok: true,
      html: '<span class="line">b</span>',
    })
    workers[0]!.emitMessage({
      id: workers[0]!.received[1]!.id,
      ok: true,
      html: '<span class="line">c</span>',
    })
    expect(await p2).toBe('<span class="line">b</span>')
    expect(await p3).toBe('<span class="line">c</span>')

    pool.dispose()
  })

  test('deduplicates concurrent identical requests and serves cached results synchronously via peek', async () => {
    const workers: FakeWorkerPort[] = []
    const pool = createShikiWorkerPool({
      maxWorkers: 2,
      idleTimeoutMs: 0,
      createWorker: () => {
        const port = new FakeWorkerPort()
        workers.push(port)
        return port
      },
    })

    const p1 = pool.highlight('const x = 1', 'ts')
    const p2 = pool.highlight('const x = 1', 'typescript')

    expect(workers).toHaveLength(1)
    expect(workers[0]!.received).toHaveLength(1)

    const req = workers[0]!.received[0]!
    workers[0]!.emitMessage({ id: req.id, ok: true, html: '<span class="line">x</span>' })

    expect(await p1).toBe('<span class="line">x</span>')
    expect(await p2).toBe('<span class="line">x</span>')

    // Normalized alias hits the same cache synchronously without posting to any worker.
    expect(pool.peek('const x = 1', 'ts')).toBe('<span class="line">x</span>')
    expect(pool.peek('const x = 1', 'TypeScript')).toBe('<span class="line">x</span>')
    expect(await pool.highlight('const x = 1', 'ts')).toBe('<span class="line">x</span>')
    expect(workers[0]!.received).toHaveLength(1)

    pool.dispose()
  })

  test('evicts least-recently-used entries when cache bounds are exceeded', async () => {
    const workers: FakeWorkerPort[] = []
    const pool = createShikiWorkerPool({
      maxWorkers: 1,
      idleTimeoutMs: 0,
      maxCacheEntries: 2,
      createWorker: () => {
        const port = new FakeWorkerPort()
        workers.push(port)
        return port
      },
    })

    const run = async (code: string, html: string) => {
      const promise = pool.highlight(code, 'ts')
      const req = workers[0]!.received.at(-1)!
      workers[0]!.emitMessage({ id: req.id, ok: true, html })
      return promise
    }

    await run('one', '<span class="line">1</span>')
    await run('two', '<span class="line">2</span>')
    // Touch "one" so "two" becomes the oldest entry.
    expect(pool.peek('one', 'ts')).toBe('<span class="line">1</span>')

    await run('three', '<span class="line">3</span>')
    expect(pool.peek('one', 'ts')).toBe('<span class="line">1</span>')
    expect(pool.peek('two', 'ts')).toBeNull()
    expect(pool.peek('three', 'ts')).toBe('<span class="line">3</span>')

    pool.dispose()
  })

  test('schedules queued requests by priority and LIFO order for fast scrolling, and evicts stalest overflow', async () => {
    const workers: FakeWorkerPort[] = []
    const pool = createShikiWorkerPool({
      maxWorkers: 1,
      idleTimeoutMs: 0,
      maxQueueSize: 3,
      createWorker: () => {
        const port = new FakeWorkerPort()
        workers.push(port)
        return port
      },
    })

    // Occupy the single worker.
    const running = pool.highlight('running', 'ts')

    // Queue four tasks; capacity is 3 so the stalest low/normal one ("first") is evicted smoothly.
    const first = pool.highlight('first', 'ts', { priority: 'low' })
    const second = pool.highlight('second', 'ts', { priority: 'normal' })
    const third = pool.highlight('third', 'ts', { priority: 'normal' })
    const fourth = pool.highlight('fourth', 'ts', { priority: 'high' })

    expect(await first).toBe('')

    const worker = workers[0]!
    // Finish "running" -> "fourth" (high priority) should run next.
    worker.emitMessage({ id: worker.received[0]!.id, ok: true, html: '<span>running</span>' })
    expect(await running).toBe('<span>running</span>')
    expect(worker.received[1]!.code).toBe('fourth')

    // Finish "fourth" -> "third" (newer normal task, LIFO) should run before "second".
    worker.emitMessage({ id: worker.received[1]!.id, ok: true, html: '<span>fourth</span>' })
    expect(await fourth).toBe('<span>fourth</span>')
    expect(worker.received[2]!.code).toBe('third')

    worker.emitMessage({ id: worker.received[2]!.id, ok: true, html: '<span>third</span>' })
    expect(await third).toBe('<span>third</span>')
    expect(worker.received[3]!.code).toBe('second')

    worker.emitMessage({ id: worker.received[3]!.id, ok: true, html: '<span>second</span>' })
    expect(await second).toBe('<span>second</span>')

    pool.dispose()
  })

  test('cancels queued tasks via AbortSignal without affecting other deduplicated waiters', async () => {
    const workers: FakeWorkerPort[] = []
    const pool = createShikiWorkerPool({
      maxWorkers: 1,
      idleTimeoutMs: 0,
      createWorker: () => {
        const port = new FakeWorkerPort()
        workers.push(port)
        return port
      },
    })

    const running = pool.highlight('busy', 'ts')

    const soloAbort = new AbortController()
    const solo = pool.highlight('drop-me', 'ts', { signal: soloAbort.signal })
    soloAbort.abort()
    await expect(solo).rejects.toMatchObject({ name: 'AbortError' })

    const sharedAbort = new AbortController()
    const sharedCancelled = pool.highlight('keep-me', 'ts', { signal: sharedAbort.signal })
    const sharedAlive = pool.highlight('keep-me', 'ts')
    sharedAbort.abort()
    await expect(sharedCancelled).rejects.toMatchObject({ name: 'AbortError' })

    const worker = workers[0]!
    worker.emitMessage({ id: worker.received[0]!.id, ok: true, html: '<span>busy</span>' })
    expect(await running).toBe('<span>busy</span>')

    // "drop-me" was removed before dispatch; only "keep-me" is posted next.
    expect(worker.received).toHaveLength(2)
    expect(worker.received[1]!.code).toBe('keep-me')
    worker.emitMessage({ id: worker.received[1]!.id, ok: true, html: '<span>keep</span>' })
    expect(await sharedAlive).toBe('<span>keep</span>')

    pool.dispose()
  })

  test('routes requests with language affinity and evicts surplus idle workers', async () => {
    vi.useFakeTimers()
    const workers: FakeWorkerPort[] = []
    const pool = createShikiWorkerPool({
      maxWorkers: 2,
      minIdleWorkers: 1,
      idleTimeoutMs: 5_000,
      createWorker: () => {
        const port = new FakeWorkerPort()
        workers.push(port)
        return port
      },
    })

    const pTs = pool.highlight('const a = 1', 'ts')
    const pGo = pool.highlight('package main', 'go')
    expect(workers).toHaveLength(2)

    workers[0]!.emitMessage({ id: workers[0]!.received[0]!.id, ok: true, html: '<span>ts</span>' })
    workers[1]!.emitMessage({ id: workers[1]!.received[0]!.id, ok: true, html: '<span>go</span>' })
    await Promise.all([pTs, pGo])

    // Both workers are idle now; a Go request should prefer worker[1] which already loaded Go.
    const pGo2 = pool.highlight('func main() {}', 'go')
    expect(workers[1]!.received).toHaveLength(2)
    expect(workers[0]!.received).toHaveLength(1)
    workers[1]!.emitMessage({ id: workers[1]!.received[1]!.id, ok: true, html: '<span>go2</span>' })
    expect(await pGo2).toBe('<span>go2</span>')

    // After idleTimeoutMs, surplus worker is terminated while minIdleWorkers stays warm.
    vi.advanceTimersByTime(5_100)
    expect(pool.stats().activeWorkers).toBe(1)
    expect(workers.filter((w) => w.terminated)).toHaveLength(1)

    pool.dispose()
  })

  test('recovers from a worker crash and falls back cleanly when workers are unavailable', async () => {
    const workers: FakeWorkerPort[] = []
    const pool = createShikiWorkerPool({
      maxWorkers: 1,
      idleTimeoutMs: 0,
      createWorker: () => {
        const port = new FakeWorkerPort()
        workers.push(port)
        return port
      },
    })

    const promise = pool.highlight('const a = 1', 'ts')
    expect(workers).toHaveLength(1)
    workers[0]!.emitError('OOM')

    // Pool spawns a fresh worker and retries the interrupted task once.
    expect(workers[0]!.terminated).toBe(true)
    expect(workers).toHaveLength(2)
    workers[1]!.emitMessage({ id: workers[1]!.received[0]!.id, ok: true, html: '<span>ok</span>' })
    expect(await promise).toBe('<span>ok</span>')
    pool.dispose()

    const fallback = vi.fn(
      async (code: string, lang: string) => `<code data-lang="${lang}">${code}</code>`,
    )
    const fallbackPool = createShikiWorkerPool({
      createWorker: () => null,
      fallback,
    })

    expect(await fallbackPool.highlight('let x = 1', 'ts')).toBe(
      '<code data-lang="typescript">let x = 1</code>',
    )
    expect(fallbackPool.stats().fallbackMode).toBe(true)
    expect(fallbackPool.peek('let x = 1', 'ts')).toBe(
      '<code data-lang="typescript">let x = 1</code>',
    )
    expect(fallback).toHaveBeenCalledTimes(1)
    fallbackPool.dispose()
  })

  test('does not evict existing cache entries when a single result exceeds maxCacheChars', async () => {
    const workers: FakeWorkerPort[] = []
    const pool = createShikiWorkerPool({
      maxWorkers: 1,
      idleTimeoutMs: 0,
      maxCacheChars: 50,
      createWorker: () => {
        const port = new FakeWorkerPort()
        workers.push(port)
        return port
      },
    })

    const run = async (code: string, html: string) => {
      const promise = pool.highlight(code, 'ts')
      const req = workers[0]!.received.at(-1)!
      workers[0]!.emitMessage({ id: req.id, ok: true, html })
      return promise
    }

    const smallA = '<span>aaaaaaaa</span>' // 21 chars
    const smallB = '<span>bbbbbbbb</span>' // 21 chars
    const oversized = `<span>${'x'.repeat(60)}</span>` // 73 chars > maxCacheChars (50)

    await run('a', smallA)
    await run('b', smallB)
    expect(await run('big', oversized)).toBe(oversized)

    expect(pool.peek('a', 'ts')).toBe(smallA)
    expect(pool.peek('b', 'ts')).toBe(smallB)
    expect(pool.peek('big', 'ts')).toBeNull()
    expect(pool.stats().cacheEntries).toBe(2)
    expect(pool.stats().cacheChars).toBe(smallA.length + smallB.length)

    pool.dispose()
  })

  test('enters fallbackMode after consecutive async worker failures and resets failure count after recovery', async () => {
    const failingWorkers: FakeWorkerPort[] = []
    const fallback = vi.fn(
      async (code: string, lang: string) => `<fallback data-lang="${lang}">${code}</fallback>`,
    )
    const failingPool = createShikiWorkerPool({
      maxWorkers: 2,
      idleTimeoutMs: 0,
      createWorker: () => {
        const port = new FakeWorkerPort()
        failingWorkers.push(port)
        return port
      },
      fallback,
    })

    const first = failingPool.highlight('const a = 1', 'ts')
    expect(failingWorkers).toHaveLength(1)
    // Simulate async module worker load failure (CSP / 404).
    failingWorkers[0]!.emitError('Failed to fetch worker module')
    expect(failingWorkers).toHaveLength(2)
    failingWorkers[1]!.emitError('Failed to fetch worker module')

    expect(await first).toBe('<fallback data-lang="typescript">const a = 1</fallback>')
    expect(failingPool.stats().fallbackMode).toBe(true)
    expect(failingPool.stats().activeWorkers).toBe(0)

    // Subsequent requests go directly to fallback without spawning doomed workers.
    const second = await failingPool.highlight('const b = 2', 'ts')
    expect(second).toBe('<fallback data-lang="typescript">const b = 2</fallback>')
    expect(failingWorkers).toHaveLength(2)
    expect(fallback).toHaveBeenCalledTimes(2)
    failingPool.dispose()

    // Isolated crashes separated by healthy responses reset the failure counter.
    const recoveringWorkers: FakeWorkerPort[] = []
    const recoveringFallback = vi.fn(async () => '<span>fallback</span>')
    const recoveringPool = createShikiWorkerPool({
      maxWorkers: 1,
      idleTimeoutMs: 0,
      createWorker: () => {
        const port = new FakeWorkerPort()
        recoveringWorkers.push(port)
        return port
      },
      fallback: recoveringFallback,
    })

    const p1 = recoveringPool.highlight('one', 'ts')
    recoveringWorkers[0]!.emitError('transient crash 1')
    recoveringWorkers[1]!.emitMessage({
      id: recoveringWorkers[1]!.received[0]!.id,
      ok: true,
      html: '<span>1</span>',
    })
    expect(await p1).toBe('<span>1</span>')

    const p2 = recoveringPool.highlight('two', 'ts')
    recoveringWorkers[1]!.emitError('transient crash 2')
    recoveringWorkers[2]!.emitMessage({
      id: recoveringWorkers[2]!.received[0]!.id,
      ok: true,
      html: '<span>2</span>',
    })
    expect(await p2).toBe('<span>2</span>')
    expect(recoveringPool.stats().fallbackMode).toBe(false)
    expect(recoveringFallback).not.toHaveBeenCalled()
    recoveringPool.dispose()
  })

  test('settles aborted running tasks without retrying or running fallback on worker error', async () => {
    const workers: FakeWorkerPort[] = []
    const fallback = vi.fn(async () => '<span>fallback</span>')
    const pool = createShikiWorkerPool({
      maxWorkers: 1,
      idleTimeoutMs: 0,
      createWorker: () => {
        const port = new FakeWorkerPort()
        workers.push(port)
        return port
      },
      fallback,
    })

    const controller = new AbortController()
    const promise = pool.highlight('const cancelled = 1', 'ts', { signal: controller.signal })
    expect(workers).toHaveLength(1)
    expect(pool.stats().inFlightTasks).toBe(1)

    controller.abort()
    await expect(promise).rejects.toMatchObject({ name: 'AbortError' })

    // Worker crashes while executing the now-unawaited task.
    workers[0]!.emitError('OOM')

    expect(workers).toHaveLength(1)
    expect(pool.stats().inFlightTasks).toBe(0)
    expect(pool.stats().queuedTasks).toBe(0)
    expect(fallback).not.toHaveBeenCalled()

    // Same for an error protocol response on an aborted running task.
    const controller2 = new AbortController()
    const promise2 = pool.highlight('const cancelled2 = 2', 'ts', { signal: controller2.signal })
    expect(workers).toHaveLength(2)
    controller2.abort()
    await expect(promise2).rejects.toMatchObject({ name: 'AbortError' })

    workers[1]!.emitMessage({
      id: workers[1]!.received[0]!.id,
      ok: false,
      error: 'grammar failed',
    })
    expect(pool.stats().inFlightTasks).toBe(0)
    expect(fallback).not.toHaveBeenCalled()

    pool.dispose()
  })

  test('highlights dual-theme HTML end-to-end through attachShikiWorker', async () => {
    const pool = createShikiWorkerPool({
      maxWorkers: 2,
      idleTimeoutMs: 0,
      createWorker: createInProcessShikiPort,
    })

    const [tsHtml, goHtml] = await Promise.all([
      pool.highlight('export const answer: number = 42', 'ts'),
      pool.highlight('package main\nfunc main() {}', 'go'),
    ])

    expect(tsHtml).toContain('--shiki-light:')
    expect(tsHtml).toContain('--shiki-dark:')
    expect(goHtml).toContain('--shiki-light:')
    expect(goHtml).toContain('--shiki-dark:')
    expect(pool.peek('export const answer: number = 42', 'typescript')).toBe(tsHtml)

    pool.dispose()
  })
})
