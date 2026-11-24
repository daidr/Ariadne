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
export function canUseCodeWorker(): boolean {
  return (
    typeof Worker !== 'undefined' && import.meta.env.MODE !== 'test' && !workerUnavailable.value
  )
}

function createDefaultCodeWorker(): Worker {
  const worker = new Worker(codeWorkerUrl(), {
    type: 'module',
    name: 'ariadne-code',
  })
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
export function codeEditorWorker(): CodeWorkerOption {
  return canUseCodeWorker() ? codeWorkerPool : false
}
