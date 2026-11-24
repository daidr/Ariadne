import { describe, expect, test } from 'vitest'
import { createCodeEditorWorkerPool } from './code-worker-pool'

class FakeEditorWorker {
  terminated = false
  private readonly errorListeners = new Set<() => void>()

  addEventListener(type: string, listener: () => void): void {
    if (type === 'error') this.errorListeners.add(listener)
  }

  terminate(): void {
    this.terminated = true
  }

  emitError(): void {
    for (const listener of Array.from(this.errorListeners)) listener()
    this.errorListeners.clear()
  }
}

describe('CodeEditor worker pool', () => {
  test('lazily creates up to maxWorkers, round-robins reuse, and reclaims terminated slots', () => {
    const created: FakeEditorWorker[] = []
    const pool = createCodeEditorWorkerPool({
      maxWorkers: 2,
      createWorker: () => {
        const worker = new FakeEditorWorker()
        created.push(worker)
        return worker as unknown as Worker
      },
    })

    expect(pool.activeCount()).toBe(0)

    const w1 = pool.acquire()
    const w2 = pool.acquire()
    expect(w1).not.toBe(w2)
    expect(created).toHaveLength(2)
    expect(pool.activeCount()).toBe(2)

    // Reuses active workers once maxWorkers is reached.
    const w3 = pool.acquire()
    expect(w3).toBe(w1)
    expect(created).toHaveLength(2)

    // When @dou-ui/code's ref-counter terminates a worker, the pool frees that slot.
    w1.terminate()
    expect(created[0]!.terminated).toBe(true)
    expect(pool.activeCount()).toBe(1)

    const w4 = pool.acquire()
    expect(w4).not.toBe(w1)
    expect(w4).not.toBe(w2)
    expect(created).toHaveLength(3)
    expect(pool.activeCount()).toBe(2)

    // Worker error also clears its slot so future editors do not reuse a broken thread.
    created[1]!.emitError()
    expect(pool.activeCount()).toBe(1)
  })
})
