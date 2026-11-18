import type { TranscriptEntry } from '@ariadne/core'
import { compactAt, compactionCut, estimateTokens, summaryRequest } from '../compaction'
import type { ModelMessage } from '../transcript'

const input = (await Bun.stdin.json()) as {
  entries: TranscriptEntry[]
  messages: ModelMessage[]
  budgets: number[]
  cuts: number[]
}
console.info(
  JSON.stringify({
    thresholds: [compactAt(), compactAt(200000), compactAt(1000000, 0.9)],
    estimate: estimateTokens(input.messages),
    cuts: input.budgets.map((budget) => compactionCut(input.entries, budget)),
    summaries: input.cuts.map((cut) =>
      summaryRequest(
        input.entries,
        cut,
        (id) => `title:${id}`,
        'coordinator',
        undefined,
        undefined,
        20,
      ),
    ),
  }),
)
