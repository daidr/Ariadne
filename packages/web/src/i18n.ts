import {
  createMessagesTranslator,
  defaultLocale,
  negotiateLocale,
  type Locale,
  type LocaleMessages,
  type LocalizedMessage,
  type MessageKey,
  type MessageParams,
} from '@ariadne/i18n/core'
import type { Locale as DouLocale } from 'dou-ui'
import type { UsageTotals } from '@ariadne/core'
import type { LocalizedText } from '@ariadne/plugin-sdk'
import { computed, ref, shallowReactive, watch } from 'vue'

const LOCALE_KEY = 'ariadne.locale'

interface LocaleBundle {
  messages: LocaleMessages
  douLocale: DouLocale
}

const localeLoaders: Record<Locale, () => Promise<LocaleBundle>> = {
  'zh-CN': () => import('./locales/zh-CN'),
  en: () => import('./locales/en'),
}

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

const loadedBundles = shallowReactive<Partial<Record<Locale, LocaleBundle>>>({})
const pendingLoads = new Map<Locale, Promise<void>>()
// Keep rendering the last loaded locale while a newly selected locale chunk is in flight.
const lastLoadedLocale = ref<Locale | null>(null)

export function isLocaleLoaded(target: Locale): boolean {
  return !!loadedBundles[target]
}

export function ensureLocale(target: Locale = locale.value): Promise<void> {
  if (loadedBundles[target]) {
    if (locale.value === target) lastLoadedLocale.value = target
    return Promise.resolve()
  }
  const existing = pendingLoads.get(target)
  if (existing) return existing

  const task = localeLoaders[target]()
    .then((bundle) => {
      loadedBundles[target] = bundle
      if (locale.value === target) {
        lastLoadedLocale.value = target
      }
    })
    .finally(() => {
      pendingLoads.delete(target)
    })

  pendingLoads.set(target, task)
  return task
}

export function resetLocaleCache(): void {
  pendingLoads.clear()
  delete loadedBundles['zh-CN']
  delete loadedBundles.en
  lastLoadedLocale.value = null
}

watch(locale, (next) => {
  if (loadedBundles[next]) {
    lastLoadedLocale.value = next
    return
  }
  void ensureLocale(next)
})

const activeBundle = computed<LocaleBundle | undefined>(
  () =>
    loadedBundles[locale.value] ??
    (lastLoadedLocale.value ? loadedBundles[lastLoadedLocale.value] : undefined) ??
    loadedBundles[defaultLocale],
)

const translate = computed(() =>
  createMessagesTranslator(
    activeBundle.value?.messages ?? {},
    loadedBundles[defaultLocale]?.messages,
  ),
)

/** dou-ui's own strings follow the same locale. */
export const douLocale = computed<DouLocale | undefined>(() => activeBundle.value?.douLocale)

export function t(key: MessageKey, params?: MessageParams): string {
  return translate.value(key, params)
}

export function tm(message: LocalizedMessage): string {
  return t(message.key, message.params)
}

export function setLocale(next: Locale): Promise<void> {
  locale.value = next
  if (typeof document !== 'undefined') {
    document.documentElement.lang = next
  }
  try {
    localStorage.setItem(LOCALE_KEY, next)
  } catch {
    // Not remembered; still applies to this page.
  }
  return ensureLocale(next)
}

/** Plugin text in the current locale, falling back to Chinese. */
export function lt(text: LocalizedText | undefined): string | undefined {
  return text ? (text[locale.value] ?? text['zh-CN']) : undefined
}

// Token counts keep k/M in every locale: people know those units, and 万/亿 read as no size at all.
// Thousands are a lowercase k, as the SI prefix is; millions stay M.
const compact = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 })

/** A token count, e.g. `950`, `12.3k` or `1.2M`, the same in every locale. */
export function count(value: number): string {
  return compact.format(value).replace('K', 'k')
}

export function percent(part: number, whole: number): string {
  return whole
    ? new Intl.NumberFormat(locale.value, { style: 'percent', maximumFractionDigits: 0 }).format(
        part / whole,
      )
    : '—'
}

export function cacheHitRate({ input, cached, cacheWrite }: UsageTotals): string {
  // Older usage can contain cache counts without the corresponding total input.
  if (cached + cacheWrite > input) return '—'
  return percent(cached, input)
}

/** Names joined as the locale lists them: `A、B和C`, `A, B, and C`. */
export function list(items: string[]): string {
  return new Intl.ListFormat(locale.value === 'en' ? 'en' : 'zh-CN', {
    type: 'conjunction',
  }).format(items)
}

/** A duration in seconds or milliseconds, short. */
export function duration(ms: number): string {
  if (ms < 1000) return t('time.ms', { value: Math.round(ms) })
  if (ms < 60_000) return t('time.s', { value: (ms / 1000).toFixed(1) })
  return t('time.min', { m: Math.floor(ms / 60_000), s: Math.round((ms % 60_000) / 1000) })
}

/** A day, as short as it reads: `11月14日`, `Nov 14`. */
export function dayText(at: number): string {
  return new Date(at).toLocaleDateString(locale.value, { month: 'short', day: 'numeric' })
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
