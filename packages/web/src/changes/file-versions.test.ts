import { afterEach, expect, it, vi } from 'vitest'
import { api } from '../api'
import { fileVersionsFor } from './file-versions'

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

it('reuses immutable file versions within a comparison and expires old entries', async () => {
  vi.useFakeTimers()
  const file = { before: 'old', after: 'new', skipped: null }
  const fetch = vi.spyOn(api, 'getChangedFile').mockResolvedValue({ file })
  const { signal } = new AbortController()
  const versions = fileVersionsFor('thread', 'base', 'head')
  expect(await versions('renamed.go', 'old.go', signal)).toBe(file)
  expect(await versions('renamed.go', 'old.go', signal)).toBe(file)
  expect(fetch).toHaveBeenCalledTimes(1)
  await versions('renamed.go', 'other.go', signal)
  await fileVersionsFor('thread', 'base', 'new-head')('renamed.go', 'old.go', signal)
  expect(fetch).toHaveBeenCalledTimes(3)
  vi.advanceTimersByTime(121_000)
  await versions('renamed.go', 'old.go', signal)
  expect(fetch).toHaveBeenCalledTimes(4)
})

it('evicts old files once their combined text reaches the cache limit', async () => {
  const file = { before: 'x'.repeat(512 * 1024), after: '', skipped: null }
  const fetch = vi.spyOn(api, 'getChangedFile').mockResolvedValue({ file })
  const { signal } = new AbortController()
  const versions = fileVersionsFor('thread', 'base', 'head')
  for (let index = 0; index < 24; index++) await versions(`file-${index}`, null, signal)
  await versions('file-23', null, signal)
  expect(fetch).toHaveBeenCalledTimes(24)
  await versions('file-0', null, signal)
  expect(fetch).toHaveBeenCalledTimes(25)
})

it('batches concurrent file requests, deduplicates in-flight loads, and serves peek synchronously', async () => {
  const a = { before: 'a0', after: 'a1', skipped: null }
  const b = { before: 'b0', after: 'b1', skipped: null }
  const batch = vi.spyOn(api, 'getChangedFiles').mockResolvedValue({ files: [a, b] })
  const single = vi.spyOn(api, 'getChangedFile')
  const { signal } = new AbortController()
  const versions = fileVersionsFor('thread', 'base', 'head')

  const [resA1, resA2, resB] = await Promise.all([
    versions('a.ts', null, signal),
    versions('a.ts', null, signal),
    versions('b.ts', 'old-b.ts', signal),
  ])
  expect(resA1).toBe(a)
  expect(resA2).toBe(a)
  expect(resB).toBe(b)
  expect(batch).toHaveBeenCalledTimes(1)
  expect(batch).toHaveBeenCalledWith('thread', {
    from: 'base',
    head: 'head',
    files: [
      { path: 'a.ts', previousPath: null },
      { path: 'b.ts', previousPath: 'old-b.ts' },
    ],
  })
  expect(single).not.toHaveBeenCalled()
  expect(versions.peek('a.ts', null)).toBe(a)
  expect(versions.peek('b.ts', 'old-b.ts')).toBe(b)
})

it('prefetches uncached files in a batch without re-fetching cached entries', async () => {
  const a = { before: 'a0', after: 'a1', skipped: null }
  const b = { before: 'b0', after: 'b1', skipped: null }
  const batch = vi.spyOn(api, 'getChangedFiles').mockResolvedValue({ files: [a, b] })
  const versions = fileVersionsFor('thread', 'base', 'head')

  versions.prefetch([
    { path: 'a.ts', previousPath: null },
    { path: 'b.ts', previousPath: null },
  ])
  await Promise.resolve()
  await Promise.resolve()
  expect(batch).toHaveBeenCalledTimes(1)
  expect(versions.peek('a.ts', null)).toBe(a)
  expect(versions.peek('b.ts', null)).toBe(b)

  versions.prefetch([{ path: 'a.ts', previousPath: null }])
  await Promise.resolve()
  expect(batch).toHaveBeenCalledTimes(1)
})
