import type { Author, Reaction } from '@ariadne/core'
import type { ChatMessageReaction } from 'dou-ui'
import { list, t } from '../i18n'

/** What the add button offers: a few everyday answers, no full picker. */
export const commonEmojis = ['👍', '❤️', '🎉', '😄', '👀', '🙏']

/** The emojis on a message, one item each, with who put it there; the user's own marked. */
export function shownReactions(reactions: Reaction[]): ChatMessageReaction[] {
  const by = new Map<string, Author[]>()
  for (const reaction of reactions) {
    by.set(reaction.emoji, [...(by.get(reaction.emoji) ?? []), reaction.by])
  }
  return [...by].map(([emoji, authors]) => {
    // The user first, as chat apps read it out: "you and the coordinator".
    const ordered = authors.toSorted((a, b) => Number(b === 'user') - Number(a === 'user'))
    return {
      emoji,
      count: authors.length,
      reacted: authors.includes('user'),
      label: list(ordered.map((author) => t(`message.quote.${author}`))),
    }
  })
}
