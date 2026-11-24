import type * as Y from 'yjs'
import { normalizeAskProps, type AskBlockProps } from './document-block-data'

export interface DocumentAskSummary {
  /** Number of active (unconfirmed) inline ask blocks in the document. */
  totalAsks: number
  /** Active asks where the user has selected an option or written a note. */
  answeredAsks: number
  /** Active unanswered choice asks that have at least one recommended option. */
  recommendedFallbackAsks: number
  /** Active unanswered asks without a recommended fallback. */
  remainingAsks: number
  /** True when the document has at least one active inline ask block. */
  hasActiveAsks: boolean
}

export function summarizeDocumentAsks(asks: readonly AskBlockProps[]): DocumentAskSummary {
  let totalAsks = 0
  let answeredAsks = 0
  let recommendedFallbackAsks = 0
  let remainingAsks = 0
  for (const ask of asks) {
    if (ask.confirmed) continue
    totalAsks++
    const answered = ask.selected.length > 0 || ask.note.trim().length > 0
    if (answered) {
      answeredAsks++
      continue
    }
    const hasRecommended = ask.pick !== 'text' && ask.options.some((opt) => opt.recommended)
    if (hasRecommended) recommendedFallbackAsks++
    else remainingAsks++
  }
  return {
    totalAsks,
    answeredAsks,
    recommendedFallbackAsks,
    remainingAsks,
    hasActiveAsks: totalAsks > 0,
  }
}

/** Reads all normalized ask blocks currently stored in a shared document's Yjs map. */
export function readSharedDocumentAsks(
  root: Y.Map<unknown>,
): { key: string; ask: AskBlockProps; propsMap: Y.Map<unknown> }[] {
  const result: { key: string; ask: AskBlockProps; propsMap: Y.Map<unknown> }[] = []
  root.forEach((raw, key) => {
    if (key === 'order' || !raw || typeof (raw as Y.Map<unknown>).get !== 'function') return
    const blockMap = raw as Y.Map<unknown>
    if (blockMap.get('type') !== 'ask') return
    const propsMap = blockMap.get('props')
    if (!propsMap || typeof (propsMap as Y.Map<unknown>).toJSON !== 'function') return
    const normalized = normalizeAskProps(
      (propsMap as Y.Map<unknown>).toJSON() as Record<string, unknown>,
    )
    if (!normalized) return
    result.push({ key, ask: normalized, propsMap: propsMap as Y.Map<unknown> })
  })
  return result
}

/**
 * Applies `recommended: true` options to active unanswered choice ask blocks in the shared Yjs
 * document before explicit submission, returning the number of ask blocks updated.
 */
export function applyRecommendedDocumentAsks(doc: Y.Doc): number {
  const root = doc.getMap('document')
  const entries = readSharedDocumentAsks(root)
  const updates: { propsMap: Y.Map<unknown>; selected: string[] }[] = []
  for (const { ask, propsMap } of entries) {
    if (ask.confirmed || ask.pick === 'text') continue
    if (ask.selected.length > 0 || ask.note.trim().length > 0) continue
    const recommended = ask.options.filter((opt) => opt.recommended).map((opt) => opt.id)
    if (!recommended.length) continue
    updates.push({
      propsMap,
      selected: ask.pick === 'many' ? recommended : [recommended[0]!],
    })
  }
  if (!updates.length) return 0
  doc.transact(() => {
    for (const { propsMap, selected } of updates) {
      propsMap.set('selected', selected)
    }
  })
  return updates.length
}
