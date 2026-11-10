import type { ChatMessage, ModelProvider, ModelRequest, ModelStreamPart } from '@ariadne/plugin-sdk'
import type { ResolvedModel } from './runner'

/** A scripted model for tests: `script` sees each request and the role it was made for. */
export type Script = (
  request: ModelRequest,
  role: string,
) => ModelStreamPart[] | Promise<ModelStreamPart[]>

export function fakeModel(
  script: Script,
): ResolvedModel & { requests: ModelRequest[]; sent: ModelRequest[] } {
  const requests: ModelRequest[] = []
  /** Every request, those that ended a turn after `say` too. */
  const sent: ModelRequest[] = []
  const provider: ModelProvider<never> = {
    kind: 'fake',
    title: { 'zh-CN': '假模型' },
    settings: { type: 'object', properties: {} },
    async *stream(_settings, request) {
      sent.push(request)
      // What `say` sent is the turn's last step: it ends without a script, and is not one of
      // the `requests` the script answered.
      const last = request.messages.at(-1)
      const step = request.messages.findLast((m: ChatMessage) => m.role === 'assistant')
      if (
        last?.role === 'tool' &&
        step?.role === 'assistant' &&
        step.toolCalls?.length &&
        step.toolCalls.every((c) => c.id.startsWith(SAY))
      ) {
        yield { type: 'finish', reason: 'stop' }
        return
      }
      requests.push(request)
      const system = request.messages[0]?.content ?? ''
      const role = system.includes('You are the coordinator') ? 'coordinator' : 'thread'
      for (const part of await script(request, role)) yield part
    },
  }
  return { provider, settings: {}, model: 'fake-1', label: 'fake / fake-1', requests, sent }
}

export const call = (id: string, name: string, args: unknown): ModelStreamPart => ({
  type: 'tool-call',
  id,
  name,
  arguments: JSON.stringify(args),
})
/** Plain text as the answer, for calls whose text is what they return (summaries, research). */
export const plain = (body: string): ModelStreamPart[] => [
  { type: 'text', text: body },
  { type: 'usage', input: 10, output: 5 },
  { type: 'finish', reason: 'stop' },
]
const SAY = 'say_'
let said = 0
/** Send `text` to the user and end the turn; nothing at all for empty text. */
export const say = (text: string): ModelStreamPart[] =>
  text.trim()
    ? [
        call(`${SAY}${++said}`, 'send_message', { text }),
        { type: 'usage', input: 10, output: 5 },
        { type: 'finish', reason: 'tool-calls' },
      ]
    : [
        { type: 'usage', input: 10, output: 5 },
        { type: 'finish', reason: 'stop' },
      ]
/** The last user input, without the message id the coordinator reads after it. */
export const lastUser = (request: ModelRequest) =>
  (request.messages.filter((m: ChatMessage) => m.role === 'user').at(-1)?.content ?? '').replace(
    /\n\n\[message msg_[\w-]+\]$/,
    '',
  )
export const answered = (request: ModelRequest) => request.messages.at(-1)?.role === 'tool'
/** Told to report after a turn that ended without one: scripts that act on new input skip it. */
export const reminded = (request: ModelRequest) => lastUser(request).includes('<report_missing>')
