import { shikiLanguage } from '@dou-ui/code/shiki'
import { createHighlighterCore } from 'shiki/core'
import { createOnigurumaEngine } from 'shiki/engine/oniguruma'
import onigurumaUrl from 'shiki/onig.wasm?url'

const grammars = {
  go: () => import('shiki/langs/go.mjs'),
  rs: () => import('shiki/langs/rust.mjs'),
  lua: () => import('shiki/langs/lua.mjs'),
  toml: () => import('shiki/langs/toml.mjs'),
  ini: () => import('shiki/langs/ini.mjs'),
  dockerfile: () => import('shiki/langs/docker.mjs'),
  ps1: () => import('shiki/langs/powershell.mjs'),
  xml: () => import('shiki/langs/xml.mjs'),
}

let highlighter: ReturnType<typeof createHighlighterCore> | undefined

export async function extraCodeLanguage(extension: string) {
  if (!Object.hasOwn(grammars, extension)) return null
  const load = grammars[extension as keyof typeof grammars]
  const [grammar, engine] = await Promise.all([
    load(),
    (highlighter ??= createHighlighterCore({
      themes: [],
      langs: [],
      engine: createOnigurumaEngine(() => fetch(onigurumaUrl)),
    })),
  ])
  await engine.loadLanguage(grammar.default)
  const name = grammar.default.at(-1)?.name
  return name ? shikiLanguage(engine, name) : null
}
