import { highlightCodeToHtml } from './shiki'
import { attachShikiWorker } from './shiki-worker-runtime'
import type { ShikiWorkerScope } from './shiki-worker-protocol'

attachShikiWorker(globalThis as unknown as ShikiWorkerScope, {
  highlightToHtml: highlightCodeToHtml,
})
