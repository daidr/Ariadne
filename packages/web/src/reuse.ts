/**
 * `next`, with every part that equals the same part of `previous` taken from `previous`.
 * A refresh brings the whole state again though little of it changed; keeping the objects
 * that did not change lets whatever shows them stay as it is. Both are plain JSON.
 */
export function reuse<T>(previous: unknown, next: T): T {
  if (Object.is(previous, next)) return next
  if (typeof previous !== 'object' || typeof next !== 'object' || !previous || !next) return next
  if (Array.isArray(next)) {
    if (!Array.isArray(previous)) return next
    // Rows mostly keep their place, and one that moved is found again by its id.
    let byId: Map<unknown, unknown> | null = null
    let same = previous.length === next.length
    const merged = next.map((item: unknown, index) => {
      let before: unknown = previous[index]
      const id = idOf(item)
      if (id !== undefined && idOf(before) !== id) {
        byId ??= new Map(
          previous.flatMap((row) => (idOf(row) === undefined ? [] : [[idOf(row), row]])),
        )
        before = byId.get(id)
      }
      const kept = reuse(before, item)
      if (kept !== previous[index]) same = false
      return kept
    })
    return (same ? previous : merged) as T
  }
  if (Array.isArray(previous)) return next
  const before = previous as Record<string, unknown>
  const after = next as Record<string, unknown>
  const keys = Object.keys(after)
  let same = Object.keys(before).length === keys.length
  const merged: Record<string, unknown> = {}
  for (const key of keys) {
    merged[key] = reuse(before[key], after[key])
    if (merged[key] !== before[key] || !(key in before)) same = false
  }
  return (same ? previous : merged) as T
}

function idOf(value: unknown): unknown {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as { id?: unknown }).id
    : undefined
}
