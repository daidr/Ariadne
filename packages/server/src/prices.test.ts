import { expect, test } from 'vitest'
import { ModelCatalog } from './prices'

const catalog = {
  openrouter: {
    models: { 'deepseek/deepseek-chat': { cost: { input: 0.3, output: 1.2 } } },
  },
  deepseek: {
    models: { 'deepseek-chat': { cost: { input: 0.27, output: 1.1, cache_read: 0.07 } } },
  },
  anthropic: {
    models: { 'claude-x': { cost: { input: 3, output: 15, cache_read: 0.3, cache_write: 3.75 } } },
  },
  free: { models: { 'no-price': {} } },
}

function counting() {
  let calls = 0
  const fetcher = (async () => {
    calls++
    return Response.json(catalog)
  }) as unknown as typeof fetch
  return { catalog: new ModelCatalog(fetcher), calls: () => calls }
}

test('finds a model’s list price, the maker’s first, the hinted service when given', async () => {
  const { catalog: prices, calls } = counting()
  expect(await prices.lookup('claude-x')).toEqual({
    source: 'anthropic',
    price: { currency: 'USD', input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 },
  })
  expect((await prices.lookup('deepseek-chat'))?.source).toBe('deepseek')
  expect(
    (await prices.lookup('deepseek-chat', 'openai-compatible https://openrouter.ai/api'))?.source,
  ).toBe('openrouter')
  expect(await prices.lookup('no-price')).toBeNull()
  expect(await prices.lookup('unknown')).toBeNull()
  // Fetched once, then kept.
  expect(calls()).toBe(1)
})

test('a catalog that cannot be read says so', async () => {
  const prices = new ModelCatalog(
    (async () => new Response('no', { status: 503 })) as unknown as typeof fetch,
  )
  await expect(prices.lookup('x')).rejects.toThrow('503')
})
