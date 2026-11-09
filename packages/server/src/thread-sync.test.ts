import { describe, expect, it } from 'vitest'
import { ThreadSync } from './thread-sync'

const thread = (id: string, title = id) => ({ id, title })

describe('ThreadSync', () => {
  it('sends everything to a page that names no cursor', () => {
    const sync = new ThreadSync()
    const all = [thread('a'), thread('b')]
    const first = sync.delta('p', all, null)
    expect(first).toEqual({ cursor: first.cursor, partial: false, threads: all })
  })

  it('sends nothing when nothing changed since the cursor', () => {
    const sync = new ThreadSync()
    const { cursor } = sync.delta('p', [thread('a'), thread('b')], null)
    expect(sync.delta('p', [thread('a'), thread('b')], cursor)).toEqual({
      cursor,
      partial: true,
      threads: [],
    })
  })

  it('sends only the threads that changed, to every page that has not seen them', () => {
    const sync = new ThreadSync()
    const { cursor } = sync.delta('p', [thread('a'), thread('b')], null)
    const next = [thread('a'), thread('b', 'renamed')]
    const one = sync.delta('p', next, cursor)
    expect(one).toEqual({ cursor: one.cursor, partial: true, threads: [next[1]] })
    expect(one.cursor).not.toBe(cursor)
    // Another page, still at the first cursor, gets the same change.
    expect(sync.delta('p', next, cursor).threads).toEqual([next[1]])
    expect(sync.delta('p', next, one.cursor).threads).toEqual([])
  })

  it('names every id when a thread came or went', () => {
    const sync = new ThreadSync()
    const { cursor } = sync.delta('p', [thread('a'), thread('b')], null)
    const added = sync.delta('p', [thread('a'), thread('b'), thread('c')], cursor)
    expect(added.threads).toEqual([thread('c')])
    expect(added.ids).toEqual(['a', 'b', 'c'])
    const removed = sync.delta('p', [thread('a'), thread('c')], added.cursor)
    expect(removed.threads).toEqual([])
    expect(removed.ids).toEqual(['a', 'c'])
    // A thread replaced by another leaves the count as it was, and is still noticed.
    const swapped = sync.delta('p', [thread('a'), thread('d')], removed.cursor)
    expect(swapped.threads).toEqual([thread('d')])
    expect(swapped.ids).toEqual(['a', 'd'])
  })

  it('sends everything for a cursor it did not give', () => {
    const sync = new ThreadSync()
    const all = [thread('a')]
    const { cursor } = sync.delta('p', all, null)
    for (const since of ['', 'x', 'other.1', `${cursor.split('.')[0]}.99`, `${cursor}x`])
      expect(sync.delta('p', all, since).partial).toBe(false)
    expect(new ThreadSync().delta('p', all, cursor).partial).toBe(false)
    // Forgotten and seen again, it is all new to a page that knew it before.
    sync.forget('p')
    expect(sync.delta('p', all, cursor)).toMatchObject({ threads: all, ids: ['a'] })
  })

  it('keeps projects apart', () => {
    const sync = new ThreadSync()
    const { cursor } = sync.delta('p', [thread('a')], null)
    sync.delta('q', [thread('z')], null)
    expect(sync.delta('p', [thread('a')], cursor).threads).toEqual([])
  })
})
