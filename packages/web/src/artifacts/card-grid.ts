import type { ArtifactSummary } from '@ariadne/core'

/** Switch from a plain CSS grid to a row-virtualized list once card count exceeds this threshold. */
export const ARTIFACT_VIRTUALIZE_THRESHOLD = 40

export interface ArtifactCardRow {
  key: string
  items: ArtifactSummary[]
}

/** Match `repeat(auto-fill, minmax(min(minCardWidth, 100%), 1fr))` column count. */
export function gridColumnCount(width: number, minCardWidth: number, gap: number): number {
  if (!Number.isFinite(width) || width <= 0) return 1
  return Math.max(1, Math.floor((width + gap) / (minCardWidth + gap)))
}

/** Group artifact cards into horizontal rows so DouVirtualList can virtualize a responsive grid. */
export function chunkArtifactRows(
  artifacts: readonly ArtifactSummary[],
  columns: number,
): ArtifactCardRow[] {
  const cols = Math.max(1, Math.floor(columns))
  const rows: ArtifactCardRow[] = []
  for (let i = 0; i < artifacts.length; i += cols) {
    const items = artifacts.slice(i, i + cols)
    rows.push({ key: items[0]!.id, items })
  }
  return rows
}
