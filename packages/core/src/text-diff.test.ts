import { describe, expect, test } from 'vitest'
import { lineDiff } from './text-diff'

describe('lineDiff', () => {
  test('nothing changed', () => {
    expect(lineDiff('a\nb', 'a\nb')).toBeNull()
  })

  test('changed lines with one line around them, the rest left out', () => {
    const before = ['1', '2', '3', '4', '5', '6', '7'].join('\n')
    const after = ['1', '2', '3', 'x', '5', '6', '7'].join('\n')
    expect(lineDiff(before, after)).toBe('…\n 3\n-4\n+x\n 5\n…')
  })

  test('added at the end', () => {
    expect(lineDiff('a', 'a\nb')).toBe(' a\n+b')
  })

  test('a long change is cut', () => {
    const after = Array.from({ length: 100 }, (_, i) => `line ${i}`).join('\n')
    const diff = lineDiff('', after, 50)!
    expect(diff.length).toBeLessThan(70)
    expect(diff.endsWith('…')).toBe(true)
  })
})
