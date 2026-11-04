import {
  definePlugin,
  httpErrorPart,
  incompletePart,
  readServerSentEvents,
  transportErrorPart,
  type ChatMessage,
  type ModelProvider,
  type ModelStreamPart,
} from '@ariadne/plugin-sdk'

export interface OpenAICompatibleSettings {
  baseUrl: string
  apiKey: string
}

function toWire(message: ChatMessage): Record<string, unknown> {
  switch (message.role) {
    case 'assistant':
      return {
        role: 'assistant',
        content: message.content || null,
        ...(message.toolCalls?.length
          ? {
              tool_calls: message.toolCalls.map((call) => ({
                id: call.id,
                type: 'function',
                function: { name: call.name, arguments: call.arguments },
              })),
            }
          : {}),
      }
    case 'tool':
      return { role: 'tool', tool_call_id: message.toolCallId, content: message.content }
    default:
      return { role: message.role, content: message.content }
  }
}

interface ChunkToolCall {
  index: number
  id?: string
  function?: { name?: string; arguments?: string }
}

interface Chunk {
  choices?: {
    delta?: { content?: string | null; tool_calls?: ChunkToolCall[] }
    finish_reason?: string | null
  }[]
  usage?: { prompt_tokens?: number; completion_tokens?: number } | null
  error?: { message?: string }
}

function finishReason(reason: string | null): 'stop' | 'length' | 'tool-calls' {
  if (reason === 'tool_calls' || reason === 'function_call') return 'tool-calls'
  if (reason === 'length') return 'length'
  return 'stop'
}

export const openAICompatibleProvider: ModelProvider<OpenAICompatibleSettings> = {
  kind: 'openai-compatible',
  title: { 'zh-CN': 'OpenAI 兼容接口', en: 'OpenAI-compatible API' },
  settings: {
    type: 'object',
    properties: {
      baseUrl: {
        type: 'string',
        title: { 'zh-CN': '接口地址', en: 'Base URL' },
        description: { 'zh-CN': '以 /v1 结尾的地址', en: 'Usually ends with /v1' },
        default: 'https://api.openai.com/v1',
      },
      apiKey: { type: 'string', title: { 'zh-CN': 'API Key', en: 'API key' }, secret: true },
    },
    required: ['baseUrl'],
  },
  async *stream(settings, request): AsyncGenerator<ModelStreamPart> {
    let response: Response
    try {
      response = await fetch(`${settings.baseUrl.replace(/\/+$/, '')}/chat/completions`, {
        method: 'POST',
        headers: {
          'content-type': 'application/json',
          ...(settings.apiKey ? { authorization: `Bearer ${settings.apiKey}` } : {}),
        },
        body: JSON.stringify({
          model: request.model,
          messages: request.messages.map(toWire),
          ...(request.tools.length
            ? {
                tools: request.tools.map((tool) => ({
                  type: 'function',
                  function: {
                    name: tool.name,
                    description: tool.description,
                    parameters: tool.parameters,
                  },
                })),
              }
            : {}),
          stream: true,
          stream_options: { include_usage: true },
        }),
        ...(request.signal ? { signal: request.signal } : {}),
      })
    } catch (error) {
      const part = transportErrorPart(error, request.signal)
      if (part) yield part
      return
    }
    if (!response.ok || !response.body) {
      yield await httpErrorPart(response)
      return
    }
    yield* readStream(response.body, request.signal)
  },
}

async function* readStream(
  body: ReadableStream<Uint8Array>,
  signal: AbortSignal | undefined,
): AsyncGenerator<ModelStreamPart> {
  // Tool calls stream in pieces keyed by index; they are emitted whole at the end.
  const calls = new Map<number, { id: string; name: string; arguments: string }>()
  let reason: string | null = null
  let done = false
  try {
    for await (const { data } of readServerSentEvents(body)) {
      if (data === '[DONE]') {
        done = true
        break
      }
      let chunk: Chunk
      try {
        chunk = JSON.parse(data)
      } catch {
        continue
      }
      if (chunk.error) {
        yield { type: 'finish', reason: 'error', error: chunk.error.message ?? data }
        return
      }
      if (chunk.usage) {
        yield {
          type: 'usage',
          input: chunk.usage.prompt_tokens ?? 0,
          output: chunk.usage.completion_tokens ?? 0,
        }
      }
      const choice = chunk.choices?.[0]
      if (!choice) continue
      const delta = choice.delta
      if (delta?.content) yield { type: 'text', text: delta.content }
      for (const piece of delta?.tool_calls ?? []) {
        const call = calls.get(piece.index) ?? { id: '', name: '', arguments: '' }
        if (piece.id) call.id = piece.id
        if (piece.function?.name) call.name += piece.function.name
        if (piece.function?.arguments) call.arguments += piece.function.arguments
        calls.set(piece.index, call)
      }
      if (choice.finish_reason) reason = choice.finish_reason
    }
  } catch (error) {
    const part = transportErrorPart(error, signal, true)
    if (part) yield part
    return
  }
  // Some services end without `[DONE]`; a finish reason says the answer is whole too.
  if (!done && reason === null) {
    if (!signal?.aborted) yield incompletePart()
    return
  }
  for (const [index, call] of [...calls].toSorted(([a], [b]) => a - b)) {
    yield { type: 'tool-call', id: call.id || `call_${index}`, ...withoutId(call) }
  }
  // A token limit hit while streaming tool arguments still reports `length`, so the runner
  // treats the call as truncated instead of running it with broken arguments.
  yield {
    type: 'finish',
    reason: calls.size && reason !== 'length' ? 'tool-calls' : finishReason(reason),
  }
}

function withoutId(call: { name: string; arguments: string }) {
  return { name: call.name, arguments: call.arguments || '{}' }
}

export default definePlugin({
  id: 'openai-compatible',
  name: { 'zh-CN': 'OpenAI 兼容模型', en: 'OpenAI-compatible models' },
  setup(ctx) {
    return ctx.registerModelProvider(openAICompatibleProvider as ModelProvider<never>)
  },
})
