import type { TranscriptEntry } from '@ariadne/core'
import { fenced } from '../prompts'
import {
  asOutside,
  currentSections,
  foldedUserWords,
  inModelOrder,
  keptUserTokens,
  removedTools,
  shownUserWords,
  summaryMessages,
  textTokens,
  visibleHistory,
} from '../transcript'

const input = (await Bun.stdin.json()) as {
  entries: TranscriptEntry[]
  texts: string[]
  budgets: number[]
  thresholds: number[]
  blocks: { name: string; body: string; attributes: string }[]
}
console.info(
  JSON.stringify({
    ordered: inModelOrder(input.entries),
    visible: visibleHistory(input.entries),
    sections: currentSections(input.entries),
    removed: [...removedTools(input.entries)].toSorted(),
    tokens: input.texts.map(textTokens),
    outside: input.texts.map(asOutside),
    budgets: input.thresholds.map(keptUserTokens),
    words: input.budgets.map((budget) => {
      const words = foldedUserWords(input.entries, 20, budget)
      return { words, shown: shownUserWords(words) }
    }),
    blocks: input.blocks.map(({ name, body, attributes }) => fenced(name, body, attributes)),
    summary: summaryMessages('earlier notes', 'open questions', [{ text: 'thinking' }], ['words']),
  }),
)
