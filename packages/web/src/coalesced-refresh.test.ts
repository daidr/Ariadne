import { expect, it } from 'vitest'
import { coalescedRefresh } from './coalesced-refresh'

it('merges pending work without overlapping requests or dropping changes after failure', async () => {
  const calls: string[][] = []
  const first = Promise.withResolvers<number>()
  const refresh = coalescedRefresh<string, number>(async (keys) => {
    calls.push([...keys])
    return calls.length === 1 ? first.promise : calls.length
  })
  const current = refresh(['detail'])
  await Promise.resolve()
  const next = refresh(['conversations'])
  expect(refresh(['plan', 'conversations'])).toBe(next)
  expect(calls).toEqual([['detail']])
  const failure = expect(current).rejects.toThrow('disconnected')
  first.reject(new Error('disconnected'))
  await failure
  expect(await next).toBe(2)
  expect(calls).toEqual([['detail'], ['conversations', 'plan']])
})
