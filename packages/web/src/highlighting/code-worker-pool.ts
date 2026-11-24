import {
  createCodeWorkerPool,
  type CodeWorkerOption,
  type CodeWorkerPool,
  type CodeWorkerPoolOptions,
} from '@dou-ui/code'
import { ref } from 'vue'
import { codeWorkerUrl } from './code-worker-url'
import { resolveHighlightPoolSize } from './shiki-worker-pool'

const workerUnavailable = ref(false)

/** Whether browser Web Workers are available and healthy for `@dou-ui/code` highlighting. */
export function canUseCodeWorker(mode: string = import.meta.env.MODE): boolean {
  return typeof Worker !== 'undefined' && mode !== 'test' && !workerUnavailable.value
}

/** Test helper to clear the cached worker-failure fallback flag between tests. */
export function resetCodeWorkerAvailability(): void {
  workerUnavailable.value = false
}

export function createDefaultCodeWorker(): Worker {
  let worker: Worker
  try {
    worker = new Worker(codeWorkerUrl(), {
      type: 'module',
      name: 'ariadne-code',
    })
  } catch (error) {
    workerUnavailable.value = true
    throw error
  }
  worker.addEventListener(
    'error',
    () => {
      workerUnavailable.value = true
    },
    { once: true },
  )
  return worker
}

/**
 * Create a lazily-spawned `@dou-ui/code` worker pool backed by `code.worker.ts`
 * (built-in languages + on-demand Shiki off-main-thread highlighting + async large diffs).
 */
export function createAriadneCodeWorkerPool(
  options: CodeWorkerPoolOptions = {},
  factory: () => Worker = createDefaultCodeWorker,
): CodeWorkerPool {
  return createCodeWorkerPool(factory, {
    maxWorkers: resolveHighlightPoolSize(),
    ...options,
  })
}

/**
 * Shared lazily-spawned worker pool for `<DouCodeEditor>` and `<DouCodeDiff>`.
 */
export const codeWorkerPool: CodeWorkerPool = createAriadneCodeWorkerPool()

/**
 * Reactive `:worker` prop value for `DouCodeEditor` and `DouCodeDiff`: returns the shared
 * lazy `codeWorkerPool` in real browsers, and `false` in Vitest or when Workers are unavailable.
 */
export function codeEditorWorker(mode: string = import.meta.env.MODE): CodeWorkerOption {
  return canUseCodeWorker(mode) ? codeWorkerPool : false
}
