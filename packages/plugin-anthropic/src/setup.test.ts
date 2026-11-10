import { afterAll, expect, test } from 'vitest'
import { anthropicProvider as provider } from './index'

let seen: { path: string; key: string | null } | null = null
const server = Bun.serve({
  port: 0,
  fetch(request) {
    const url = new URL(request.url)
    seen = { path: url.pathname + url.search, key: request.headers.get('x-api-key') }
    return request.headers.get('x-api-key') === 'sk-1'
      ? Response.json({
          data: [
            {
              id: 'claude-new',
              display_name: 'Claude New',
              capabilities: {
                effort: {
                  supported: true,
                  low: { supported: true },
                  high: { supported: true },
                  max: { supported: true },
                  xhigh: { supported: false },
                },
              },
            },
            { id: 'claude-old', capabilities: { effort: { supported: false } } },
            { id: 'claude-older' },
            {},
          ],
        })
      : Response.json({ error: { message: 'invalid x-api-key' } }, { status: 401 })
  },
})
afterAll(() => server.stop(true))

const context = (env: Record<string, string>) => ({ env, home: '/nowhere', which: () => null })

test('lists the models the key can use', async () => {
  const baseUrl = `http://127.0.0.1:${server.port}/`
  expect(await provider.models!({ baseUrl, apiKey: 'sk-1' })).toEqual([
    { id: 'claude-new', title: 'Claude New', efforts: ['low', 'high', 'max'] },
    { id: 'claude-old', efforts: [] },
    { id: 'claude-older' },
  ])
  expect(seen).toEqual({ path: '/v1/models?limit=1000', key: 'sk-1' })
  await expect(provider.models!({ baseUrl, apiKey: 'bad' })).rejects.toThrow('invalid x-api-key')
})

test('finds the key in the environment', () => {
  expect(provider.find!(context({}))).toEqual([])
  expect(provider.find!(context({ ANTHROPIC_API_KEY: 'sk-a' }))).toEqual([
    {
      id: 'ANTHROPIC_API_KEY',
      from: { env: 'ANTHROPIC_API_KEY' },
      settings: { baseUrl: 'https://api.anthropic.com', apiKey: 'sk-a' },
    },
  ])
  expect(
    provider.find!(context({ ANTHROPIC_API_KEY: 'sk-a', ANTHROPIC_BASE_URL: 'https://p.test' }))[0]
      ?.settings.baseUrl,
  ).toBe('https://p.test')
})
