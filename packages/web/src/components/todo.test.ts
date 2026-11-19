import { describe, expect, it } from 'vitest'
import type { TodoView } from '@ariadne/core'
import { placeTodo, todoPlan } from './todo'

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
      ['c', 'waiting'],
      ['d', 'pending'],
    ])
    expect(items[1]?.description).toBe('half way')
    expect(items[2]?.description).toBe('CI')
    expect(items[3]?.description).toBeUndefined()
  })

  it('shows the step it was on as not started between turns', () => {
    const todo = { conversationId: 'c', items: [{ label: 'b', status: 'doing' }] } as never
    expect(todoPlan(todo, false).map((item) => item.status)).toEqual(['pending'])
  })

  it('is empty without a list', () => {
    expect(todoPlan(null)).toEqual([])
  })
})

describe('placeTodo', () => {
  const first = { id: 'msg_a', at: 20, message: { id: 'msg_a' } }
  const relay = { id: 'msg_a:1', at: 20, message: { id: 'msg_a:1' } }
  const sameTime = { id: 'msg_b', at: 20, message: { id: 'msg_b' } }
  const later = { id: 'msg_c', at: 30, message: { id: 'msg_c' } }
  const list: TodoView = {
    conversationId: 'thread',
    items: [{ label: 'Read the source', status: 'doing' }],
    updatedAt: 35,
    position: { at: 20, afterMessageId: first.id },
  }
  const ids = (items: ReturnType<typeof placeTodo<typeof first>>) => items.map((item) => item.id)

  it('stays before newer messages, including those with the same timestamp', () => {
    expect(ids(placeTodo([first, relay, sameTime, later], list))).toEqual([
      first.id,
      relay.id,
      'todo:thread',
      sameTime.id,
      later.id,
    ])
  })

  it('keeps the list out of a page loaded after its original position', () => {
    const page = [sameTime, later]
    expect(placeTodo(page, list, { id: sameTime.id, createdAt: sameTime.at })).toBe(page)
    expect(ids(placeTodo([first, ...page], list, { id: first.id, createdAt: first.at }))).toEqual([
      first.id,
      'todo:thread',
      sameTime.id,
      later.id,
    ])
  })

  it('uses the time of an older todo update without saved position', () => {
    expect(ids(placeTodo([first, later], { ...list, updatedAt: 25, position: undefined }))).toEqual(
      [first.id, 'todo:thread', later.id],
    )
  })
})
