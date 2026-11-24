import { resolveHighlightPoolSize } from './shiki-worker-pool'

export interface CodeEditorWorkerPoolOptions {
  createWorker?: () => Worker
  maxWorkers?: number
}

export interface CodeEditorWorkerPool {
  /** Acquire a worker for `DouCodeEditor`'s `:worker` prop, lazily spawning up to `maxWorkers`. */
  acquire(): Worker
  /** Number of currently active (non-terminated) workers tracked by the pool. */
  activeCount(): number
}

function createDefaultCodeWorker(): Worker {
  return new Worker(new URL('./code.worker.ts', import.meta.url), {
    type: 'module',
    name: 'ariadne-code',
  })
}

/**
 * Pool of `@dou-ui/code/worker` instances for `DouCodeEditor`.
 *
 * `@dou-ui/code` ref-counts each returned `Worker` and calls `worker.terminate()` when the last
 * editor bound to it unmounts. Wrapping `terminate` lets the pool drop ended slots automatically
 * and lazily spawn up to `maxWorkers` workers across concurrent editors without leaking threads.
 */
export function createCodeEditorWorkerPool(
  options: CodeEditorWorkerPoolOptions = {},
): CodeEditorWorkerPool {
  const createWorker = options.createWorker ?? createDefaultCodeWorker
  const maxWorkers = Math.max(1, Math.floor(options.maxWorkers ?? resolveHighlightPoolSize()))
  const slots: Array<Worker | null> = Array.from({ length: maxWorkers }, () => null)
  let cursor = 0

  function acquire(): Worker {
    // Prefer filling an empty slot first so concurrent editors spread across distinct workers.
    for (let i = 0; i < maxWorkers; i++) {
      const index = (cursor + i) % maxWorkers
      if (slots[index] === null) {
        const worker = createWorker()
        const rawTerminate = worker.terminate.bind(worker)
        const releaseSlot = () => {
          if (slots[index] === worker) slots[index] = null
        }
        worker.addEventListener('error', releaseSlot, { once: true })
        worker.terminate = () => {
          releaseSlot()
          rawTerminate()
        }
        slots[index] = worker
        cursor = (index + 1) % maxWorkers
        return worker
      }
    }

    const index = cursor
    cursor = (cursor + 1) % maxWorkers
    return slots[index]!
  }

  function activeCount(): number {
    return slots.filter((slot) => slot !== null).length
  }

  return { acquire, activeCount }
}

const sharedCodeEditorWorkerPool = createCodeEditorWorkerPool()

/** Shared worker factory suitable for `<DouCodeEditor :worker="acquireCodeEditorWorker">`. */
export const acquireCodeEditorWorker = (): Worker => sharedCodeEditorWorkerPool.acquire()
