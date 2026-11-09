import { afterAll, expect, test } from 'vitest'
import { openAICompatibleProvider as provider, requestBody } from './index'

let reply: () => Response = () => new Response('')
let seen: { path: string; auth: string | null; extra: string | null } | null = null
const server = Bun.serve({
  port: 0,
  fetch(request) {
    seen = {
      path: new URL(request.url).pathname,
      auth: request.headers.get('authorization'),
      extra: request.headers.get('x-extra'),
    }
    return reply()
  },
})
afterAll(() => server.stop(true))

const context = (env: Record<string, string>) => ({ env, home: '/nowhere', which: () => null })

test('lists the service’s models, newest first', async () => {
  reply = () =>
    Response.json({
      data: [{ id: 'old', created: 1 }, { id: 'new', created: 9 }, { id: 'undated' }, { nope: 1 }],
    })
  const listed = await provider.models!(
    { baseUrl: `http://127.0.0.1:${server.port}/v1/`, apiKey: 'sk-1' },
    { headers: { 'x-extra': 'yes' } },
  )
  expect(listed).toEqual(['new', 'old', 'undated'])
  expect(seen).toEqual({ path: '/v1/models', auth: 'Bearer sk-1', extra: 'yes' })

  reply = () => Response.json({ error: { message: 'Incorrect API key' } }, { status: 401 })
  await expect(
    provider.models!({ baseUrl: `http://127.0.0.1:${server.port}/v1`, apiKey: 'bad' }),
  ).rejects.toThrow('Incorrect API key')
})

test('finds keys in the environment, each with its service’s address', () => {
  expect(provider.find!(context({}))).toEqual([])
  expect(
    provider.find!(
      context({
        OPENAI_API_KEY: ' sk-o ',
        OPENAI_BASE_URL: 'https://proxy.test/v1',
        DEEPSEEK_API_KEY: 'sk-d',
        OPENROUTER_API_KEY: '',
      }),
    ),
  ).toEqual([
    {
      id: 'OPENAI_API_KEY',
      name: 'OpenAI',
      from: { env: 'OPENAI_API_KEY' },
      settings: { baseUrl: 'https://proxy.test/v1', apiKey: 'sk-o' },
    },
    {
      id: 'DEEPSEEK_API_KEY',
      name: 'DeepSeek',
      from: { env: 'DEEPSEEK_API_KEY' },
      settings: { baseUrl: 'https://api.deepseek.com/v1', apiKey: 'sk-d' },
    },
  ])
})

test('the output limit goes under the name the service takes', () => {
  const request = { model: 'm', messages: [], tools: [], maxTokens: 4096 }
  const own = requestBody({ baseUrl: 'https://api.openai.com/v1', apiKey: '' }, request) as Record<
    string,
    unknown
  >
  expect(own.max_completion_tokens).toBe(4096)
  expect(own.max_tokens).toBeUndefined()
  const other = requestBody({ baseUrl: 'https://api.deepseek.com/v1', apiKey: '' }, request)
  expect(other).toMatchObject({ max_tokens: 4096 })
})
