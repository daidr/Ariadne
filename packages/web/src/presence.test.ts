// @vitest-environment happy-dom
import { beforeEach, describe, expect, test, vi } from 'vitest'

const noteReturn = vi.fn(async (_projectId: string, _away: number) => {})
vi.mock('./api', () => ({ api: { noteReturn } }))
vi.mock('./store', () => ({ state: { currentId: 'prj_1' as string | null } }))

const { touch } = await import('./presence')
const { state } = await import('./store')

const MINUTE = 60_000

describe('presence', () => {
  beforeEach(() => {
    localStorage.clear()
    noteReturn.mockClear()
    state.currentId = 'prj_1'
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
})
