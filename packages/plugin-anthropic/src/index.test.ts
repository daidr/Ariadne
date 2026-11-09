import { ModelSearchRefused, type ModelStreamPart } from '@ariadne/plugin-sdk'
import { afterAll, describe, expect, test } from 'vitest'
import { anthropicProvider, requestBody, toWire } from './index'

let lastBody: Record<string, unknown> | null = null
let lastHeaders: Headers | null = null
let reply: () => Response = () => new Response('')

const server = Bun.serve({
  port: 0,
  async fetch(request) {
    lastHeaders = request.headers
    lastBody = (await request.json()) as Record<string, unknown>
    return reply()
  },
})
afterAll(() => server.stop(true))

const sse = (...events: { type: string; [key: string]: unknown }[]) =>
  new Response(events.map((e) => `event: ${e.type}\ndata: ${JSON.stringify(e)}\n\n`).join(''), {
    headers: { 'content-type': 'text/event-stream' },
  })

async function collect(effort?: 'low'): Promise<ModelStreamPart[]> {
  const parts: ModelStreamPart[] = []
  for await (const part of anthropicProvider.stream(
    { baseUrl: `http://127.0.0.1:${server.port}`, apiKey: 'key' },
    {
      model: 'm',
      messages: [
        { role: 'system', content: 'sys' },
        { role: 'user', content: 'hi' },
      ],
      tools: [],
      maxTokens: 1024,
      headers: { 'x-extra': 'yes', 'x-api-key': 'not this' },
      extraBody: { temperature: 0.2, model: 'not this' },
      ...(effort ? { effort } : {}),
    },
  )) {
    // What says the service is still there is tested on its own.
    if (part.type !== 'alive') parts.push(part)
  }
  return parts
}

const tool = (name: string) => ({ name, description: name, parameters: { type: 'object' } })

describe('anthropic provider', () => {
  test('sends images after the text, and a tool result with its images', () => {
    const image = { mime: 'image/png', data: 'AAAA' }
    const source = { type: 'base64', media_type: 'image/png', data: 'AAAA' }
    expect(
      toWire([
        { role: 'user', content: 'look', images: [image] },
        { role: 'assistant', content: '', toolCalls: [{ id: 't1', name: 'f', arguments: '{}' }] },
        { role: 'tool', toolCallId: 't1', content: 'the file', images: [image] },
      ]).messages,
    ).toEqual([
      {
        role: 'user',
        content: [
          { type: 'text', text: 'look' },
          { type: 'image', source },
        ],
      },
      { role: 'assistant', content: [{ type: 'tool_use', id: 't1', name: 'f', input: {} }] },
      {
        role: 'user',
        content: [
          {
            type: 'tool_result',
            tool_use_id: 't1',
            content: [
              { type: 'text', text: 'the file' },
              { type: 'image', source },
            ],
          },
        ],
      },
    ])
  })

  test('merges neighbouring roles and puts tool results in a user message', () => {
    expect(
      toWire([
        { role: 'system', content: 'a' },
        { role: 'user', content: 'q' },
        { role: 'user', content: 'note' },
        {
          role: 'assistant',
          content: 'let me',
          toolCalls: [{ id: 't1', name: 'f', arguments: '{"x":1}' }],
        },
        { role: 'tool', toolCallId: 't1', content: 'bad', isError: true },
        { role: 'user', content: 'later' },
      ]),
    ).toEqual({
      system: 'a',
      messages: [
        {
          role: 'user',
          content: [
            { type: 'text', text: 'q' },
            { type: 'text', text: 'note' },
          ],
        },
        {
          role: 'assistant',
          content: [
            { type: 'text', text: 'let me' },
            { type: 'tool_use', id: 't1', name: 'f', input: { x: 1 } },
          ],
        },
        {
          role: 'user',
          content: [
            { type: 'tool_result', tool_use_id: 't1', content: 'bad', is_error: true },
            { type: 'text', text: 'later' },
          ],
        },
      ],
    })
  })

  test('streams text, tool calls and usage', async () => {
    reply = () =>
      sse(
        { type: 'message_start', message: { usage: { input_tokens: 12, output_tokens: 1 } } },
        { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } },
        { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: '好的' } },
        { type: 'content_block_stop', index: 0 },
        {
          type: 'content_block_start',
          index: 1,
          content_block: { type: 'tool_use', id: 'tu1', name: 'f', input: {} },
        },
        {
          type: 'content_block_delta',
          index: 1,
          delta: { type: 'input_json_delta', partial_json: '{"a"' },
        },
        {
          type: 'content_block_delta',
          index: 1,
          delta: { type: 'input_json_delta', partial_json: ':1}' },
        },
        { type: 'content_block_stop', index: 1 },
        { type: 'message_delta', delta: { stop_reason: 'tool_use' }, usage: { output_tokens: 9 } },
        { type: 'message_stop' },
      )
    const parts = await collect()
    expect(lastHeaders?.get('x-api-key')).toBe('key')
    expect(lastHeaders?.get('anthropic-version')).toBe('2023-06-01')
    // The user's header and body fields go along; the provider's own are not replaced.
    expect(lastHeaders?.get('x-extra')).toBe('yes')
    expect(lastBody).toMatchObject({
      model: 'm',
      temperature: 0.2,
      max_tokens: 1024,
      system: [{ type: 'text', text: 'sys', cache_control: { type: 'ephemeral' } }],
      stream: true,
    })
    expect(parts).toEqual([
      { type: 'text', text: '好的' },
      { type: 'calling' },
      { type: 'tool-input', call: 0, name: 'f', arguments: '{"a"' },
      { type: 'tool-input', call: 0, name: 'f', arguments: '{"a":1}' },
      { type: 'tool-call', id: 'tu1', name: 'f', arguments: '{"a":1}' },
      { type: 'usage', input: 12, output: 9 },
      { type: 'finish', reason: 'tool-calls' },
    ])
  })

  test('thinks when asked, streams its thinking back whole, and counts cached input', async () => {
    reply = () =>
      sse(
        {
          type: 'message_start',
          message: {
            usage: {
              input_tokens: 2,
              cache_creation_input_tokens: 3,
              cache_read_input_tokens: 10,
              output_tokens: 1,
            },
          },
        },
        { type: 'content_block_start', index: 0, content_block: { type: 'thinking' } },
        {
          type: 'content_block_delta',
          index: 0,
          delta: { type: 'thinking_delta', thinking: '想' },
        },
        {
          type: 'content_block_delta',
          index: 0,
          delta: { type: 'thinking_delta', thinking: '想' },
        },
        {
          type: 'content_block_delta',
          index: 0,
          delta: { type: 'signature_delta', signature: 's' },
        },
        { type: 'content_block_stop', index: 0 },
        {
          type: 'content_block_start',
          index: 1,
          content_block: { type: 'redacted_thinking', data: 'x' },
        },
        { type: 'content_block_stop', index: 1 },
        { type: 'content_block_start', index: 2, content_block: { type: 'text', text: '' } },
        { type: 'content_block_delta', index: 2, delta: { type: 'text_delta', text: '好' } },
        { type: 'content_block_stop', index: 2 },
        { type: 'message_delta', delta: { stop_reason: 'end_turn' }, usage: { output_tokens: 5 } },
        { type: 'message_stop' },
      )
    const parts = await collect('low')
    expect(lastBody).toMatchObject({
      max_tokens: 1024,
      thinking: { type: 'adaptive', display: 'summarized' },
      output_config: { effort: 'low' },
    })
    expect(lastBody).not.toHaveProperty('thinking.budget_tokens')
    expect(parts).toEqual([
      { type: 'reasoning', text: '想想', signature: 's', startedAt: expect.any(Number) },
      { type: 'reasoning', text: 'x', redacted: true, startedAt: expect.any(Number) },
      { type: 'text', text: '好' },
      { type: 'usage', input: 15, output: 5, cached: 10, cacheWrite: 3 },
      { type: 'finish', reason: 'stop' },
    ])
  })

  test('sends signed thinking back with or without effort, and nothing unsigned', () => {
    const messages = [
      { role: 'user' as const, content: 'hi' },
      {
        role: 'assistant' as const,
        content: '',
        toolCalls: [{ id: 't1', name: 'f', arguments: '{}' }],
        reasoning: [
          { text: '想', signature: 's' },
          { text: 'x', redacted: true },
          { text: '别家的思考' },
        ],
      },
      { role: 'tool' as const, toolCallId: 't1', content: 'ok' },
    ]
    const off = requestBody({ baseUrl: '', apiKey: '' }, { model: 'm', messages, tools: [] })
    expect(off).not.toHaveProperty('thinking')
    expect(off).not.toHaveProperty('output_config')
    const on = requestBody(
      { baseUrl: '', apiKey: '' },
      { model: 'm', messages, tools: [], effort: 'high' },
    )
    expect(on.messages[1]?.content).toEqual([
      { type: 'thinking', thinking: '想', signature: 's' },
      { type: 'redacted_thinking', data: 'x' },
      { type: 'tool_use', id: 't1', name: 'f', input: {} },
    ])
    expect(off.messages).toEqual(on.messages)
  })

  test('caches the tools, the system text, the previous request and this one', () => {
    const settings = { baseUrl: '', apiKey: '' }
    const messages = [
      { role: 'system' as const, content: 'sys' },
      { role: 'user' as const, content: 'hi' },
      {
        role: 'assistant' as const,
        content: '',
        toolCalls: [{ id: 't1', name: 'f', arguments: '{}' }],
      },
      { role: 'tool' as const, toolCallId: 't1', content: 'one' },
      {
        role: 'assistant' as const,
        content: 'next',
        reasoning: [{ text: '想', signature: 's' }],
        toolCalls: [{ id: 't2', name: 'f', arguments: '{}' }],
      },
      { role: 'tool' as const, toolCallId: 't2', content: 'two' },
      { role: 'user' as const, content: 'also this' },
    ]
    const point = { type: 'ephemeral' }
    const body = requestBody(settings, {
      model: 'm',
      messages,
      tools: [tool('a'), tool('b')],
    })
    expect(body.tools?.map((spec) => 'cache_control' in spec)).toEqual([false, true])
    expect(body.system).toEqual([{ type: 'text', text: 'sys', cache_control: point }])
    // Where the request before this one ended, and where this one ends.
    expect(body.messages[2]?.content).toEqual([
      { type: 'tool_result', tool_use_id: 't1', content: 'one', cache_control: point },
    ])
    expect(body.messages[4]?.content).toEqual([
      { type: 'tool_result', tool_use_id: 't2', content: 'two' },
      { type: 'text', text: 'also this', cache_control: point },
    ])
    const points = JSON.stringify(body).match(/cache_control/g) ?? []
    expect(points).toHaveLength(4)

    // A request that ends on a step: the point goes on its call, never on its thinking.
    const ending = requestBody(settings, {
      model: 'm',
      messages: messages.slice(0, 5),
      tools: [],
    })
    expect(ending.messages.at(-1)?.content.at(-1)).toMatchObject({
      type: 'tool_use',
      cache_control: point,
    })
    expect(ending.messages.at(-1)?.content[0]).not.toHaveProperty('cache_control')
    expect(ending).not.toHaveProperty('tools')
  })

  test('a one-off request is not cached', () => {
    const body = requestBody(
      { baseUrl: '', apiKey: '' },
      {
        model: 'm',
        messages: [
          { role: 'system', content: 'sys' },
          { role: 'user', content: 'a' },
          { role: 'assistant', content: 'b' },
          { role: 'user', content: 'summarise' },
        ],
        tools: [{ name: 'f', description: 'f', parameters: {} }],
        cache: false,
      },
    )
    expect(body.system).toBe('sys')
    expect(JSON.stringify(body)).not.toContain('cache_control')
  })

  test('reports a stream error event', async () => {
    reply = () => sse({ type: 'error', error: { type: 'overloaded_error', message: 'Overloaded' } })
    expect(await collect()).toEqual([
      { type: 'finish', reason: 'error', error: 'Overloaded', retryable: true, busy: true },
    ])
  })

  test('a stream that stops before message_stop is incomplete and retryable', async () => {
    reply = () =>
      sse(
        { type: 'message_start', message: { usage: { input_tokens: 1, output_tokens: 0 } } },
        { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: '半' } },
      )
    const parts = await collect()
    expect(parts.at(-1)).toMatchObject({ type: 'finish', reason: 'error', retryable: true })
  })

  test('a rate limit is retryable after the wait the service asks for', async () => {
    reply = () =>
      Response.json(
        { error: { message: 'slow down' } },
        { status: 429, headers: { 'retry-after': '3' } },
      )
    expect(await collect()).toEqual([
      {
        type: 'finish',
        reason: 'error',
        error: 'HTTP 429: slow down',
        busy: true,
        retryable: true,
        retryAfterMs: 3000,
      },
    ])
  })
})

const searchSettings = () => ({ baseUrl: `http://127.0.0.1:${server.port}`, apiKey: 'key' })
const askSearch = () =>
  anthropicProvider.search!.run(searchSettings(), { model: 'm', query: 'q', limit: 5 })

describe("the service's own web search", () => {
  test('only the service itself is taken to search, not a relay under another address', () => {
    const { available } = anthropicProvider.search!
    expect(available({ baseUrl: 'https://api.anthropic.com/', apiKey: 'k' })).toBe(true)
    expect(available({ baseUrl: 'https://relay.example/anthropic', apiKey: 'k' })).toBe(false)
    expect(available({ baseUrl: 'not an address', apiKey: 'k' })).toBe(false)
  })

  test('one request with the search tool; the answer, its quoted pages first, and the tokens', async () => {
    reply = () =>
      Response.json({
        stop_reason: 'end_turn',
        content: [
          { type: 'server_tool_use', id: 'srvtoolu_1', name: 'web_search', input: { query: 'q' } },
          {
            type: 'web_search_tool_result',
            tool_use_id: 'srvtoolu_1',
            content: [
              {
                type: 'web_search_result',
                url: 'https://a.example',
                title: 'A',
                encrypted_content: 'x',
              },
              {
                type: 'web_search_result',
                url: 'https://b.example',
                title: 'B',
                encrypted_content: 'y',
              },
            ],
          },
          { type: 'text', text: 'B says ' },
          {
            type: 'text',
            text: 'it shipped.',
            citations: [
              {
                type: 'web_search_result_location',
                url: 'https://b.example',
                title: 'B',
                cited_text: 'It shipped today.',
              },
            ],
          },
        ],
        usage: { input_tokens: 100, cache_read_input_tokens: 20, output_tokens: 30 },
      })
    const answer = await anthropicProvider.search!.run(searchSettings(), {
      model: 'm',
      query: 'did it ship',
      limit: 5,
      headers: { 'x-extra': 'yes', 'x-api-key': 'not this' },
    })
    expect(lastHeaders?.get('x-api-key')).toBe('key')
    expect(lastHeaders?.get('x-extra')).toBe('yes')
    expect(lastBody).toMatchObject({
      model: 'm',
      tools: [{ type: 'web_search_20250305', name: 'web_search', max_uses: 2 }],
    })
    expect(JSON.stringify(lastBody?.messages)).toContain('Query: did it ship')
    expect(lastBody).not.toHaveProperty('stream')
    expect(answer).toEqual({
      text: 'B says it shipped.',
      sources: [
        { title: 'B', url: 'https://b.example', snippet: 'It shipped today.' },
        { title: 'A', url: 'https://a.example', snippet: '' },
      ],
      usage: { input: 120, output: 30, cached: 20 },
    })
  })

  test('a search the service could not run, or a refused request, is an error', async () => {
    reply = () =>
      Response.json({
        stop_reason: 'end_turn',
        content: [
          {
            type: 'web_search_tool_result',
            tool_use_id: 't',
            content: { type: 'web_search_tool_result_error', error_code: 'max_uses_exceeded' },
          },
          { type: 'text', text: 'I could not search.' },
        ],
      })
    await expect(askSearch()).rejects.toThrow('max_uses_exceeded')
    reply = () => Response.json({ error: { message: 'bad key sk-secret' } }, { status: 401 })
    await expect(askSearch()).rejects.toThrow(/^HTTP 401$/)
    // Turned down outright, as when search is off for the organisation: not worth asking again.
    await expect(askSearch()).rejects.toBeInstanceOf(ModelSearchRefused)
    reply = () => Response.json({ error: { message: 'overloaded' } }, { status: 529 })
    await expect(askSearch()).rejects.not.toBeInstanceOf(ModelSearchRefused)
    reply = () =>
      Response.json({
        stop_reason: 'max_tokens',
        content: [
          {
            type: 'web_search_tool_result',
            tool_use_id: 't',
            content: [{ url: 'https://a.example' }],
          },
        ],
      })
    await expect(askSearch()).rejects.toThrow('ended early')
  })
})
