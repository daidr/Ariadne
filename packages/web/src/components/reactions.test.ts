import { describe, expect, test } from 'vitest'
import { shownReactions } from './reactions'

describe('shownReactions', () => {
  test('one item an emoji, counting who put it there, the user first and marked', () => {
    expect(
      shownReactions([
        { emoji: '👀', by: 'coordinator' },
        { emoji: '👍', by: 'coordinator' },
        { emoji: '👀', by: 'user' },
      ]),
    ).toEqual([
      { emoji: '👀', count: 2, reacted: true, label: '你和协调器' },
      { emoji: '👍', count: 1, reacted: false, label: '协调器' },
    ])
  })
})
