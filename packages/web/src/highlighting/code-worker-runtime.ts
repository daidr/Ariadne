import { shikiLanguage } from '@dou-ui/code/shiki'
import {
  css,
  html,
  javascript,
  json,
  jsx,
  markdown,
  python,
  scss,
  serveCodeHighlighting,
  shell,
  sql,
  tsx,
  typescript,
  vue,
  yaml,
  type CodeLanguage,
  type WorkerMessageScope,
} from '@dou-ui/code/worker'
import type { LoadedShikiLanguage } from './shiki'

export const BUILT_IN_CODE_WORKER_LANGUAGES: readonly CodeLanguage[] = [
  typescript,
  tsx,
  javascript,
  jsx,
  json,
  css,
  scss,
  html,
  vue,
  markdown,
  python,
  shell,
  sql,
  yaml,
]

export type WorkerShikiLanguageLoader = (name: string) => Promise<LoadedShikiLanguage | null>

/**
 * Bind `@dou-ui/code`'s worker protocol to a Web Worker global scope with all built-in
 * languages plus lazy on-demand Shiki resolution (running entirely off the main thread).
 */
export function attachCodeWorker(
  scope: WorkerMessageScope,
  loadLanguage: WorkerShikiLanguageLoader,
): void {
  serveCodeHighlighting(
    {
      languages: BUILT_IN_CODE_WORKER_LANGUAGES,
      async resolve(name) {
        const loaded = await loadLanguage(name)
        return loaded ? shikiLanguage(loaded.engine, loaded.name, { name }) : null
      },
    },
    scope,
  )
}
