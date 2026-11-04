/** Where the user is in their earlier messages, and what they had typed before going there. */
export interface Recall {
  index: number
  draft: string
}

/**
 * Step through `sent` (newest first) with ↑ (`1`) and ↓ (`-1`). It only starts on an empty
 * input and only goes on while the shown message is unchanged, so arrows still move the
 * caret in anything being written. Past the newest, the earlier draft comes back. `null`:
 * the key is not for the history.
 */
export function stepHistory(
  sent: string[],
  recall: Recall | null,
  draft: string,
  step: 1 | -1,
): { recall: Recall | null; draft: string } | null {
  const browsing = recall !== null && sent[recall.index] === draft
  if (!browsing) {
    if (step < 0 || draft !== '' || !sent.length) return null
    return { recall: { index: 0, draft }, draft: sent[0]! }
  }
  const index = recall.index + step
  if (index < 0) return { recall: null, draft: recall.draft }
  if (index >= sent.length) return null
  return { recall: { index, draft: recall.draft }, draft: sent[index]! }
}
