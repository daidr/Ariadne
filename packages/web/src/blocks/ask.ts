import type { AskBlock } from '@ariadne/core'

/** What an ask block asks, in one line: its question or its title. */
export const askTitle = (block: AskBlock): string =>
  block.type === 'choice' ? block.question : block.title
