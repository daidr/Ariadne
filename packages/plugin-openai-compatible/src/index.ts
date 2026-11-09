import {
  definePlugin,
  httpErrorPart,
  incompletePart,
  readServerSentEvents,
  transportErrorPart,
  type ChatMessage,
  type ImagePart,
  type ModelProvider,
  type ModelRequest,
  type ModelStreamPart,
  withExtraBody,
} from '@ariadne/plugin-sdk'

export interface OpenAICompatibleSettings {
  baseUrl: string
  apiKey: string
}

const imageUrl = (image: ImagePart) => ({
  type: 'image_url',
  image_url: { url: `data:${image.mime};base64,${image.data}` },
})

/** Text, then images; a message without images keeps plain string content. */
function userContent(text: string, images: ImagePart[] = []) {
  if (!images.length) return text
  return [...(text ? [{ type: 'text', text }] : []), ...images.map(imageUrl)]
}

/** A message's reasoning as one text, without what the service hid. */
const reasoningOf = (message: ChatMessage) =>
  message.role === 'assistant'
    ? (message.reasoning ?? [])
        .filter((block) => !block.redacted)
        .map((block) => block.text)
        .join('')
    : ''

/**
 * The wire messages. Tool messages take only text here, so the images of a step's tool
 * results follow its last result in one user message, saying which call they came from.
 */
export function toWire(messages: ChatMessage[]): Record<string, unknown>[] {
  const wire: Record<string, unknown>[] = []
  let pending: { text: string; images: ImagePart[] }[] = []
  const flush = () => {
    if (!pending.length) return
    wire.push({
      role: 'user',
      content: pending.flatMap((entry) => [
        { type: 'text', text: entry.text },
        ...entry.images.map(imageUrl),
      ]),
    })
    pending = []
  }
  // Reasoning goes back as it came: services that think by default (DeepSeek) refuse every
  // earlier assistant message without it once tools are in play. Only services that sent
  // some have any; then a message with none (written before the model thought, or by
  // another model) gets an empty one, so none is missing.
  const thinks = messages.some((message) => reasoningOf(message))
  for (const message of messages) {
    if (message.role !== 'tool') flush()
    switch (message.role) {
      case 'assistant': {
        const reasoning = reasoningOf(message)
        wire.push({
          role: 'assistant',
          content: message.content || null,
          ...(thinks ? { reasoning_content: reasoning } : {}),
          ...(message.toolCalls?.length
            ? {
                tool_calls: message.toolCalls.map((call) => ({
                  id: call.id,
                  type: 'function',
                  function: { name: call.name, arguments: call.arguments },
                })),
              }
            : {}),
        })
        break
      }
      case 'tool':
        wire.push({ role: 'tool', tool_call_id: message.toolCallId, content: message.content })
        if (message.images?.length) {
          pending.push({
            text: `Images from the result of tool call ${message.toolCallId}:`,
            images: message.images,
          })
        }
        break
      case 'user':
        wire.push({ role: 'user', content: userContent(message.content, message.images) })
        break
      case 'system':
        wire.push({ role: 'system', content: message.content })
        break
    }
  }
  flush()
  return wire
}

interface ChunkToolCall {
  index: number
  id?: string
  function?: { name?: string; arguments?: string }
}

interface Chunk {
  choices?: {
    delta?: {
      content?: string | null
      // Services that show reasoning name it one of these.
      reasoning_content?: string | null
      reasoning?: string | null
      tool_calls?: ChunkToolCall[]
    }
    finish_reason?: string | null
  }[]
  usage?: {
    prompt_tokens?: number
    completion_tokens?: number
    prompt_tokens_details?: { cached_tokens?: number }
    /** DeepSeek reports cache hits here instead of under `prompt_tokens_details`. */
    prompt_cache_hit_tokens?: number
    completion_tokens_details?: { reasoning_tokens?: number }
  } | null
  error?: { message?: string }
}

function finishReason(reason: string | null): 'stop' | 'length' | 'tool-calls' {
  if (reason === 'tool_calls' || reason === 'function_call') return 'tool-calls'
  if (reason === 'length') return 'length'
  return 'stop'
}

/** The body of a chat completion request; the key goes in a header. */
export function requestBody(_settings: OpenAICompatibleSettings, request: ModelRequest) {
  return withExtraBody(baseBody(request), request.extraBody)
}

function baseBody(request: ModelRequest) {
  return {
    model: request.model,
    messages: toWire(request.messages),
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
    ...(request.effort ? { reasoning_effort: request.effort } : {}),
    ...(request.maxTokens ? { max_tokens: request.maxTokens } : {}),
    stream: true,
    stream_options: { include_usage: true },
  }
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
  requestBody,
  async *stream(settings, request): AsyncGenerator<ModelStreamPart> {
    let response: Response
    try {
      response = await fetch(`${settings.baseUrl.replace(/\/+$/, '')}/chat/completions`, {
        method: 'POST',
        headers: {
          ...request.headers,
          'content-type': 'application/json',
          ...(settings.apiKey ? { authorization: `Bearer ${settings.apiKey}` } : {}),
        },
        body: JSON.stringify(requestBody(settings, request)),
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
  let reasoning = ''
  let reasoningFrom = 0
  let done = false
  try {
    for await (const { data, comment } of readServerSentEvents(body)) {
      // Anything at all means the service is still at it: thinking can be long and silent.
      yield { type: 'alive' }
      if (comment) continue
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
        const cached =
          chunk.usage.prompt_tokens_details?.cached_tokens ?? chunk.usage.prompt_cache_hit_tokens
        const thought = chunk.usage.completion_tokens_details?.reasoning_tokens
        yield {
          type: 'usage',
          input: chunk.usage.prompt_tokens ?? 0,
          output: chunk.usage.completion_tokens ?? 0,
          ...(cached ? { cached } : {}),
          ...(thought ? { reasoning: thought } : {}),
        }
      }
      const choice = chunk.choices?.[0]
      if (!choice) continue
      const delta = choice.delta
      const thought = delta?.reasoning_content ?? delta?.reasoning ?? ''
      if (thought && !reasoning) reasoningFrom = Date.now()
      reasoning += thought
      // Reasoning ends where the answer starts.
      if (reasoning && (delta?.content || delta?.tool_calls?.length)) {
        yield { type: 'reasoning', text: reasoning, startedAt: reasoningFrom }
        reasoning = ''
      }
      if (delta?.content) yield { type: 'text', text: delta.content }
      for (const piece of delta?.tool_calls ?? []) {
        if (!calls.has(piece.index)) yield { type: 'calling' }
        const call = calls.get(piece.index) ?? { id: '', name: '', arguments: '' }
        if (piece.id) call.id = piece.id
        if (piece.function?.name) call.name += piece.function.name
        if (piece.function?.arguments) call.arguments += piece.function.arguments
        calls.set(piece.index, call)
        if (piece.function?.arguments && call.name) {
          yield {
            type: 'tool-input',
            call: [...calls.keys()].indexOf(piece.index),
            name: call.name,
            arguments: call.arguments,
          }
        }
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
  if (reasoning) yield { type: 'reasoning', text: reasoning, startedAt: reasoningFrom }
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
