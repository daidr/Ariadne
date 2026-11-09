import type { ModelStreamPart } from '@ariadne/plugin-sdk'
import { afterAll, describe, expect, test } from 'vitest'
import { openAICompatibleProvider, requestBody, toWire } from './index'

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
    // What says the service is still there is tested on its own.
    if (part.type !== 'alive') parts.push(part)
  }
  return parts
}

describe('openai-compatible provider', () => {
  test("sends images as parts, and tool images right after the step's results", () => {
    const image = { mime: 'image/png', data: 'AAAA' }
    const url = { type: 'image_url', image_url: { url: 'data:image/png;base64,AAAA' } }
    expect(
      toWire([
        { role: 'user', content: 'look', images: [image] },
        {
          role: 'assistant',
          content: '',
          toolCalls: [
            { id: 't1', name: 'f', arguments: '{}' },
            { id: 't2', name: 'g', arguments: '{}' },
          ],
        },
        { role: 'tool', toolCallId: 't1', content: 'a picture', images: [image] },
        { role: 'tool', toolCallId: 't2', content: 'text' },
        { role: 'user', content: 'next' },
      ]),
    ).toEqual([
      { role: 'user', content: [{ type: 'text', text: 'look' }, url] },
      {
        role: 'assistant',
        content: null,
        tool_calls: [
          { id: 't1', type: 'function', function: { name: 'f', arguments: '{}' } },
          { id: 't2', type: 'function', function: { name: 'g', arguments: '{}' } },
        ],
      },
      { role: 'tool', tool_call_id: 't1', content: 'a picture' },
      { role: 'tool', tool_call_id: 't2', content: 'text' },
      {
        role: 'user',
        content: [{ type: 'text', text: 'Images from the result of tool call t1:' }, url],
      },
      { role: 'user', content: 'next' },
    ])
  })

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
      { type: 'calling' },
      { type: 'tool-input', call: 0, name: 'f', arguments: '{"q"' },
      { type: 'tool-input', call: 0, name: 'f', arguments: '{"q":2}' },
      { type: 'usage', input: 7, output: 3 },
      { type: 'tool-call', id: 'x1', name: 'f', arguments: '{"q":2}' },
      { type: 'finish', reason: 'tool-calls' },
    ])
  })

  test('asks for an effort, and passes on reasoning and detailed usage', async () => {
    reply = () =>
      sse(
        { choices: [{ delta: { reasoning_content: '先想' } }] },
        { choices: [{ delta: { reasoning_content: '一下' } }] },
        { choices: [{ delta: { content: '好' }, finish_reason: 'stop' }] },
        {
          choices: [],
          usage: {
            prompt_tokens: 20,
            completion_tokens: 8,
            prompt_tokens_details: { cached_tokens: 16 },
            completion_tokens_details: { reasoning_tokens: 6 },
          },
        },
        '[DONE]',
      )
    const parts: ModelStreamPart[] = []
    for await (const part of openAICompatibleProvider.stream(
      { baseUrl: `http://127.0.0.1:${server.port}/v1`, apiKey: '' },
      { model: 'm', messages: [{ role: 'user', content: 'hi' }], tools: [], effort: 'medium' },
    )) {
      if (part.type !== 'alive') parts.push(part)
    }
    expect(lastBody).toMatchObject({ reasoning_effort: 'medium' })
    expect(parts).toEqual([
      { type: 'reasoning', text: '先想一下', startedAt: expect.any(Number) },
      { type: 'text', text: '好' },
      { type: 'usage', input: 20, output: 8, cached: 16, reasoning: 6 },
      { type: 'finish', reason: 'stop' },
    ])
  })

  test('says it is alive on every event, keep-alive comments included', async () => {
    reply = () =>
      new Response(
        [
          ': keep-alive\n\n',
          `data: ${JSON.stringify({ choices: [{ delta: { reasoning_content: 'hm' } }] })}\n\n`,
          ': keep-alive\n\n',
          `data: ${JSON.stringify({ choices: [{ delta: { content: 'ok' }, finish_reason: 'stop' }] })}\n\n`,
          'data: [DONE]\n\n',
        ].join(''),
        { headers: { 'content-type': 'text/event-stream' } },
      )
    const all: ModelStreamPart['type'][] = []
    for await (const part of openAICompatibleProvider.stream(
      { baseUrl: `http://127.0.0.1:${server.port}/v1/`, apiKey: 'sk-test' },
      { model: 'm', messages: [{ role: 'user', content: 'hi' }], tools: [] },
    )) {
      all.push(part.type)
    }
    // Reasoning is handed on whole, later; the events before it still count.
    expect(all.slice(0, 5)).toEqual(['alive', 'alive', 'alive', 'alive', 'reasoning'])
    expect(all).toContain('text')
  })

  test('reads cache hits where DeepSeek reports them', async () => {
    reply = () =>
      sse(
        { choices: [{ delta: { content: '好' }, finish_reason: 'stop' }] },
        {
          choices: [],
          usage: {
            prompt_tokens: 20,
            completion_tokens: 2,
            prompt_cache_hit_tokens: 12,
            prompt_cache_miss_tokens: 8,
          },
        },
        '[DONE]',
      )
    const parts: ModelStreamPart[] = []
    for await (const part of openAICompatibleProvider.stream(
      { baseUrl: `http://127.0.0.1:${server.port}/v1`, apiKey: '' },
      { model: 'm', messages: [{ role: 'user', content: 'hi' }], tools: [] },
    )) {
      parts.push(part)
    }
    expect(parts).toContainEqual({ type: 'usage', input: 20, output: 2, cached: 12 })
  })

  test('sends reasoning back with the tool calls it led to', () => {
    const body = requestBody(
      { baseUrl: '', apiKey: '' },
      {
        model: 'm',
        tools: [],
        messages: [
          { role: 'user', content: 'hi' },
          {
            role: 'assistant',
            content: '',
            toolCalls: [{ id: 't1', name: 'f', arguments: '{}' }],
            reasoning: [{ text: '先想' }, { text: '密', redacted: true }, { text: '一下' }],
          },
          { role: 'tool', toolCallId: 't1', content: 'ok' },
          { role: 'assistant', content: '好' },
        ],
      },
    )
    expect(body.messages[1]).toMatchObject({ role: 'assistant', reasoning_content: '先想一下' })
    // Once the model thinks, no assistant message goes without reasoning.
    expect(body.messages[3]).toMatchObject({ role: 'assistant', reasoning_content: '' })
    const plain = requestBody(
      { baseUrl: '', apiKey: '' },
      {
        model: 'm',
        tools: [],
        messages: [
          { role: 'user', content: 'hi' },
          { role: 'assistant', content: '好' },
        ],
      },
    )
    expect(plain.messages[1]).not.toHaveProperty('reasoning_content')
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

  test('a server error and a cut-off stream are retryable; a wrong address is not', async () => {
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
    expect(parts).toHaveLength(1)
    expect(parts[0]).toMatchObject({ type: 'finish', reason: 'error' })
    expect(parts[0]).not.toHaveProperty('retryable')
  })

  test('a wait longer than a minute fails at once and says how long', async () => {
    reply = () =>
      Response.json(
        { error: { message: 'quota' } },
        { status: 429, headers: { 'retry-after': '1800' } },
      )
    const [part] = await collect()
    expect(part).toEqual({
      type: 'finish',
      reason: 'error',
      error: 'HTTP 429: quota (the service asks to wait 30 min before trying again)',
      // Too long to wait for, but another model may answer.
      busy: true,
    })
  })
})
