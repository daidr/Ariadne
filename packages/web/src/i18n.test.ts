import { describe, expect, test } from 'vitest'
import { count, locale } from './i18n'

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
