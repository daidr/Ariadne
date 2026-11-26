import { ref } from 'vue'
import type { PrototypeTweaksPreviewEntry } from './prototype-bridge'
import type { PrototypeTweakValues } from './prototype-tweaks'

export interface PendingArtifactFeedback {
  pendingChanges: number
  pendingNotes: number
  paused: boolean
}

export interface PendingPrototypeTweakRollback {
  nodeId: string
  nextDiff: PrototypeTweakValues | undefined
  prevDiff: PrototypeTweakValues | undefined
  nextSavedKey: string
  prevSavedKey: string
  preview?: PrototypeTweaksPreviewEntry
}

/**
 * Remembers unsent artifact feedback counts and paused state across view switches
 * (e.g., inspecting older versions or closing and reopening an artifact tab).
 */
const pending = new Map<string, PendingArtifactFeedback>()

/**
 * Remembers prototype tweak rollbacks and local retry previews when a send or withdraw settles
 * after the user already left the whiteboard (or while disconnected). Reconciled as soon as
 * the whiteboard room is live and synced again.
 */
const pendingPrototypeRollbacks = new Map<string, Map<string, PendingPrototypeTweakRollback>>()
export const pendingPrototypeRollbackRevision = ref(0)

export function getPendingArtifactFeedback(
  artifactId: string,
): PendingArtifactFeedback | undefined {
  return pending.get(artifactId)
}

export function setPendingArtifactFeedback(
  artifactId: string,
  state: PendingArtifactFeedback,
): void {
  if (state.pendingChanges <= 0) {
    pending.delete(artifactId)
    return
  }
  pending.set(artifactId, { ...state })
}

export function clearPendingArtifactFeedback(artifactId: string): void {
  pending.delete(artifactId)
}

export function setPendingPrototypeTweakRollback(
  artifactId: string,
  entry: PendingPrototypeTweakRollback,
): void {
  let byNode = pendingPrototypeRollbacks.get(artifactId)
  if (!byNode) {
    byNode = new Map()
    pendingPrototypeRollbacks.set(artifactId, byNode)
  }
  byNode.set(entry.nodeId, {
    nodeId: entry.nodeId,
    nextDiff: entry.nextDiff ? { ...entry.nextDiff } : undefined,
    prevDiff: entry.prevDiff ? { ...entry.prevDiff } : undefined,
    nextSavedKey: entry.nextSavedKey,
    prevSavedKey: entry.prevSavedKey,
    preview: entry.preview
      ? {
          specsKey: entry.preview.specsKey,
          savedKey: entry.preview.savedKey,
          liveValues: { ...entry.preview.liveValues },
        }
      : undefined,
  })
  pendingPrototypeRollbackRevision.value++
}

export function getPendingPrototypeTweakRollbacks(
  artifactId: string,
): PendingPrototypeTweakRollback[] {
  const byNode = pendingPrototypeRollbacks.get(artifactId)
  return byNode ? Array.from(byNode.values()) : []
}

export function clearPendingPrototypeTweakRollback(artifactId: string, nodeId?: string): void {
  if (nodeId === undefined) {
    if (pendingPrototypeRollbacks.delete(artifactId)) {
      pendingPrototypeRollbackRevision.value++
    }
    return
  }
  const byNode = pendingPrototypeRollbacks.get(artifactId)
  if (!byNode || !byNode.delete(nodeId)) return
  if (byNode.size === 0) pendingPrototypeRollbacks.delete(artifactId)
  pendingPrototypeRollbackRevision.value++
}
