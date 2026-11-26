import { innerLink } from '../markdown/links'
import { viewOf, isPaneKey } from '../side/layout'

export interface ArtifactHistoryEntry {
  id: string
  /** Optional whiteboard node or decision group ID to focus when arriving at this entry. */
  focusNodeId?: string
}

export interface ArtifactHistoryStack {
  entries: ArtifactHistoryEntry[]
  index: number
}

export interface ArtifactNavigationTarget {
  artifactId: string
  focusNodeId?: string
}

export interface ArtifactHistoryNeighbors {
  back: ArtifactHistoryEntry | null
  forward: ArtifactHistoryEntry | null
}

const MAX_ARTIFACT_HISTORY = 50

const makeEntry = (id: string, focusNodeId?: string): ArtifactHistoryEntry =>
  focusNodeId ? { id, focusNodeId } : { id }

export function createArtifactHistory(id: string, focusNodeId?: string): ArtifactHistoryStack {
  return { entries: [makeEntry(id, focusNodeId)], index: 0 }
}

/**
 * Record an in-place jump from `fromId` to `toId`. Truncates forward history beyond the
 * current index and avoids pushing consecutive duplicate entries when jumping within the
 * same artifact.
 */
export function pushArtifactHistory(
  stack: ArtifactHistoryStack | undefined,
  fromId: string,
  toId: string,
  focusNodeId?: string,
): ArtifactHistoryStack {
  const base =
    stack && stack.entries[stack.index]?.id === fromId ? stack : createArtifactHistory(fromId)
  if (toId === fromId) {
    if (!focusNodeId || base.entries[base.index]?.focusNodeId === focusNodeId) return base
    const entries = base.entries.map((entry, idx) =>
      idx === base.index ? makeEntry(fromId, focusNodeId) : entry,
    )
    return { entries, index: base.index }
  }
  const combined = [...base.entries.slice(0, base.index + 1), makeEntry(toId, focusNodeId)]
  const overflow = combined.length - MAX_ARTIFACT_HISTORY
  const entries = overflow > 0 ? combined.slice(overflow) : combined
  return { entries, index: entries.length - 1 }
}

/** Move one step back (`-1`) or forward (`1`) in `stack` when `currentId` matches the cursor. */
export function stepArtifactHistoryStack(
  stack: ArtifactHistoryStack | undefined,
  currentId: string,
  delta: -1 | 1,
): { stack: ArtifactHistoryStack; entry: ArtifactHistoryEntry } | null {
  if (!stack || stack.entries[stack.index]?.id !== currentId) return null
  const nextIndex = stack.index + delta
  const entry = stack.entries[nextIndex]
  if (!entry) return null
  return { stack: { entries: stack.entries, index: nextIndex }, entry }
}

/**
 * Remove entries that no longer exist (for example deleted artifacts) while keeping the
 * cursor aligned and collapsing adjacent duplicates left behind by removals.
 */
export function pruneArtifactHistory(
  stack: ArtifactHistoryStack | undefined,
  keep: (id: string) => boolean,
): ArtifactHistoryStack | undefined {
  if (!stack || !stack.entries.length) return undefined
  const current = stack.entries[stack.index]
  if (!current || !keep(current.id)) return undefined
  const entries: ArtifactHistoryEntry[] = []
  let nextIndex = 0
  for (let i = 0; i < stack.entries.length; i++) {
    const entry = stack.entries[i]!
    if (!keep(entry.id)) continue
    const previous = entries[entries.length - 1]
    if (previous && previous.id === entry.id) {
      if (i === stack.index) {
        entries[entries.length - 1] = entry
        nextIndex = entries.length - 1
      }
      continue
    }
    if (i === stack.index) nextIndex = entries.length
    entries.push(entry)
  }
  return entries.length > 1 ? { entries, index: nextIndex } : undefined
}

/** Immediate back and forward targets for `currentId`, or `null` when unavailable. */
export function artifactHistoryNeighbors(
  stack: ArtifactHistoryStack | undefined,
  currentId: string,
): ArtifactHistoryNeighbors {
  if (!stack || stack.entries[stack.index]?.id !== currentId) {
    return { back: null, forward: null }
  }
  return {
    back: stack.entries[stack.index - 1] ?? null,
    forward: stack.entries[stack.index + 1] ?? null,
  }
}

/**
 * Re-align an existing history stack when an external navigation (such as browser Back/Forward)
 * moves the active artifact to an adjacent entry in the stack.
 */
export function syncArtifactHistoryCursor(
  stack: ArtifactHistoryStack | undefined,
  currentId: string,
): ArtifactHistoryStack | undefined {
  if (!stack) return undefined
  if (stack.entries[stack.index]?.id === currentId) return stack
  if (stack.entries[stack.index - 1]?.id === currentId) {
    return { entries: stack.entries, index: stack.index - 1 }
  }
  if (stack.entries[stack.index + 1]?.id === currentId) {
    return { entries: stack.entries, index: stack.index + 1 }
  }
  return undefined
}

/** Validate a serialized history stack (e.g. from browser history.state) before restoring it. */
export function parseArtifactHistoryStack(
  raw: unknown,
  expectedCurrentId?: string,
): ArtifactHistoryStack | undefined {
  if (!raw || typeof raw !== 'object') return undefined
  const { entries, index } = raw as { entries?: unknown; index?: unknown }
  if (!Array.isArray(entries) || typeof index !== 'number' || !Number.isInteger(index)) {
    return undefined
  }
  if (entries.length < 2 || index < 0 || index >= entries.length) return undefined
  const parsed: ArtifactHistoryEntry[] = []
  for (const item of entries) {
    if (!item || typeof item !== 'object') return undefined
    const { id, focusNodeId } = item as { id?: unknown; focusNodeId?: unknown }
    if (typeof id !== 'string' || !id.trim()) return undefined
    if (focusNodeId !== undefined && typeof focusNodeId !== 'string') return undefined
    parsed.push(makeEntry(id, focusNodeId || undefined))
  }
  const trimmed =
    parsed.length > MAX_ARTIFACT_HISTORY
      ? parsed.slice(parsed.length - MAX_ARTIFACT_HISTORY)
      : parsed
  const shiftedIndex =
    parsed.length > MAX_ARTIFACT_HISTORY ? index - (parsed.length - MAX_ARTIFACT_HISTORY) : index
  if (shiftedIndex < 0 || shiftedIndex >= trimmed.length) return undefined
  if (expectedCurrentId && trimmed[shiftedIndex]?.id !== expectedCurrentId) return undefined
  return { entries: trimmed, index: shiftedIndex }
}

/**
 * Extract an artifact ID from an inline `artifact:<id>` link or a same-project workspace URL
 * pointing at an artifact tab/modal.
 */
export function referencedArtifactId(href: string | undefined, projectId: string): string | null {
  const raw = href?.trim()
  if (!raw) return null
  const inner = innerLink(raw)
  if (inner) return inner.kind === 'artifact' ? inner.id : null
  if (!projectId) return null

  const rawOrigin = typeof window !== 'undefined' ? window.location?.origin : undefined
  const origin = rawOrigin && rawOrigin !== 'null' ? rawOrigin : 'http://ariadne.local'
  let parsed: URL
  try {
    parsed = new URL(raw, origin)
    if (parsed.origin !== origin) return null
  } catch {
    return null
  }

  const segments = parsed.pathname.split('/').filter(Boolean)
  if (segments.length !== 2 || segments[0] !== 'projects') return null
  let routeProjectId: string
  try {
    routeProjectId = decodeURIComponent(segments[1]!)
  } catch {
    return null
  }
  if (routeProjectId !== projectId) return null

  for (const param of ['modal', 'tab'] as const) {
    const value = parsed.searchParams.get(param)
    if (!value || !isPaneKey(value)) continue
    const view = viewOf(value)
    if (view?.kind === 'artifact') return view.id
  }
  return null
}
