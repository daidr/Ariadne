import {
  createHighlighterCore,
  type HighlighterCore,
  type LanguageRegistration,
  type RegexEngine,
} from 'shiki/core'
import { createOnigurumaEngine } from 'shiki/engine/oniguruma'
import onigurumaUrl from 'shiki/onig.wasm?url'
import { resolveShikiLanguage, type ShikiLanguageKey } from './languages'

type GrammarModule = { default: LanguageRegistration[] }

const LIGHT_THEME = 'github-light-default'
const DARK_THEME = 'github-dark-default'

const grammars: Record<ShikiLanguageKey, () => Promise<GrammarModule>> = {
  bash: () => import('shiki/langs/bash.mjs'),
  c: () => import('shiki/langs/c.mjs'),
  cpp: () => import('shiki/langs/cpp.mjs'),
  csharp: () => import('shiki/langs/csharp.mjs'),
  css: () => import('shiki/langs/css.mjs'),
  diff: () => import('shiki/langs/diff.mjs'),
  dockerfile: () => import('shiki/langs/docker.mjs'),
  go: () => import('shiki/langs/go.mjs'),
  graphql: () => import('shiki/langs/graphql.mjs'),
  html: () => import('shiki/langs/html.mjs'),
  ini: () => import('shiki/langs/ini.mjs'),
  java: () => import('shiki/langs/java.mjs'),
  javascript: () => import('shiki/langs/javascript.mjs'),
  json: () => import('shiki/langs/json.mjs'),
  jsonc: () => import('shiki/langs/jsonc.mjs'),
  jsx: () => import('shiki/langs/jsx.mjs'),
  kotlin: () => import('shiki/langs/kotlin.mjs'),
  lua: () => import('shiki/langs/lua.mjs'),
  makefile: () => import('shiki/langs/make.mjs'),
  markdown: () => import('shiki/langs/markdown.mjs'),
  php: () => import('shiki/langs/php.mjs'),
  powershell: () => import('shiki/langs/powershell.mjs'),
  proto: () => import('shiki/langs/proto.mjs'),
  python: () => import('shiki/langs/python.mjs'),
  ruby: () => import('shiki/langs/ruby.mjs'),
  rust: () => import('shiki/langs/rust.mjs'),
  scss: () => import('shiki/langs/scss.mjs'),
  sql: () => import('shiki/langs/sql.mjs'),
  swift: () => import('shiki/langs/swift.mjs'),
  toml: () => import('shiki/langs/toml.mjs'),
  tsx: () => import('shiki/langs/tsx.mjs'),
  typescript: () => import('shiki/langs/typescript.mjs'),
  vue: () => import('shiki/langs/vue.mjs'),
  xml: () => import('shiki/langs/xml.mjs'),
  yaml: () => import('shiki/langs/yaml.mjs'),
}

export interface LoadedShikiLanguage {
  engine: HighlighterCore
  name: string
}

export interface ShikiCodeService {
  loadLanguage(raw: string): Promise<LoadedShikiLanguage | null>
  highlightToHtml(code: string, lang: string): Promise<string>
}

export function createShikiService(
  createEngine: () => Promise<RegexEngine> | RegexEngine,
): ShikiCodeService {
  let highlighter: Promise<HighlighterCore> | undefined
  let themesReady: Promise<void> | undefined
  const loadedLanguages = new Map<ShikiLanguageKey, Promise<string | null>>()

  function getHighlighter(): Promise<HighlighterCore> {
    if (!highlighter) {
      highlighter = createHighlighterCore({
        themes: [],
        langs: [],
        engine: createEngine(),
      }).catch((error) => {
        highlighter = undefined
        throw error
      })
    }
    return highlighter
  }

  function ensureThemes(engine: HighlighterCore): Promise<void> {
    if (!themesReady) {
      themesReady = Promise.all([
        import('shiki/themes/github-light-default.mjs'),
        import('shiki/themes/github-dark-default.mjs'),
      ])
        .then(([light, dark]) => engine.loadTheme(light.default, dark.default))
        .catch((error) => {
          themesReady = undefined
          throw error
        })
    }
    return themesReady
  }

  async function loadLanguage(raw: string): Promise<LoadedShikiLanguage | null> {
    const key = resolveShikiLanguage(raw)
    if (!key) return null
    let pending = loadedLanguages.get(key)
    if (!pending) {
      pending = Promise.all([grammars[key](), getHighlighter()])
        .then(async ([grammar, engine]) => {
          await engine.loadLanguage(grammar.default)
          return grammar.default.at(-1)?.name ?? null
        })
        .catch((error) => {
          loadedLanguages.delete(key)
          throw error
        })
      loadedLanguages.set(key, pending)
    }
    const [name, engine] = await Promise.all([pending, getHighlighter()])
    return name ? { engine, name } : null
  }

  async function highlightToHtml(code: string, lang: string): Promise<string> {
    if (!code) return ''
    const loaded = await loadLanguage(lang)
    if (!loaded) return ''
    await ensureThemes(loaded.engine)
    return loaded.engine.codeToHtml(code, {
      lang: loaded.name,
      themes: { light: LIGHT_THEME, dark: DARK_THEME },
      defaultColor: false,
    })
  }

  return { loadLanguage, highlightToHtml }
}

const shared = createShikiService(() => createOnigurumaEngine(() => fetch(onigurumaUrl)))

export const loadShikiLanguage = shared.loadLanguage
export const highlightCodeToHtml = shared.highlightToHtml
