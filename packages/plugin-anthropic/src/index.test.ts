import type { ModelStreamPart } from '@ariadne/plugin-sdk'
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
    { baseUrl: `http://127.0.0.1:${server.port}`, apiKey: 'key', maxTokens: 1024 },
    {
      model: 'm',
      messages: [
        { role: 'system', content: 'sys' },
        { role: 'user', content: 'hi' },
      ],
      tools: [],
      ...(effort ? { effort } : {}),
    },
  )) {
    parts.push(part)
  }
  return parts
}

describe('anthropic provider', () => {
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
    expect(lastBody).toMatchObject({ model: 'm', max_tokens: 1024, system: 'sys', stream: true })
    expect(parts).toEqual([
      { type: 'text', text: '好的' },
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
          message: { usage: { input_tokens: 2, cache_read_input_tokens: 10, output_tokens: 1 } },
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
      max_tokens: 1024 + 2048,
      thinking: { type: 'enabled', budget_tokens: 2048 },
    })
    expect(parts).toEqual([
      { type: 'reasoning', text: '想想', signature: 's' },
      { type: 'reasoning', text: 'x', redacted: true },
      { type: 'text', text: '好' },
      { type: 'usage', input: 12, output: 5, cached: 10 },
      { type: 'finish', reason: 'stop' },
    ])
  })

  test('sends thinking back only while thinking, and only what it signed', () => {
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
    const off = requestBody(
      { baseUrl: '', apiKey: '', maxTokens: 100 },
      { model: 'm', messages, tools: [] },
    )
    expect(off).not.toHaveProperty('thinking')
    expect(JSON.stringify(off.messages)).not.toContain('thinking')
    const on = requestBody(
      { baseUrl: '', apiKey: '', maxTokens: 100 },
      { model: 'm', messages, tools: [], effort: 'high' },
    )
    expect(on.messages[1]?.content).toEqual([
      { type: 'thinking', thinking: '想', signature: 's' },
      { type: 'redacted_thinking', data: 'x' },
      { type: 'tool_use', id: 't1', name: 'f', input: {} },
    ])
  })

  test('reports a stream error event', async () => {
    reply = () => sse({ type: 'error', error: { type: 'overloaded_error', message: 'Overloaded' } })
    expect(await collect()).toEqual([
      { type: 'finish', reason: 'error', error: 'Overloaded', retryable: true },
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
        retryable: true,
        retryAfterMs: 3000,
      },
    ])
  })
})
