import {
  css,
  html,
  javascript,
  json,
  jsx,
  languageFor,
  markdown,
  python,
  scss,
  shell,
  sql,
  tsx,
  typescript,
  vue,
  yaml,
  type CodeLanguage,
} from '@dou-ui/code'
import { computed, shallowRef, watch, type ComputedRef } from 'vue'
import { languageHintOf, resolveShikiLanguage, type ShikiLanguageKey } from './languages'

const BUILT_IN_LANGUAGES = [
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
] as CodeLanguage[]

export type ShikiCodeLanguageLoader = (key: ShikiLanguageKey) => Promise<CodeLanguage | null>

async function defaultShikiLoader(key: ShikiLanguageKey): Promise<CodeLanguage | null> {
  const { loadShikiCodeLanguage } = await import('./shiki-code-language')
  return loadShikiCodeLanguage(key)
}

const cachedShiki = new Map<ShikiLanguageKey, CodeLanguage>()
const pendingShiki = new Map<ShikiLanguageKey, Promise<CodeLanguage | null>>()

/** Built-in `@dou-ui/code` language for a file path, available synchronously without loading Shiki. */
export function builtInCodeLanguage(path: string): CodeLanguage | null {
  const hint = languageHintOf(path)
  return hint ? languageFor(BUILT_IN_LANGUAGES, hint) : null
}

/** Synchronous language lookup: built-in first, then any previously loaded Shiki grammar. */
export function peekCodeLanguage(path: string): CodeLanguage | null {
  const builtIn = builtInCodeLanguage(path)
  if (builtIn) return builtIn
  const hint = languageHintOf(path)
  const key = hint ? resolveShikiLanguage(hint) : null
  return key ? (cachedShiki.get(key) ?? null) : null
}

/**
 * Resolve a `@dou-ui/code` language for `path`, falling back lazily to Shiki for languages
 * outside `@dou-ui/code`'s built-in set (such as Go, Protobuf, Rust, Dockerfile, TOML, etc.).
 * Concurrent calls for the same language share one load and cache the resulting tokenizer.
 */
export async function loadCodeLanguage(
  path: string,
  loader: ShikiCodeLanguageLoader = defaultShikiLoader,
): Promise<CodeLanguage | null> {
  const useCache = loader === defaultShikiLoader
  const cached = useCache ? peekCodeLanguage(path) : builtInCodeLanguage(path)
  if (cached) return cached
  const hint = languageHintOf(path)
  const key = hint ? resolveShikiLanguage(hint) : null
  if (!key) return null

  if (useCache) {
    const inFlight = pendingShiki.get(key)
    if (inFlight) return inFlight
  }

  const task = loader(key)
    .then((grammar) => {
      if (useCache && grammar) cachedShiki.set(key, grammar)
      return grammar
    })
    .finally(() => {
      if (useCache) pendingShiki.delete(key)
    })

  if (useCache) pendingShiki.set(key, task)
  return task
}

/** Reactive `@dou-ui/code` language binding with synchronous built-ins and lazy Shiki fallback. */
export function useCodeLanguage(
  path: () => string,
  loader: ShikiCodeLanguageLoader = defaultShikiLoader,
): {
  language: ComputedRef<CodeLanguage | null>
  isBuiltIn: ComputedRef<boolean>
} {
  const builtIn = computed(() => builtInCodeLanguage(path()))
  const extra = shallowRef<CodeLanguage | null>(null)

  watch(
    path,
    async (current, _previous, cleanup) => {
      extra.value = null
      if (builtInCodeLanguage(current)) return
      const cached = loader === defaultShikiLoader ? peekCodeLanguage(current) : null
      if (cached) {
        extra.value = cached
        return
      }
      let active = true
      cleanup(() => {
        active = false
      })
      try {
        const grammar = await loadCodeLanguage(current, loader)
        if (active) extra.value = grammar
      } catch {
        // Keep plain rendering if the optional Shiki tokenizer fails to load.
      }
    },
    { immediate: true },
  )

  return {
    language: computed(() => builtIn.value ?? extra.value),
    isBuiltIn: computed(() => builtIn.value !== null),
  }
}

/** Test helper to clear cached Shiki `CodeLanguage` instances between tests. */
export function resetCodeLanguageCache() {
  cachedShiki.clear()
  pendingShiki.clear()
}
