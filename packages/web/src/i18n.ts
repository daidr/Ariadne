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
