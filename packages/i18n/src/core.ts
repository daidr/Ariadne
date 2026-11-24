import type { MessageKey } from './locales/zh-CN'

export type { MessageKey }

export const supportedLocales = ['zh-CN', 'en'] as const

export type Locale = (typeof supportedLocales)[number]

export type LocaleMessages = Record<MessageKey, string>

export const defaultLocale: Locale = 'zh-CN'

export type MessageParams = Record<string, string | number>

/** A message that has not been rendered yet: servers send these, clients translate them. */
export interface LocalizedMessage {
  key: MessageKey
  params?: MessageParams
}

export function isLocale(value: string): value is Locale {
  return (supportedLocales as readonly string[]).includes(value)
}

/** Fill `{name}` placeholders. Unknown placeholders are left as they are. */
export function format(template: string, params?: MessageParams): string {
  if (!params) return template
  return template.replace(/\{(\w+)\}/g, (match, name: string) =>
    Object.hasOwn(params, name) ? String(params[name]) : match,
  )
}

export type Translate = (key: MessageKey, params?: MessageParams) => string

export function createMessagesTranslator(
  messages: Partial<LocaleMessages>,
  fallback?: Partial<LocaleMessages>,
): Translate {
  return (key, params) => format(messages[key] ?? fallback?.[key] ?? key, params)
}

/**
 * The language a terminal is set to (`LC_ALL`, `LC_MESSAGES`, then `LANG`, e.g.
 * `zh_CN.UTF-8`), for what a command line program prints; `fallback` when it is unset,
 * `C`/`POSIX`, or a language without a translation.
 */
export function terminalLocale(
  env: Record<string, string | undefined>,
  fallback: Locale = defaultLocale,
): Locale {
  const value = env.LC_ALL || env.LC_MESSAGES || env.LANG || ''
  const language = value.split(/[_.@-]/)[0]?.toLowerCase()
  if (language === 'zh') return 'zh-CN'
  if (language === 'en') return 'en'
  return fallback
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
