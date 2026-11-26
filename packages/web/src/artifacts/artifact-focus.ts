import { ref } from 'vue'

export interface ArtifactFocusRequest {
  artifactId: string
  nodeId: string
  seq: number
}

let nextSeq = 0

/** Pending focus request set when jumping from a cross-artifact reference card to a node/group. */
export const artifactFocusTarget = ref<ArtifactFocusRequest | null>(null)

export function requestArtifactFocus(artifactId: string, nodeId: string) {
  if (!artifactId || !nodeId) return
  artifactFocusTarget.value = { artifactId, nodeId, seq: ++nextSeq }
}

export function clearArtifactFocus(seq: number) {
  if (artifactFocusTarget.value?.seq === seq) {
    artifactFocusTarget.value = null
  }
}

export function clearAllArtifactFocus() {
  artifactFocusTarget.value = null
}
