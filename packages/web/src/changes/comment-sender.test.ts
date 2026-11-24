// @vitest-environment happy-dom
import { afterEach, describe, expect, test, vi } from 'vitest'
import { api } from '../api'
import { state } from '../store'
import {
  QUIET_MS,
  deferComments,
  draftsChanged,
  flushComments,
  isPaused,
  pauseComments,
  remainingSeconds,
  resumeComments,
} from './comment-sender'

const draft = {
  id: 'lc_1',
  createdAt: '2026-11-09T00:00:00.000Z',
  path: 'a.ts',
  line: 1,
  quote: 'x',
  body: '改一下',
}

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  for (const id of Object.keys(state.lineComments)) pauseComments(id)
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

  test('tracks countdown and supports pause, resume and defer without silent submit', async () => {
    vi.useFakeTimers()
    const post = vi.spyOn(api, 'postMessage').mockResolvedValue({ message: {} as never })
    state.currentId = 'prj_a'
    state.lineComments = { thr_a: [draft] }

    draftsChanged('thr_a')
    expect(remainingSeconds('thr_a')).toBe(Math.ceil(QUIET_MS / 1000))
    expect(isPaused('thr_a')).toBe(false)

    // Pausing stops auto-send while keeping the draft intact.
    pauseComments('thr_a')
    expect(isPaused('thr_a')).toBe(true)
    expect(remainingSeconds('thr_a')).toBeNull()
    await vi.advanceTimersByTimeAsync(QUIET_MS + 1000)
    expect(post).not.toHaveBeenCalled()
    expect(state.lineComments.thr_a).toHaveLength(1)

    // Deferring on unmount also keeps drafts instead of silently submitting.
    resumeComments('thr_a')
    expect(isPaused('thr_a')).toBe(false)
    expect(remainingSeconds('thr_a')).toBe(Math.ceil(QUIET_MS / 1000))
    deferComments('thr_a')
    await vi.advanceTimersByTimeAsync(QUIET_MS + 1000)
    expect(post).not.toHaveBeenCalled()
    expect(state.lineComments.thr_a).toHaveLength(1)
  })
})
