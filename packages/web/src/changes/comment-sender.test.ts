// @vitest-environment happy-dom
import { afterEach, describe, expect, test, vi } from 'vitest'
import { api } from '../api'
import { state } from '../store'
import { draftsChanged, flushComments } from './comment-sender'

const draft = {
  id: 'lc_1',
  createdAt: '2026-11-09T00:00:00.000Z',
  path: 'a.ts',
  line: 1,
  quote: 'x',
  body: '改一下',
}

afterEach(() => {
  vi.restoreAllMocks()
  state.lineComments = {}
})

describe('comment sender', () => {
  test('drafts go to their own project after the user moved to another', async () => {
    const post = vi.spyOn(api, 'postMessage').mockResolvedValue({ message: {} as never })
    state.currentId = 'prj_a'
    state.lineComments = { thr_a: [draft] }
    draftsChanged('thr_a')
    // Opening another project empties the open one before the changes view goes.
    state.currentId = 'prj_b'
    state.detail = null
    expect(await flushComments('thr_a')).toBe(true)
    expect(post).toHaveBeenCalledWith('prj_a', 'thr_a', '', {
      comments: [{ path: 'a.ts', line: 1, quote: 'x', body: '改一下' }],
    })
    expect(state.lineComments.thr_a).toBeUndefined()
  })
})
