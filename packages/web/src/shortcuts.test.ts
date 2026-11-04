import { describe, expect, test } from 'vitest'
import { steppedThread } from './shortcuts'

describe('thread switching', () => {
  const order = ['a', 'b', 'c']

  test('moves one thread and stops at the ends', () => {
    expect(steppedThread(order, 'b', 1)).toBe('c')
    expect(steppedThread(order, 'b', -1)).toBe('a')
    expect(steppedThread(order, 'c', 1)).toBe('c')
    expect(steppedThread(order, 'a', -1)).toBe('a')
  })

  test('with none open, down starts at the top and up at the bottom', () => {
    expect(steppedThread(order, null, 1)).toBe('a')
    expect(steppedThread(order, null, -1)).toBe('c')
    expect(steppedThread(order, 'gone', 1)).toBe('a')
    expect(steppedThread([], null, 1)).toBeNull()
  })
})
