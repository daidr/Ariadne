import { anthropicProvider, readSearch, requestBody } from '../index'
import type { ModelRequest, ModelStreamPart } from '@ariadne/plugin-sdk'
import type { AnthropicSettings } from '../index'

const input = (await Bun.stdin.json()) as {
  streams: {
    request: ModelRequest
    settings: AnthropicSettings
    body: string
    status: number
    headers: Record<string, string>
  }[]
  searches: { response: Parameters<typeof readSearch>[0]; limit: number }[]
  models: unknown
  envs: Record<string, string>[]
}
Date.now = () => 1_800_000_000_000
const streams = []
for (const test of input.streams) {
  const mockFetch = async () =>
    new Response(test.body, { status: test.status, headers: test.headers ?? {} })
  globalThis.fetch = Object.assign(mockFetch, { preconnect: () => {} })
  const parts: ModelStreamPart[] = []
  for await (const part of anthropicProvider.stream(test.settings, test.request)) parts.push(part)
  streams.push({ body: requestBody(test.settings, test.request), parts })
}
const searches = input.searches.map(({ response, limit }) => {
  try {
    return { value: readSearch(response, limit), error: null }
  } catch (error) {
    return { value: null, error: error instanceof Error ? error.message : String(error) }
  }
})
const mockFetch = async () => Response.json(input.models)
globalThis.fetch = Object.assign(mockFetch, { preconnect: () => {} })
const models = await anthropicProvider.models!({ baseUrl: 'https://unused.invalid', apiKey: 'key' })
const found = input.envs.map(
  (env) => anthropicProvider.find!({ env, home: '/unused', which: () => null })[0] ?? null,
)
console.info(JSON.stringify({ streams, searches, models, found }))
