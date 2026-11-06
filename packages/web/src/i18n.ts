import {
  createTranslator,
  defaultLocale,
  negotiateLocale,
  type Locale,
  type LocalizedMessage,
  type MessageKey,
  type MessageParams,
} from '@ariadne/i18n'
import { en as douEn, zhCN as douZhCN } from 'dou-ui'
import type { LocalizedText } from '@ariadne/plugin-sdk'
import { computed, ref } from 'vue'

const LOCALE_KEY = 'ariadne.locale'

function initialLocale(): Locale {
  try {
    const saved = localStorage.getItem(LOCALE_KEY)
    if (saved) return negotiateLocale([saved])
  } catch {
    // Fall back to the default.
  }
  // Chinese first: the browser's language does not override it, only an explicit choice does.
  return defaultLocale
}

export const locale = ref<Locale>(initialLocale())

const translate = computed(() => createTranslator(locale.value))

/** dou-ui's own strings follow the same locale. */
export const douLocale = computed(() => (locale.value === 'en' ? douEn : douZhCN))

export function t(key: MessageKey, params?: MessageParams): string {
  return translate.value(key, params)
}

export function tm(message: LocalizedMessage): string {
  return t(message.key, message.params)
}

export function setLocale(next: Locale): void {
  locale.value = next
  document.documentElement.lang = next
  try {
    localStorage.setItem(LOCALE_KEY, next)
  } catch {
    // Not remembered; still applies to this page.
  }
}

/** Plugin text in the current locale, falling back to Chinese. */
export function lt(text: LocalizedText | undefined): string | undefined {
  return text ? (text[locale.value] ?? text['zh-CN']) : undefined
}

// Token counts keep K/M in every locale: people know those units, and 万/亿 read as no size at all.
const compact = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 })

/** A token count, e.g. `950`, `12.3K` or `1.2M`, the same in every locale. */
export function count(value: number): string {
  return compact.format(value)
}

/** A duration in seconds or milliseconds, short. */
export function duration(ms: number): string {
  if (ms < 1000) return t('time.ms', { value: Math.round(ms) })
  if (ms < 60_000) return t('time.s', { value: (ms / 1000).toFixed(1) })
  return t('time.min', { m: Math.floor(ms / 60_000), s: Math.round((ms % 60_000) / 1000) })
}

/** A coming moment, as short as it can be: `14:30` today, else with its date (`11/6 14:30`). */
export function clockText(at: number, now = Date.now()): string {
  const date = new Date(at)
  const today = new Date(now).toDateString() === date.toDateString()
  return date.toLocaleString(locale.value, {
    ...(today ? {} : { month: 'numeric', day: 'numeric' }),
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
}
