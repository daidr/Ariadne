import {
  createMessagesTranslator,
  defaultLocale,
  type Locale,
  type LocaleMessages,
  type Translate,
} from './core'
import { en } from './locales/en'
import { zhCN } from './locales/zh-CN'

export {
  createMessagesTranslator,
  defaultLocale,
  format,
  isLocale,
  negotiateLocale,
  supportedLocales,
  terminalLocale,
  type Locale,
  type LocaleMessages,
  type LocalizedMessage,
  type MessageKey,
  type MessageParams,
  type Translate,
} from './core'

export const locales = { 'zh-CN': zhCN, en } satisfies Record<Locale, LocaleMessages>

export function createTranslator(locale: Locale = defaultLocale): Translate {
  return createMessagesTranslator(locales[locale], zhCN)
}
