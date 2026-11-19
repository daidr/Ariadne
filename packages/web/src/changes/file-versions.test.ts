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
  for (let index = 0; index < 8; index++) await versions(`file-${index}`, null, signal)
  await versions('file-7', null, signal)
  expect(fetch).toHaveBeenCalledTimes(8)
  await versions('file-0', null, signal)
  expect(fetch).toHaveBeenCalledTimes(9)
})
