import type { TranscriptEntry } from '@ariadne/core'
import type { ChatMessage } from '@ariadne/plugin-sdk'
import type { ResolvedModel } from './runner'
import { inModelOrder, orderKey, toChatMessages, visibleHistory } from './transcript'

/** When the model's input reaches this many tokens, older history is summarised. */
export const COMPACT_AT_TOKENS = 100_000

/** A rough count that errs high: Chinese text runs near a token per character. */
export const estimateTokens = (messages: ChatMessage[]) =>
  Math.ceil(JSON.stringify(messages).length / 2.5)

const sizeOf = (entry: TranscriptEntry) => JSON.stringify(entry).length / 2.5

/**
 * Where to cut a conversation's history so that about `keepTokens` of the newest stays in
 * full. The cut never separates a step from its tool results, and something is always kept.
 * Returns the position to summarise up to (in the model's order), or null when there is
 * nothing worth folding.
 */
export function compactionCut(transcript: TranscriptEntry[], keepTokens: number): number | null {
  const ordered = inModelOrder(visibleHistory(transcript).entries)
  // Everything from `target` on is kept at least.
  let target = ordered.length
  let kept = 0
  while (target > 0 && kept < keepTokens) {
    target--
    kept += sizeOf(ordered[target]!)
  }
  // Positions where every call made before them has its result before them, too.
  const answeredAt = new Map<string, number>()
  ordered.forEach((entry, index) => {
    if (entry.kind === 'tool') answeredAt.set(entry.toolCallId, index)
  })
  let lastAnswer = 0
  let best = 0
  for (let index = 0; index < ordered.length && index <= target; index++) {
    if (index > 0 && index > lastAnswer) best = index
    const entry = ordered[index]!
    if (entry.kind === 'step') {
      for (const call of entry.toolCalls) {
        lastAnswer = Math.max(lastAnswer, answeredAt.get(call.id) ?? Number.POSITIVE_INFINITY)
      }
    }
  }
  // Leave at least a little so the summary is worth its call.
  if (best < 2) return null
  return orderKey(ordered[best - 1]!)
}

/** What the summarising call reads: the history up to the cut, then the ask. */
export function summaryRequest(
  transcript: TranscriptEntry[],
  upTo: number,
  titleOf: (threadId: string) => string,
  role: 'coordinator' | 'thread',
): ChatMessage[] {
  const folded = transcript.filter((entry) => entry.kind === 'summary' || orderKey(entry) <= upTo)
  return [
    {
      role: 'system',
      content: `You summarise the earlier part of a conversation so that the agent having it (the ${role} of an Ariadne project) can carry on from the summary alone. You have no tools; answer with the summary only.`,
    },
    ...toChatMessages(folded, titleOf),
    {
      role: 'user',
      content: `Summarise the conversation so far, for yourself, to continue from it. Keep, as short bullet points:
- what the user wants and every lasting preference or decision they stated, in their words where it matters;
- what was done and what was found, with the ids, paths, branches, pull requests and artifact names involved;
- what is under way or promised, and what waits on whom;
- open questions and anything that failed.
Drop pleasantries, raw tool output and anything no longer relevant. Write in the language the user writes in.`,
    },
  ]
}

/** Ask the model for the summary; null when it could not give one, so the turn goes on. */
export async function summarise(
  model: ResolvedModel,
  messages: ChatMessage[],
  signal: AbortSignal,
): Promise<{ text: string; usage: { input: number; output: number } | null } | null> {
  let text = ''
  let usage: { input: number; output: number } | null = null
  try {
    for await (const part of model.provider.stream(model.settings as never, {
      model: model.model,
      messages,
      tools: [],
      signal,
    })) {
      if (part.type === 'text') text += part.text
      else if (part.type === 'usage') usage = { input: part.input, output: part.output }
      else if (part.type === 'finish' && (part.reason === 'error' || part.reason === 'length')) {
        return null
      }
    }
  } catch {
    return null
  }
  return text.trim() ? { text: text.trim(), usage } : null
}
