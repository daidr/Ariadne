import { describe, expect, it } from 'vitest'
import { todoPlan } from './todo'

describe('todoPlan', () => {
  it('keeps the order the agent wrote and maps each status', () => {
    const items = todoPlan({
      conversationId: 'c',
      items: [
        { label: 'a', status: 'done' },
        { label: 'b', status: 'doing', detail: 'half way' },
        { label: 'c', status: 'waiting', detail: 'CI' },
        { label: 'd', status: 'next' },
      ],
    } as never)
    expect(items.map((item) => [item.label, item.status])).toEqual([
      ['a', 'done'],
      ['b', 'running'],
      ['c', 'pending'],
      ['d', 'pending'],
    ])
    expect(items[1]?.description).toBe('half way')
    expect(items[2]?.description).toContain('CI')
    expect(items[3]?.description).toBeUndefined()
  })

  it('does not show a step as running between turns', () => {
    const items = todoPlan(
      { conversationId: 'c', items: [{ label: 'b', status: 'doing' }] } as never,
      false,
    )
    expect(items[0]?.status).toBe('pending')
  })

  it('is empty without a list', () => {
    expect(todoPlan(null)).toEqual([])
  })
})
