import { en } from './locales/en'
import { zhCN, type MessageKey } from './locales/zh-CN'

export type { MessageKey }

export const locales = { 'zh-CN': zhCN, en } satisfies Record<string, Record<MessageKey, string>>

export type Locale = keyof typeof locales

export const defaultLocale: Locale = 'zh-CN'

export type MessageParams = Record<string, string | number>

/** A message that has not been rendered yet: servers send these, clients translate them. */
export interface LocalizedMessage {
  key: MessageKey
  params?: MessageParams
}

export function isLocale(value: string): value is Locale {
  return Object.hasOwn(locales, value)
}

/** Fill `{name}` placeholders. Unknown placeholders are left as they are. */
export function format(template: string, params?: MessageParams): string {
  if (!params) return template
  return template.replace(/\{(\w+)\}/g, (match, name: string) =>
    Object.hasOwn(params, name) ? String(params[name]) : match,
  )
}

export type Translate = (key: MessageKey, params?: MessageParams) => string

/**
 * A message key the event log may hold from an older version but this one no longer has
 * comes back as the key itself, so old data never breaks rendering.
 */
export function createTranslator(locale: Locale = defaultLocale): Translate {
  const messages: Record<string, string> = locales[locale]
  const source: Record<string, string> = zhCN
  return (key, params) => format(messages[key] ?? source[key] ?? key, params)
}

/** Pick the best supported locale from a list such as `navigator.languages` or `Accept-Language`. */
export function negotiateLocale(preferred: readonly string[]): Locale {
  for (const tag of preferred) {
    if (isLocale(tag)) return tag
    const base = tag.split('-')[0]?.toLowerCase()
    if (base === 'zh') return 'zh-CN'
    if (base && isLocale(base)) return base
  }
  return defaultLocale
}
