import type { ChatMessage, ImagePart } from '@ariadne/plugin-sdk'
import { createHash } from 'node:crypto'

/** What a turn of the agent starts with: words, and pictures after them. */
export interface AgentInput {
  text: string
  images: ImagePart[]
}

const hash = (text: string) => createHash('sha256').update(text).digest('hex').slice(0, 16)

/**
 * A message as far as a session needs to know it is the same one again. Only what stays
 * put is compared: a user message by its beginning (pictures that age out leave words at
 * its end), a reply by the calls it made, a result by its call, whatever its content now
 * (old results are cleared to make room). The system prompt is compared on its own.
 */
export function fingerprint(message: ChatMessage): string {
  switch (message.role) {
    case 'system':
      return 's'
    case 'user':
      return `u:${hash(message.content.slice(0, 400))}`
    case 'assistant':
      return `a:${(message.toolCalls ?? []).map((call) => call.id).join(',')}`
    case 'tool':
      return `t:${message.toolCallId}`
  }
}

export const systemHash = (system: string) => hash(system)

/** The system prompt changed under a running session: the agent hears the new one. */
export function updatedInstructions(system: string): string {
  return `<updated_instructions>\nYour instructions have changed. From now on they are:\n\n${system}\n</updated_instructions>`
}

/**
 * Everything so far, for an agent that has not seen any of it: a new session after a
 * restart, a summary of older history, or a request that does not go on from the last.
 * A conversation that is only its first message is passed as it is.
 */
export function renderHistory(messages: ChatMessage[]): AgentInput {
  const [first] = messages
  if (messages.length === 1 && first?.role === 'user') {
    return { text: first.content, images: first.images ?? [] }
  }
  const images: ImagePart[] = []
  const names = new Map<string, string>()
  const parts: string[] = []
  for (const message of messages) {
    switch (message.role) {
      case 'system':
        break
      case 'user':
        images.push(...(message.images ?? []))
        parts.push(`<user>\n${message.content}\n</user>`)
        break
      case 'assistant': {
        const calls = (message.toolCalls ?? []).map((call) => {
          names.set(call.id, call.name)
          return `<call id="${call.id}" name="${call.name}">${call.arguments}</call>`
        })
        parts.push(
          `<assistant>\n${[message.content, ...calls].filter(Boolean).join('\n')}\n</assistant>`,
        )
        break
      }
      case 'tool':
        images.push(...(message.images ?? []))
        parts.push(
          `<result id="${message.toolCallId}" name="${names.get(message.toolCallId) ?? ''}"${message.isError ? ' error="true"' : ''}>\n${message.content}\n</result>`,
        )
        break
    }
  }
  const last = messages.at(-1)
  const ending =
    last?.role === 'user'
      ? 'Answer the last user message.'
      : 'Go on from where it ends; the calls above have already run.'
  return {
    text: `<history>\nThe conversation so far, from before this session started. Your earlier replies and tool calls are shown as they were; call the tools again yourself when you need them.\n\n${parts.join('\n\n')}\n</history>\n\n${ending}`,
    images,
  }
}

/** Messages that came in while the agent worked, said in one go. */
export function joinInput(messages: ChatMessage[], prefix: string | null): AgentInput {
  const texts: string[] = prefix ? [prefix] : []
  const images: ImagePart[] = []
  for (const message of messages) {
    if (message.role !== 'user') continue
    texts.push(message.content)
    images.push(...(message.images ?? []))
  }
  return { text: texts.join('\n\n'), images }
}
