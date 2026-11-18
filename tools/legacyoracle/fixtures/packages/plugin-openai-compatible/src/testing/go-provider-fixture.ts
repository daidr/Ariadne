/** Test oracle for comparing the native provider with the previous implementation. */
import { openAICompatibleProvider, requestBody } from '../index'
import type { ModelRequest, ModelStreamPart } from '@ariadne/plugin-sdk'
import type { OpenAICompatibleSettings } from '../index'

const input = (await Bun.stdin.json()) as {
  request: ModelRequest
  settings: OpenAICompatibleSettings
  body: string
  status: number
  headers: Record<string, string>
}
Date.now = () => 1_800_000_000_000
const mockFetch = async () =>
  new Response(input.body, {
    status: input.status,
    headers: input.headers ?? {},
  })
globalThis.fetch = Object.assign(mockFetch, { preconnect: () => {} })
const parts: ModelStreamPart[] = []
for await (const part of openAICompatibleProvider.stream(input.settings, input.request))
  parts.push(part)
console.info(JSON.stringify({ body: requestBody(input.settings, input.request), parts }))
