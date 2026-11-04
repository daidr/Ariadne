import { describe, expect, test } from 'vitest'
import { createTranslator, format, locales, negotiateLocale, terminalLocale } from './index'

describe('i18n', () => {
  test('every locale covers every key of the source language', () => {
    const keys = Object.keys(locales['zh-CN']).toSorted()
    for (const messages of Object.values(locales)) {
      expect(Object.keys(messages).toSorted()).toEqual(keys)
    }
  })

  test('fills placeholders and keeps unknown ones', () => {
    expect(format('找不到 {what}', { what: '项目' })).toBe('找不到 项目')
    expect(format('{a} {b}', { a: 1 })).toBe('1 {b}')
  })

  test('translates with the chosen locale', () => {
    expect(createTranslator('en')('error.notFound', { what: 'Project' })).toBe('Project not found')
    expect(createTranslator()('attention.title')).toBe('需要我')
  })

  test("reads a terminal's language", () => {
    expect(terminalLocale({ LANG: 'zh_CN.UTF-8' })).toBe('zh-CN')
    expect(terminalLocale({ LC_ALL: 'en_US.UTF-8', LANG: 'zh_CN.UTF-8' })).toBe('en')
    expect(terminalLocale({ LANG: 'C.UTF-8' }, 'en')).toBe('en')
    expect(terminalLocale({ LANG: 'fr_FR.UTF-8' })).toBe('zh-CN')
    expect(terminalLocale({})).toBe('zh-CN')
  })

  test('negotiates locales', () => {
    expect(negotiateLocale(['zh-TW', 'en'])).toBe('zh-CN')
    expect(negotiateLocale(['en-US'])).toBe('en')
    expect(negotiateLocale(['fr'])).toBe('zh-CN')
  })
})
