import type { ChatMessage, ModelProvider, ModelRequest, ModelStreamPart } from '@ariadne/plugin-sdk'
import type { ResolvedModel } from './runner'

/** A scripted model for tests: `script` sees each request and the role it was made for. */
export type Script = (
  request: ModelRequest,
  role: string,
) => ModelStreamPart[] | Promise<ModelStreamPart[]>

export function fakeModel(script: Script): ResolvedModel & { requests: ModelRequest[] } {
  const requests: ModelRequest[] = []
  const provider: ModelProvider<never> = {
    kind: 'fake',
    title: { 'zh-CN': '假模型' },
    settings: { type: 'object', properties: {} },
    async *stream(_settings, request) {
      requests.push(request)
      const system = request.messages[0]?.content ?? ''
      const role = system.includes('You are the coordinator') ? 'coordinator' : 'thread'
      for (const part of await script(request, role)) yield part
    },
  }
  return { provider, settings: {}, model: 'fake-1', label: 'fake / fake-1', requests }
}

export const call = (id: string, name: string, args: unknown): ModelStreamPart => ({
  type: 'tool-call',
  id,
  name,
  arguments: JSON.stringify(args),
})
export const say = (text: string): ModelStreamPart[] => [
  { type: 'text', text },
  { type: 'usage', input: 10, output: 5 },
  { type: 'finish', reason: 'stop' },
]
export const lastUser = (request: ModelRequest) =>
  request.messages.filter((m: ChatMessage) => m.role === 'user').at(-1)?.content ?? ''
export const answered = (request: ModelRequest) => request.messages.at(-1)?.role === 'tool'
