import type { DocumentAskAnswerBaseline } from '@ariadne/core'
import * as Y from 'yjs'
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
  /**
   * True when at least one active ask differs from the submitted/agent baseline
   * (or, when no baseline is provided, at least one active ask has an answer).
   */
  hasUnsentChanges: boolean
  /**
   * True when the document still needs inline-ask submission: either at least one
   * active ask is unanswered, or at least one active ask has unsent changes.
   * False when all active asks are answered, already submitted, and unchanged.
   */
  needsSubmit: boolean
}

function normalizeAskSelected(selected: readonly string[]): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const item of selected) {
    const trimmed = item.trim()
    if (!trimmed || seen.has(trimmed)) continue
    seen.add(trimmed)
    out.push(trimmed)
  }
  out.sort()
  return out
}

function sameStringList(a: readonly string[], b: readonly string[]): boolean {
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false
  }
  return true
}

/** Captures a canonical answer snapshot matching backend `documentAskBaseline`. */
export function snapshotDocumentAsks(asks: readonly AskBlockProps[]): DocumentAskAnswerBaseline[] {
  return asks.map((ask) => ({
    id: ask.id,
    selected: normalizeAskSelected(ask.selected),
    note: ask.note.trim(),
  }))
}

export function summarizeDocumentAsks(
  asks: readonly AskBlockProps[],
  baseline?: readonly DocumentAskAnswerBaseline[] | null,
): DocumentAskSummary {
  let totalAsks = 0
  let answeredAsks = 0
  let recommendedFallbackAsks = 0
  let remainingAsks = 0
  let hasUnsentChanges = false

  let baselineById: Map<string, { selected: string[]; note: string }> | null = null
  if (baseline != null) {
    baselineById = new Map()
    for (const entry of baseline) {
      baselineById.set(entry.id, {
        selected: normalizeAskSelected(entry.selected),
        note: entry.note.trim(),
      })
    }
  }

  for (const ask of asks) {
    if (ask.confirmed) continue
    totalAsks++
    const currSelected = normalizeAskSelected(ask.selected)
    const currNote = ask.note.trim()
    const answered = currSelected.length > 0 || currNote.length > 0
    if (baselineById) {
      const prev = baselineById.get(ask.id)
      if (prev) {
        if (!sameStringList(currSelected, prev.selected) || currNote !== prev.note) {
          hasUnsentChanges = true
        }
      } else if (answered) {
        hasUnsentChanges = true
      }
    } else if (answered) {
      hasUnsentChanges = true
    }

    if (answered) {
      answeredAsks++
      continue
    }
    const hasRecommended = ask.pick !== 'text' && ask.options.some((opt) => opt.recommended)
    if (hasRecommended) recommendedFallbackAsks++
    else remainingAsks++
  }

  const hasActiveAsks = totalAsks > 0
  const allAnswered = hasActiveAsks && answeredAsks === totalAsks
  const needsSubmit = hasActiveAsks && (!allAnswered || hasUnsentChanges)

  return {
    totalAsks,
    answeredAsks,
    recommendedFallbackAsks,
    remainingAsks,
    hasActiveAsks,
    hasUnsentChanges,
    needsSubmit,
  }
}

const ASK_ANSWER_PROP_KEYS = new Set(['selected', 'note'])

/**
 * Reports whether a Yjs transaction only modified user answer fields (`selected` / `note`)
 * on `ask` blocks inside the shared `document` map.
 */
export function isAskAnswerOnlyTransaction(doc: Y.Doc, tr: Y.Transaction | undefined): boolean {
  if (!tr || tr.changed.size === 0) return false
  const root = doc.getMap('document')
  for (const [target, keys] of tr.changed) {
    if (keys.size === 0) return false
    const block = target.parent
    if (!(block instanceof Y.Map) || block.parent !== root) return false
    if (block.get('type') !== 'ask' || block.get('props') !== target) return false
    for (const key of keys) {
      if (key === null || !ASK_ANSWER_PROP_KEYS.has(key)) return false
    }
  }
  return true
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
