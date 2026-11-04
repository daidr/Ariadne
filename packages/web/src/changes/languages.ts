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

const LANGUAGES = [
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

/** The language to colour a file in, from its extension; `null` shows it plain. */
export function languageOf(path: string): CodeLanguage | null {
  const name = path.split('/').at(-1) ?? ''
  const dot = name.lastIndexOf('.')
  return dot > 0 ? languageFor(LANGUAGES, name.slice(dot + 1)) : null
}
