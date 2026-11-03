import {
  definePlugin,
  describeHttpError,
  readServerSentEvents,
  type ChatMessage,
  type ModelProvider,
  type ModelStreamPart,
} from '@ariadne/plugin-sdk'

export interface AnthropicSettings {
  baseUrl: string
  apiKey: string
  maxTokens: number
}

const API_VERSION = '2023-06-01'

type Block =
  | { type: 'text'; text: string }
  | { type: 'tool_use'; id: string; name: string; input: unknown }
  | { type: 'tool_result'; tool_use_id: string; content: string; is_error?: boolean }

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

/**
 * The protocol keeps system text apart, wants tool results inside a user message, and
 * expects roles to alternate, so neighbouring messages of one role are merged.
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
        push('user', message.content ? [{ type: 'text', text: message.content }] : [])
        break
      case 'assistant':
        push('assistant', [
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
            content: message.content,
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
  message?: { usage?: { input_tokens?: number; output_tokens?: number } }
  content_block?: { type: string; id?: string; name?: string }
  delta?: { type?: string; text?: string; partial_json?: string; stop_reason?: string | null }
  usage?: { output_tokens?: number }
  error?: { message?: string }
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
      maxTokens: {
        type: 'number',
        title: { 'zh-CN': '单次回复的最大 token 数', en: 'Max tokens per response' },
        default: 8192,
      },
    },
    required: ['baseUrl', 'apiKey'],
  },
  async *stream(settings, request): AsyncGenerator<ModelStreamPart> {
    const { system, messages } = toWire(request.messages)
    const response = await fetch(`${settings.baseUrl.replace(/\/+$/, '')}/v1/messages`, {
      method: 'POST',
      headers: {
        'content-type': 'application/json',
        'anthropic-version': API_VERSION,
        'x-api-key': settings.apiKey,
      },
      body: JSON.stringify({
        model: request.model,
        max_tokens: settings.maxTokens || 8192,
        ...(system ? { system } : {}),
        messages,
        ...(request.tools.length
          ? {
              tools: request.tools.map((tool) => ({
                name: tool.name,
                description: tool.description,
                input_schema: tool.parameters,
              })),
            }
          : {}),
        stream: true,
      }),
      ...(request.signal ? { signal: request.signal } : {}),
    })
    if (!response.ok || !response.body) {
      yield { type: 'finish', reason: 'error', error: await describeHttpError(response) }
      return
    }

    const blocks = new Map<number, { id: string; name: string; json: string }>()
    let input = 0
    let output = 0
    let stop: string | null = null
    for await (const { data } of readServerSentEvents(response.body)) {
      let event: StreamEvent
      try {
        event = JSON.parse(data)
      } catch {
        continue
      }
      switch (event.type) {
        case 'message_start':
          input = event.message?.usage?.input_tokens ?? 0
          output = event.message?.usage?.output_tokens ?? 0
          break
        case 'content_block_start':
          if (event.content_block?.type === 'tool_use' && event.index !== undefined) {
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
          } else if (event.delta?.type === 'input_json_delta' && event.index !== undefined) {
            const block = blocks.get(event.index)
            if (block) block.json += event.delta.partial_json ?? ''
          }
          break
        case 'content_block_stop': {
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
        case 'error':
          yield { type: 'finish', reason: 'error', error: event.error?.message ?? data }
          return
      }
    }
    yield { type: 'usage', input, output }
    yield {
      type: 'finish',
      reason: stop === 'tool_use' ? 'tool-calls' : stop === 'max_tokens' ? 'length' : 'stop',
    }
  },
}

export default definePlugin({
  id: 'anthropic',
  name: { 'zh-CN': 'Anthropic 模型', en: 'Anthropic models' },
  setup(ctx) {
    return ctx.registerModelProvider(anthropicProvider as ModelProvider<never>)
  },
})
