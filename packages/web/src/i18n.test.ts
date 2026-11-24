// @vitest-environment happy-dom
import { afterAll, describe, expect, test } from 'vitest'
import {
  count,
  douLocale,
  ensureLocale,
  isLocaleLoaded,
  locale,
  resetLocaleCache,
  setLocale,
  t,
} from './i18n'

describe('token counts', () => {
  test('use k/M in every locale', () => {
    for (const next of ['zh-CN', 'en'] as const) {
      locale.value = next
      expect(count(950)).toBe('950')
      expect(count(12_345)).toBe('12.3k')
      expect(count(1_234_567)).toBe('1.2M')
      expect(count(999_999)).toBe('1M')
    }
  })
})

describe('on-demand locale loading', () => {
  afterAll(async () => {
    await Promise.all([ensureLocale('zh-CN'), ensureLocale('en')])
    await setLocale('zh-CN')
  })

  test('loads only the requested locale bundle and switches both app and dou-ui strings', async () => {
    resetLocaleCache()
    locale.value = 'zh-CN'

    expect(isLocaleLoaded('zh-CN')).toBe(false)
    expect(isLocaleLoaded('en')).toBe(false)

    await ensureLocale('zh-CN')
    expect(isLocaleLoaded('zh-CN')).toBe(true)
    expect(isLocaleLoaded('en')).toBe(false)
    expect(t('attention.title')).toBe('需要我')
    expect(douLocale.value?.name).toBe('zh-CN')

    await setLocale('en')
    expect(isLocaleLoaded('en')).toBe(true)
    expect(t('attention.title')).toBe('Needs me')
    expect(douLocale.value?.name).toBe('en')
    expect(document.documentElement.lang).toBe('en')
    expect(localStorage.getItem('ariadne.locale')).toBe('en')

    await setLocale('zh-CN')
    expect(t('attention.title')).toBe('需要我')
    expect(douLocale.value?.name).toBe('zh-CN')
    expect(document.documentElement.lang).toBe('zh-CN')
  })
})
