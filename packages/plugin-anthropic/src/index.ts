import {
  definePlugin,
  EFFORTS,
  type ListedModel,
  httpErrorPart,
  incompletePart,
  readServerSentEvents,
  transportErrorPart,
  type ChatMessage,
  type ImagePart,
  type ModelProvider,
  type ModelRequest,
  type ModelSearch,
  type ModelSearchRequest,
  onHost,
  searchAsk,
  searchHttpError,
  refusalText,
  type SearchResult,
  type ModelStreamPart,
  type ReasoningBlock,
  withExtraBody,
} from '@ariadne/plugin-sdk'

/** A model as the service lists it. */
interface ServiceModel {
  id?: unknown
  display_name?: unknown
  capabilities?: {
    effort?: { supported?: unknown } & Record<string, { supported?: unknown } | unknown>
  }
}

/** A listed model with its name and, when the service says, the efforts it takes. */
export function listed(model: ServiceModel): ListedModel {
  const id = String(model.id)
  const title = typeof model.display_name === 'string' ? model.display_name.trim() : ''
  const effort = model.capabilities?.effort
  const levels =
    effort && typeof effort === 'object'
      ? EFFORTS.filter(
          (level) => (effort[level] as { supported?: unknown } | undefined)?.supported === true,
        )
      : []
  // Levels it names; none at all when it says effort cannot be set; else nothing is known.
  const efforts = levels.length ? levels : effort?.supported === false ? [] : undefined
  return { id, ...(title && title !== id ? { title } : {}), ...(efforts ? { efforts } : {}) }
}

export interface AnthropicSettings {
  baseUrl: string
  apiKey: string
}

const API_VERSION = '2023-06-01'

type ImageBlock = {
  type: 'image'
  source: { type: 'base64'; media_type: string; data: string }
}

/** A cache point: the service keeps the prompt up to the block it is on for a few minutes. */
const EPHEMERAL = { type: 'ephemeral' } as const

type Block = (
  | { type: 'thinking'; thinking: string; signature: string }
  | { type: 'redacted_thinking'; data: string }
  | { type: 'text'; text: string }
  | ImageBlock
  | { type: 'tool_use'; id: string; name: string; input: unknown }
  | {
      type: 'tool_result'
      tool_use_id: string
      content: string | ({ type: 'text'; text: string } | ImageBlock)[]
      is_error?: boolean
    }
) & { cache_control?: typeof EPHEMERAL }

const imageBlock = (image: ImagePart): ImageBlock => ({
  type: 'image',
  source: { type: 'base64', media_type: image.mime, data: image.data },
})

interface WireMessage {
  role: 'user' | 'assistant'
  content: Block[]
}

function parseInput(text: string): unknown {
  try {
    const value = JSON.parse(text || '{}')
    return value && typeof value === 'object' && !Array.isArray(value) ? value : {}
  } catch {
    return {}
  }
}

/** Thinking blocks as the protocol takes them back; ones it did not sign are left out. */
function thinkingBlocks(reasoning: ReasoningBlock[] | undefined): Block[] {
  return (reasoning ?? []).flatMap((block): Block[] => {
    if (block.redacted) return [{ type: 'redacted_thinking', data: block.text }]
    if (!block.signature) return []
    return [{ type: 'thinking', thinking: block.text, signature: block.signature }]
  })
}

/**
 * The protocol keeps system text apart, wants tool results inside a user message, and
 * expects roles to alternate, so neighbouring messages of one role are merged. Signed
 * thinking always goes back as it came: newer models think without being asked and want it
 * back in a turn's tool calls, and the API drops what the model in use cannot read.
 */
export function toWire(messages: ChatMessage[]): { system: string; messages: WireMessage[] } {
  const system: string[] = []
  const wire: WireMessage[] = []
  const push = (role: WireMessage['role'], blocks: Block[]) => {
    if (!blocks.length) return
    const last = wire.at(-1)
    if (last?.role === role) last.content.push(...blocks)
    else wire.push({ role, content: blocks })
  }
  for (const message of messages) {
    switch (message.role) {
      case 'system':
        system.push(message.content)
        break
      case 'user':
        push('user', [
          ...(message.content ? [{ type: 'text' as const, text: message.content }] : []),
          ...(message.images ?? []).map(imageBlock),
        ])
        break
      case 'assistant':
        push('assistant', [
          ...thinkingBlocks(message.reasoning),
          ...(message.content ? [{ type: 'text' as const, text: message.content }] : []),
          ...(message.toolCalls ?? []).map((call) => ({
            type: 'tool_use' as const,
            id: call.id,
            name: call.name,
            input: parseInput(call.arguments),
          })),
        ])
        break
      case 'tool':
        push('user', [
          {
            type: 'tool_result',
            tool_use_id: message.toolCallId,
            content: message.images?.length
              ? [
                  ...(message.content ? [{ type: 'text' as const, text: message.content }] : []),
                  ...message.images.map(imageBlock),
                ]
              : message.content,
            ...(message.isError ? { is_error: true } : {}),
          },
        ])
        break
    }
  }
  return { system: system.join('\n\n'), messages: wire }
}

interface StreamEvent {
  type: string
  index?: number
  message?: {
    usage?: {
      input_tokens?: number
      output_tokens?: number
      cache_creation_input_tokens?: number
      cache_read_input_tokens?: number
    }
  }
  content_block?: { type: string; id?: string; name?: string; data?: string }
  delta?: {
    type?: string
    text?: string
    thinking?: string
    signature?: string
    partial_json?: string
    stop_reason?: string | null
  }
  usage?: { output_tokens?: number }
  error?: { type?: string; message?: string }
}

/** Put a cache point on the last block of `message` that takes one (thinking takes none). */
function markLast(message: WireMessage | undefined) {
  if (!message) return
  for (let index = message.content.length - 1; index >= 0; index--) {
    const block = message.content[index]!
    if (block.type === 'thinking' || block.type === 'redacted_thinking') continue
    message.content[index] = { ...block, cache_control: EPHEMERAL }
    return
  }
}

/**
 * Cache points, at most four, each where a part that changes less often ends: the tools,
 * the system text (memory and the plan move it now and then), the end of the request
 * before this one, and the end of this one. A turn only ever appends to the conversation,
 * so the next request reads all of this one from the cache and pays in full only for what
 * came since. After a summary the conversation starts anew and is cached again from there.
 * The point on the previous request's end is a sure read, however much a step appended.
 */
function withCachePoints(messages: WireMessage[]) {
  markLast(messages.at(-1))
  const before = messages.findLastIndex(
    (message, index) => message.role === 'user' && index < messages.length - 1,
  )
  if (before >= 0) markLast(messages[before])
}

/** The body of a Messages request; credentials go in headers. */
export function requestBody(_settings: AnthropicSettings, request: ModelRequest) {
  const { system, messages } = toWire(request.messages)
  const cache = request.cache !== false
  if (cache) withCachePoints(messages)
  const tools = request.tools.map((tool, index) => ({
    name: tool.name,
    description: tool.description,
    input_schema: tool.parameters,
    ...(cache && index === request.tools.length - 1 ? { cache_control: EPHEMERAL } : {}),
  }))
  return withExtraBody(
    {
      model: request.model,
      max_tokens: request.maxTokens || DEFAULT_MAX_TOKENS,
      // Adaptive thinking, its depth set by effort; a summary of it is shown in the trace.
      // Models before the 4.6 generation only take a fixed budget and refuse this.
      ...(request.effort
        ? {
            thinking: { type: 'adaptive', display: 'summarized' },
            output_config: { effort: request.effort },
          }
        : {}),
      ...(system
        ? {
            system: cache ? [{ type: 'text', text: system, cache_control: EPHEMERAL }] : system,
          }
        : {}),
      messages,
      ...(tools.length ? { tools } : {}),
      stream: true,
    },
    request.extraBody,
  )
}

/** What a response may hold when the model's settings do not say: the protocol needs a number. */
export const DEFAULT_MAX_TOKENS = 8192

/** Searches one answer makes at most: the query, and once more when it comes back thin. */
const SEARCH_USES = 2
/** Room for the answer, and for the thinking newer models do unasked. */
const SEARCH_MAX_TOKENS = 4096

/** The body of a search request: one question, answered with the service's own search. */
export function searchBody(request: Pick<ModelSearchRequest, 'model' | 'query'>) {
  return {
    model: request.model,
    max_tokens: SEARCH_MAX_TOKENS,
    tools: [{ type: 'web_search_20250305', name: 'web_search', max_uses: SEARCH_USES }],
    messages: [{ role: 'user', content: searchAsk(request.query) }],
  }
}

interface SearchResponse {
  stop_reason?: string | null
  content?: {
    type: string
    text?: string
    citations?: { type?: string; url?: string; title?: string | null; cited_text?: string }[]
    /** A list of pages on success, one error object on failure. */
    content?: { type?: string; url?: string; title?: string | null }[] | { error_code?: string }
  }[]
  usage?: {
    input_tokens?: number
    output_tokens?: number
    cache_creation_input_tokens?: number
    cache_read_input_tokens?: number
  }
}

/**
 * What a search response says and the pages behind it: those the answer quotes first, with
 * the passage quoted, then the others the search returned.
 */
export function readSearch(response: SearchResponse, limit: number) {
  const sources = new Map<string, SearchResult>()
  const found: SearchResult[] = []
  let text = ''
  let failed: string | null = null
  for (const block of response.content ?? []) {
    if (block.type === 'text') {
      text += block.text ?? ''
      for (const cite of block.citations ?? []) {
        if (!cite.url) continue
        const known = sources.get(cite.url)
        const quoted = (cite.cited_text ?? '').trim()
        if (!known)
          sources.set(cite.url, { title: cite.title ?? '', url: cite.url, snippet: quoted })
        else if (quoted && !known.snippet.includes(quoted)) {
          known.snippet = known.snippet ? `${known.snippet} … ${quoted}` : quoted
        }
      }
    } else if (block.type === 'web_search_tool_result') {
      if (Array.isArray(block.content)) {
        for (const page of block.content) {
          if (page.url) found.push({ title: page.title ?? '', url: page.url, snippet: '' })
        }
      } else failed = block.content?.error_code ?? 'unavailable'
    }
  }
  for (const page of found) if (!sources.has(page.url)) sources.set(page.url, page)
  if (!sources.size && failed) throw new Error(`web search failed: ${failed}`)
  if (response.stop_reason === 'refusal') throw new Error('the model declined to search')
  // Cut off or paused before it wrote anything: pages alone are not an answer.
  if (
    !text.trim() &&
    (response.stop_reason === 'max_tokens' || response.stop_reason === 'pause_turn')
  ) {
    throw new Error(`the search ended early: ${response.stop_reason}`)
  }
  const usage = response.usage
  return {
    text: text.trim(),
    sources: [...sources.values()].slice(0, limit),
    ...(usage
      ? {
          usage: {
            input:
              (usage.input_tokens ?? 0) +
              (usage.cache_creation_input_tokens ?? 0) +
              (usage.cache_read_input_tokens ?? 0),
            output: usage.output_tokens ?? 0,
            ...(usage.cache_read_input_tokens ? { cached: usage.cache_read_input_tokens } : {}),
            ...(usage.cache_creation_input_tokens
              ? { cacheWrite: usage.cache_creation_input_tokens }
              : {}),
          },
        }
      : {}),
  }
}

const search: ModelSearch<AnthropicSettings>['run'] = async (settings, request) => {
  const response = await fetch(`${settings.baseUrl.replace(/\/+$/, '')}/v1/messages`, {
    method: 'POST',
    headers: {
      ...request.headers,
      'content-type': 'application/json',
      'anthropic-version': API_VERSION,
      'x-api-key': settings.apiKey,
    },
    body: JSON.stringify(searchBody(request)),
    ...(request.signal ? { signal: request.signal } : {}),
  })
  if (!response.ok) throw searchHttpError(response.status)
  return readSearch((await response.json()) as SearchResponse, request.limit)
}

export const anthropicProvider: ModelProvider<AnthropicSettings> = {
  kind: 'anthropic',
  title: { 'zh-CN': 'Anthropic 接口', en: 'Anthropic API' },
  settings: {
    type: 'object',
    properties: {
      baseUrl: {
        type: 'string',
        title: { 'zh-CN': '接口地址', en: 'Base URL' },
        default: 'https://api.anthropic.com',
      },
      apiKey: { type: 'string', title: { 'zh-CN': 'API Key', en: 'API key' }, secret: true },
    },
    required: ['baseUrl', 'apiKey'],
  },
  requestBody,
  search: { available: (settings) => onHost(settings.baseUrl, 'api.anthropic.com'), run: search },
  async models(settings, options = {}) {
    const response = await fetch(`${settings.baseUrl.replace(/\/+$/, '')}/v1/models?limit=1000`, {
      headers: {
        ...options.headers,
        'anthropic-version': API_VERSION,
        'x-api-key': settings.apiKey,
      },
      ...(options.signal ? { signal: options.signal } : {}),
    })
    if (!response.ok) throw new Error(await refusalText(response))
    const body = (await response.json()) as { data?: ServiceModel[] }
    return (body.data ?? []).flatMap((model) =>
      typeof model.id === 'string' ? [listed(model)] : [],
    )
  },
  find({ env }) {
    const apiKey = env.ANTHROPIC_API_KEY?.trim()
    if (!apiKey) return []
    return [
      {
        id: 'ANTHROPIC_API_KEY',
        from: { env: 'ANTHROPIC_API_KEY' },
        settings: {
          baseUrl: env.ANTHROPIC_BASE_URL?.trim() || 'https://api.anthropic.com',
          apiKey,
        },
      },
    ]
  },
  async *stream(settings, request): AsyncGenerator<ModelStreamPart> {
    let response: Response
    try {
      response = await fetch(`${settings.baseUrl.replace(/\/+$/, '')}/v1/messages`, {
        method: 'POST',
        headers: {
          ...request.headers,
          'content-type': 'application/json',
          'anthropic-version': API_VERSION,
          'x-api-key': settings.apiKey,
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

/** Server errors the service itself says are passing: overloaded, rate limited, internal. */
const PASSING_ERRORS = new Set(['overloaded_error', 'rate_limit_error', 'api_error'])

async function* readStream(
  body: ReadableStream<Uint8Array>,
  signal: AbortSignal | undefined,
): AsyncGenerator<ModelStreamPart> {
  const blocks = new Map<number, { id: string; name: string; json: string }>()
  const thoughts = new Map<number, ReasoningBlock & { startedAt: number }>()
  let input = 0
  let cached = 0
  let cacheWrite = 0
  let output = 0
  let stop: string | null = null
  let done = false
  try {
    for await (const { data, comment } of readServerSentEvents(body)) {
      // Anything at all means the service is still at it: thinking can be long and silent.
      yield { type: 'alive' }
      if (comment) continue
      let event: StreamEvent
      try {
        event = JSON.parse(data)
      } catch {
        continue
      }
      switch (event.type) {
        case 'message_start': {
          // Cached input is counted apart; all of it is what the model read.
          const usage = event.message?.usage
          input =
            (usage?.input_tokens ?? 0) +
            (usage?.cache_creation_input_tokens ?? 0) +
            (usage?.cache_read_input_tokens ?? 0)
          cached = usage?.cache_read_input_tokens ?? 0
          cacheWrite = usage?.cache_creation_input_tokens ?? 0
          output = usage?.output_tokens ?? 0
          break
        }
        case 'content_block_start':
          if (event.index === undefined) break
          if (event.content_block?.type === 'thinking') {
            thoughts.set(event.index, { text: '', signature: '', startedAt: Date.now() })
          } else if (event.content_block?.type === 'redacted_thinking') {
            thoughts.set(event.index, {
              text: event.content_block.data ?? '',
              redacted: true,
              startedAt: Date.now(),
            })
          } else if (event.content_block?.type === 'tool_use') {
            blocks.set(event.index, {
              id: event.content_block.id ?? `toolu_${event.index}`,
              name: event.content_block.name ?? '',
              json: '',
            })
          }
          break
        case 'content_block_delta':
          if (event.delta?.type === 'text_delta' && event.delta.text) {
            yield { type: 'text', text: event.delta.text }
          } else if (event.delta?.type === 'thinking_delta' && event.index !== undefined) {
            const thought = thoughts.get(event.index)
            if (thought) thought.text += event.delta.thinking ?? ''
          } else if (event.delta?.type === 'signature_delta' && event.index !== undefined) {
            const thought = thoughts.get(event.index)
            if (thought)
              thought.signature = (thought.signature ?? '') + (event.delta.signature ?? '')
          } else if (event.delta?.type === 'input_json_delta' && event.index !== undefined) {
            const block = blocks.get(event.index)
            if (block) {
              block.json += event.delta.partial_json ?? ''
              yield {
                type: 'tool-input',
                call: [...blocks.keys()].indexOf(event.index),
                name: block.name,
                arguments: block.json,
              }
            }
          }
          break
        case 'content_block_stop': {
          const thought = event.index === undefined ? undefined : thoughts.get(event.index)
          if (thought) {
            yield {
              type: 'reasoning',
              text: thought.text,
              startedAt: thought.startedAt,
              ...(thought.signature ? { signature: thought.signature } : {}),
              ...(thought.redacted ? { redacted: true } : {}),
            }
          }
          const block = event.index === undefined ? undefined : blocks.get(event.index)
          if (block) {
            yield {
              type: 'tool-call',
              id: block.id,
              name: block.name,
              arguments: block.json || '{}',
            }
          }
          break
        }
        case 'message_delta':
          stop = event.delta?.stop_reason ?? stop
          output = event.usage?.output_tokens ?? output
          break
        case 'message_stop':
          done = true
          break
        case 'error':
          yield {
            type: 'finish',
            reason: 'error',
            error: event.error?.message ?? data,
            ...(PASSING_ERRORS.has(event.error?.type ?? '') ? { retryable: true, busy: true } : {}),
          }
          return
      }
    }
  } catch (error) {
    const part = transportErrorPart(error, signal, true)
    if (part) yield part
    return
  }
  if (!done) {
    if (!signal?.aborted) yield incompletePart()
    return
  }
  yield {
    type: 'usage',
    input,
    output,
    ...(cached ? { cached } : {}),
    ...(cacheWrite ? { cacheWrite } : {}),
  }
  yield {
    type: 'finish',
    reason: stop === 'tool_use' ? 'tool-calls' : stop === 'max_tokens' ? 'length' : 'stop',
  }
}

export default definePlugin({
  id: 'anthropic',
  name: { 'zh-CN': 'Anthropic 模型', en: 'Anthropic models' },
  setup(ctx) {
    return ctx.registerModelProvider(anthropicProvider as ModelProvider<never>)
  },
})
