import { afterEach, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import { synchronizeServerClock, useServerNow } from './server-clock'

afterEach(() => vi.useRealTimers())

it('uses the server time and advances even when the browser wall clock is wrong', () => {
  vi.useFakeTimers({ toFake: ['Date', 'performance', 'setInterval', 'clearInterval'] })
  const local = 1_800_000_000_000
  vi.setSystemTime(local)
  const server = local + 3 * 60_000
  synchronizeServerClock(String(server))
  const list = effectScope()
  try {
    const now = list.run(useServerNow)!
    expect(now.value).toBe(server)

    vi.setSystemTime(local - 60_000)
    vi.advanceTimersByTime(10_000)
    expect(now.value).toBe(server + 10_000)
  } finally {
    list.stop()
  }
  expect(vi.getTimerCount()).toBe(0)
})
