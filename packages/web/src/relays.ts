import type { MessageView } from '@ariadne/core'

/**
 * How a message reads in a thread's panel. A relay that passes the user's own messages along
 * shows just those, as the user's bubbles: the coordinator's note to the thread is for the
 * model, and the user only needs to see their request arrived. The first bubble keeps the
 * relay's id, so a link to the relay lands on it; one the user wrote in this very thread is
 * already there above, and a relay with only such ones reads as the coordinator's message.
 * Any other message reads as it is.
 */
export function shownAs(message: MessageView): MessageView[] {
  const cites = message.cites.filter((cite) => cite.conversationId !== message.conversationId)
  // With none from elsewhere, it is the coordinator's message like any other.
  if (message.kind !== 'relay' || !cites.length) return [message]
  return cites.map((cite, index) => ({
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
    replyTo: null,
    // A reaction to the relay is to what it passed along: it goes on the first bubble.
    reactions: index ? [] : message.reactions,
    createdAt: message.createdAt,
    editedAt: null,
    editReason: null,
    originalContent: null,
    editedSinceRead: false,
  }))
}

/** The relay a bubble of `shownAs` came from: the id a link to it carries. */
export const relayIdOf = (id: string) => id.split(':')[0]!
