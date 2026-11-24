import type { WorkerMessageScope } from '@dou-ui/code/worker'
import { attachCodeWorker } from './code-worker-runtime'
import { loadShikiLanguage } from './shiki'

attachCodeWorker(globalThis as unknown as WorkerMessageScope, loadShikiLanguage)
