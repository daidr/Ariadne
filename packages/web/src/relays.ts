import type { MessageView } from '@ariadne/core'

/**
 * How a message reads in a thread's panel. A relay that passes the user's own messages along
 * shows just those, as the user's bubbles: the coordinator's note to the thread is for the
 * model, and the user only needs to see their request arrived. The first bubble keeps the
 * relay's id, so a link to the relay lands on it; one the user wrote in this very thread is
 * already there above. Any other message reads as it is.
 */
export function shownAs(message: MessageView): MessageView[] {
  if (message.kind !== 'relay' || !message.cites.length) return [message]
  return message.cites
    .filter((cite) => cite.conversationId !== message.conversationId)
    .map((cite, index) => ({
      id: index ? `${message.id}:${index}` : message.id,
      conversationId: message.conversationId,
      author: 'user',
      kind: 'chat',
      content: cite.content,
      blocks: [],
      ask: null,
      attachments: cite.attachments ?? [],
      mentions: cite.mentions ?? [],
      comments: [],
      cites: [],
      relayedTo: [],
      createdAt: message.createdAt,
      editedAt: null,
      editReason: null,
      originalContent: null,
      editedSinceRead: false,
    }))
}

/** The relay a bubble of `shownAs` came from: the id a link to it carries. */
export const relayIdOf = (id: string) => id.split(':')[0]!
