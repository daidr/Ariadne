export interface PendingArtifactFeedback {
  pendingChanges: number
  pendingNotes: number
  paused: boolean
}

/**
 * Remembers unsent artifact feedback counts and paused state across view switches
 * (e.g., inspecting older versions or closing and reopening an artifact tab).
 */
const pending = new Map<string, PendingArtifactFeedback>()

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
