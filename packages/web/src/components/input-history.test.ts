import { describe, expect, test } from 'vitest'
import { stepHistory } from './input-history'

describe('input history', () => {
  const sent = ['third', 'second', 'first']

  test('↑ on an empty input brings back the last message, then older ones', () => {
    const one = stepHistory(sent, null, '', 1)!
    expect(one.draft).toBe('third')
    const two = stepHistory(sent, one.recall, one.draft, 1)!
    expect(two.draft).toBe('second')
    expect(stepHistory(sent, { index: 2, draft: '' }, 'first', 1)).toBeNull()
  })

  test('↓ goes back to newer ones and past the newest to the empty input', () => {
    const back = stepHistory(sent, { index: 1, draft: '' }, 'second', -1)!
    expect(back.draft).toBe('third')
    expect(stepHistory(sent, back.recall, back.draft, -1)).toEqual({ recall: null, draft: '' })
  })

  test('leaves the arrows to the caret in text being written', () => {
    expect(stepHistory(sent, null, 'draft', 1)).toBeNull()
    expect(stepHistory(sent, null, '', -1)).toBeNull()
    expect(stepHistory([], null, '', 1)).toBeNull()
    // The recalled message was edited: it is now a draft of its own.
    expect(stepHistory(sent, { index: 0, draft: '' }, 'third!', 1)).toBeNull()
  })
})
