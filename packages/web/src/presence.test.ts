// @vitest-environment happy-dom
import { beforeEach, describe, expect, test, vi } from 'vitest'

const noteReturn = vi.fn(async (_projectId: string, _away: number) => {})
const reportNotificationPresence = vi.fn(async () => {})
vi.mock('./api', () => ({ api: { noteReturn, reportNotificationPresence } }))
vi.mock('./store', () => ({
  state: {
    currentId: 'prj_1' as string | null,
    stream: 'open' as string,
    side: {
      root: { type: 'group', id: 'g1', tabs: ['thread:thr_1'], active: 'thread:thr_1' },
      focused: 'g1',
    },
  },
}))

const { reportPresence, touch } = await import('./presence')
const { state } = await import('./store')

const MINUTE = 60_000

describe('presence', () => {
  beforeEach(() => {
    localStorage.clear()
    noteReturn.mockClear()
    reportNotificationPresence.mockClear()
    state.currentId = 'prj_1'
    state.stream = 'open'
  })

  test('tells the server only after a long gap, with how long it was', () => {
    let now = 1_000_000_000
    touch(now)
    // The first visit has nothing to come back from.
    expect(noteReturn).not.toHaveBeenCalled()
    touch((now += 9 * MINUTE))
    expect(noteReturn).not.toHaveBeenCalled()
    touch((now += 11 * MINUTE))
    expect(noteReturn).toHaveBeenCalledExactlyOnceWith('prj_1', 11 * MINUTE)
    // Back and busy: every move since counts as being here.
    touch((now += MINUTE))
    expect(noteReturn).toHaveBeenCalledTimes(1)
  })

  test('counts each project on its own', () => {
    let now = 2_000_000_000
    touch(now)
    state.currentId = 'prj_2'
    touch((now += MINUTE))
    touch((now += 20 * MINUTE))
    expect(noteReturn).toHaveBeenCalledExactlyOnceWith('prj_2', 20 * MINUTE)
    // The first project was left 21 minutes ago, while the user was busy in the other.
    state.currentId = 'prj_1'
    touch((now += 1000))
    expect(noteReturn).toHaveBeenLastCalledWith('prj_1', 21 * MINUTE + 1000)
  })

  test('reports foreground focus heartbeat with active project/thread and skips when unauthorized', () => {
    reportPresence(true, true)
    expect(reportNotificationPresence).toHaveBeenCalledTimes(1)
    expect(reportNotificationPresence).toHaveBeenLastCalledWith(
      expect.objectContaining({
        focused: true,
        projectId: 'prj_1',
        threadId: 'thr_1',
      }),
    )

    // Immediate duplicate with same state is deduplicated.
    reportPresence(true)
    expect(reportNotificationPresence).toHaveBeenCalledTimes(1)

    // Focus loss reports immediately to start server leave grace.
    reportPresence(false)
    expect(reportNotificationPresence).toHaveBeenCalledTimes(2)
    expect(reportNotificationPresence).toHaveBeenLastCalledWith(
      expect.objectContaining({ focused: false }),
    )

    // Unauthorized sessions do not send heartbeats.
    state.stream = 'unauthorized'
    reportPresence(true, true)
    expect(reportNotificationPresence).toHaveBeenCalledTimes(2)
  })
})
