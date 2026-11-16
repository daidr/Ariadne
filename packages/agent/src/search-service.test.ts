import { afterAll, expect, test } from 'vitest'
import { keyedSearch } from './search-service'

let received: { path: string; auth: string | null; body: unknown } | null = null
let status = 200
let body: unknown = {
  results: [
    {
      title: 'Result',
      url: 'https://example.com',
      content: 'Tavily text',
      snippet: 'DeepSeek text',
      summary: 'DeepSeek summary',
    },
  ],
}
let redirected = false
const server = Bun.serve({
  hostname: '127.0.0.1',
  port: 0,
  async fetch(request): Promise<Response> {
    const path = new URL(request.url).pathname
    if (path === '/target') {
      redirected = true
      return Response.json(body)
    }
    received = { path, auth: request.headers.get('authorization'), body: await request.json() }
    if (status === 302) return Response.redirect(`http://127.0.0.1:${server.port}/target`)
    return Response.json(body, { status })
  },
})
afterAll(() => server.stop(true))

test('Tavily and DeepSeek use their own path prefixes, request bodies and result text', async () => {
  for (const provider of ['tavily', 'deepseek'] as const) {
    const service = keyedSearch('test-secret', fetch, {
      provider,
      baseUrl: `http://127.0.0.1:${server.port}/${provider}/`,
    })
    const hits = await service.search('test', { limit: 3 })
    expect(received).toEqual({
      path: `/${provider}/search`,
      auth: 'Bearer test-secret',
      body:
        provider === 'deepseek'
          ? { query: 'test', count: 3, need_content: false }
          : { query: 'test', max_results: 3 },
    })
    expect(hits).toEqual([
      {
        title: 'Result',
        url: 'https://example.com',
        snippet: provider === 'deepseek' ? 'DeepSeek summary' : 'Tavily text',
      },
    ])
  }
})

test('a redirect never forwards the search request; errors never include a response body', async () => {
  const service = keyedSearch('test-secret', fetch, { baseUrl: `http://127.0.0.1:${server.port}` })
  status = 302
  await expect(service.search('test', { limit: 1 })).rejects.toThrow()
  expect(redirected).toBe(false)
  status = 401
  body = { error: { message: 'sensitive echo: test-secret' } }
  await expect(service.search('test', { limit: 1 })).rejects.toThrow('HTTP 401')
  status = 200
  await expect(service.search('test', { limit: 1 })).rejects.toThrow('Invalid search response')
})
