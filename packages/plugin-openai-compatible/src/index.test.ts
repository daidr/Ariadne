import type { ModelStreamPart } from '@ariadne/plugin-sdk'
import { afterAll, describe, expect, test } from 'vitest'
import { openAICompatibleProvider } from './index'

let lastBody: Record<string, unknown> | null = null
let lastAuth: string | null = null
let reply: () => Response = () => new Response('')

const server = Bun.serve({
  port: 0,
  async fetch(request) {
    lastAuth = request.headers.get('authorization')
    lastBody = (await request.json()) as Record<string, unknown>
    return reply()
  },
})
afterAll(() => server.stop(true))

const sse = (...chunks: unknown[]) =>
  new Response(
    chunks.map((c) => `data: ${typeof c === 'string' ? c : JSON.stringify(c)}\n\n`).join(''),
    { headers: { 'content-type': 'text/event-stream' } },
  )

async function collect(): Promise<ModelStreamPart[]> {
  const parts: ModelStreamPart[] = []
  for await (const part of openAICompatibleProvider.stream(
    { baseUrl: `http://127.0.0.1:${server.port}/v1/`, apiKey: 'sk-test' },
    {
      model: 'm',
      messages: [
        { role: 'system', content: 'sys' },
        { role: 'user', content: 'hi' },
        {
          role: 'assistant',
          content: '',
          toolCalls: [{ id: 'c1', name: 'f', arguments: '{"a":1}' }],
        },
        { role: 'tool', toolCallId: 'c1', content: 'ok' },
      ],
      tools: [{ name: 'f', description: 'd', parameters: { type: 'object' } }],
    },
  )) {
    parts.push(part)
  }
  return parts
}

describe('openai-compatible provider', () => {
  test('sends the conversation in wire form and assembles streamed tool calls', async () => {
    reply = () =>
      sse(
        { choices: [{ delta: { content: '好' } }] },
        {
          choices: [
            {
              delta: {
                tool_calls: [{ index: 0, id: 'x1', function: { name: 'f', arguments: '{"q"' } }],
              },
            },
          ],
        },
        {
          choices: [
            {
              delta: { tool_calls: [{ index: 0, function: { arguments: ':2}' } }] },
              finish_reason: 'tool_calls',
            },
          ],
        },
        { choices: [], usage: { prompt_tokens: 7, completion_tokens: 3 } },
        '[DONE]',
      )
    const parts = await collect()
    expect(lastAuth).toBe('Bearer sk-test')
    expect(lastBody?.messages).toEqual([
      { role: 'system', content: 'sys' },
      { role: 'user', content: 'hi' },
      {
        role: 'assistant',
        content: null,
        tool_calls: [{ id: 'c1', type: 'function', function: { name: 'f', arguments: '{"a":1}' } }],
      },
      { role: 'tool', tool_call_id: 'c1', content: 'ok' },
    ])
    expect(lastBody?.tools).toEqual([
      {
        type: 'function',
        function: { name: 'f', description: 'd', parameters: { type: 'object' } },
      },
    ])
    expect(parts).toEqual([
      { type: 'text', text: '好' },
      { type: 'usage', input: 7, output: 3 },
      { type: 'tool-call', id: 'x1', name: 'f', arguments: '{"q":2}' },
      { type: 'finish', reason: 'tool-calls' },
    ])
  })

  test('reports length when the token limit cuts off tool arguments', async () => {
    reply = () =>
      sse(
        {
          choices: [
            {
              delta: {
                tool_calls: [{ index: 0, id: 'x1', function: { name: 'f', arguments: '{"q"' } }],
              },
              finish_reason: 'length',
            },
          ],
        },
        '[DONE]',
      )
    expect((await collect()).at(-1)).toEqual({ type: 'finish', reason: 'length' })
  })

  test('turns an HTTP error into a finish part with the service message', async () => {
    reply = () => Response.json({ error: { message: 'invalid key' } }, { status: 401 })
    expect(await collect()).toEqual([
      { type: 'finish', reason: 'error', error: 'HTTP 401: invalid key' },
    ])
  })

  test('a server error and a cut-off stream are retryable; a refused connection too', async () => {
    reply = () => new Response('bad gateway', { status: 502 })
    expect((await collect()).at(-1)).toMatchObject({ reason: 'error', retryable: true })
    reply = () => sse({ choices: [{ delta: { content: '半' } }] })
    expect((await collect()).at(-1)).toMatchObject({ reason: 'error', retryable: true })
    const parts: ModelStreamPart[] = []
    for await (const part of openAICompatibleProvider.stream(
      { baseUrl: 'http://127.0.0.1:1/v1', apiKey: '' },
      { model: 'm', messages: [{ role: 'user', content: 'hi' }], tools: [] },
    )) {
      parts.push(part)
    }
    expect(parts).toEqual([
      expect.objectContaining({ type: 'finish', reason: 'error', retryable: true }),
    ])
  })
})
