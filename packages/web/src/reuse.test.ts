import { expect, test } from 'vitest'
import { reuse } from './reuse'

test('what did not change is the object it was', () => {
  const previous = {
    project: { id: 'p', usage: { input: 1 } },
    threads: [{ id: 'a' }, { id: 'b' }],
  }
  const next = { project: { id: 'p', usage: { input: 2 } }, threads: [{ id: 'a' }, { id: 'b' }] }
  const merged = reuse(previous, next)
  expect(merged).toEqual(next)
  expect(merged).not.toBe(previous)
  expect(merged.threads).toBe(previous.threads)
  expect(merged.project).not.toBe(previous.project)
})

test('nothing changed gives the previous value itself', () => {
  const previous = { a: [1, { b: null }], c: 'x' }
  expect(reuse(previous, { a: [1, { b: null }], c: 'x' })).toBe(previous)
})

test('rows that moved are found by id; added and removed ones change only the list', () => {
  const a = { id: 'a', n: 1 }
  const b = { id: 'b', n: 2 }
  const merged = reuse(
    [a, b],
    [
      { id: 'c', n: 3 },
      { id: 'b', n: 2 },
      { id: 'a', n: 1 },
    ],
  )
  expect(merged[1]).toBe(b)
  expect(merged[2]).toBe(a)
  expect(reuse([a, b], [{ id: 'a', n: 1 }])).toEqual([a])
})

test('a key gone, a key new, or another kind of value is a change', () => {
  expect(reuse({ a: 1, b: 2 }, { a: 1 })).toEqual({ a: 1 })
  expect(reuse({ a: undefined }, { b: undefined } as object)).toEqual({ b: undefined })
  expect(reuse([1], { 0: 1 } as object)).toEqual({ 0: 1 })
  expect(reuse({ a: null }, { a: {} })).toEqual({ a: {} })
})
