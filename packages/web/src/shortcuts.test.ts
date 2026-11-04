import { describe, expect, test } from 'vitest'
import { hotkeyOf, steppedThread, typesText } from './shortcuts'

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

const press = (init: Partial<KeyboardEvent>) =>
  ({ metaKey: false, ctrlKey: false, altKey: false, shiftKey: false, ...init }) as KeyboardEvent

describe('binding keys', () => {
  test('reads modifiers as the platform names them', () => {
    expect(hotkeyOf(press({ key: 'k', code: 'KeyK', metaKey: true }), true)).toBe('mod+k')
    expect(hotkeyOf(press({ key: 'k', code: 'KeyK', ctrlKey: true }), false)).toBe('mod+k')
    expect(hotkeyOf(press({ key: 'k', code: 'KeyK', ctrlKey: true }), true)).toBe('ctrl+k')
    expect(hotkeyOf(press({ key: 'K', code: 'KeyK', ctrlKey: true, shiftKey: true }), false)).toBe(
      'mod+shift+k',
    )
  })

  test('reads letters from the physical key and symbols as typed', () => {
    expect(hotkeyOf(press({ key: 'å', code: 'KeyA', altKey: true }), true)).toBe('alt+a')
    expect(hotkeyOf(press({ key: '?', code: 'Slash', shiftKey: true }), false)).toBe('?')
    expect(hotkeyOf(press({ key: 'ArrowUp', code: 'ArrowUp', ctrlKey: true }), false)).toBe(
      'mod+up',
    )
    expect(hotkeyOf(press({ key: 'Escape', code: 'Escape' }), false)).toBe('escape')
  })

  test('takes no modifier alone and no key the settings refuse', () => {
    expect(hotkeyOf(press({ key: 'Shift', code: 'ShiftLeft', shiftKey: true }), false)).toBeNull()
    expect(hotkeyOf(press({ key: 'Insert', code: 'Insert' }), false)).toBeNull()
    expect(hotkeyOf(press({ key: '+', code: 'Equal', shiftKey: true }), false)).toBeNull()
  })

  test('keys that type text are only heard outside fields', () => {
    expect(typesText('/')).toBe(true)
    expect(typesText('shift+a')).toBe(true)
    expect(typesText('mod+k')).toBe(false)
    expect(typesText('escape')).toBe(false)
    expect(typesText('alt+up')).toBe(false)
  })
})
