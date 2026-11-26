import type { ArtifactCommentElement } from '@ariadne/core'
import type { FlowDocument, FlowNode } from '@dou-ui/flow'
import type { InjectionKey, Ref } from 'vue'
import * as Y from 'yjs'
import {
  PROTOTYPE_DEFAULT_VIEWPORT_HEIGHT,
  PROTOTYPE_DEFAULT_VIEWPORT_WIDTH,
  PROTOTYPE_HEADER_HEIGHT,
  areTweakValuesEqual,
  type PrototypeNodeData,
  type PrototypeTweakValues,
} from './prototype-tweaks'

export interface PrototypeTweaksPreviewEntry {
  specsKey: string
  savedKey: string
  liveValues: PrototypeTweakValues
}

export interface PrototypeTweaksSubmitOptions {
  nodeId: string
  nextDiff: PrototypeTweakValues | undefined
  prevDiff: PrototypeTweakValues | undefined
  nextSavedKey: string
  prevSavedKey: string
}

export interface PrototypeTweaksFeedbackBridge {
  /**
   * Commits final prototype tweak values silently to the whiteboard (through the persistent
   * whiteboard editor transaction rather than a volatile node instance) and sends them as
   * feedback immediately. Returns true if a feedback round was recorded on the server.
   */
  submitTweaks: (options: PrototypeTweaksSubmitOptions) => Promise<boolean>
  /** Reactive flag indicating a feedback send is currently in flight for this artifact. */
  sending: Readonly<Ref<boolean>>
  /** Node ID whose prototype tweaks are currently being submitted, if any. */
  sendingNodeId: Readonly<Ref<string | null>>
  /** Bumped when rollback recovery restores a cached preview while nodes are already mounted. */
  previewRevision: Readonly<Ref<number>>
  /** Reads the room-level local preview cache entry for a prototype node. */
  getPreview: (nodeId: string) => PrototypeTweaksPreviewEntry | undefined
  /** Stores or updates the room-level local preview cache entry for a prototype node. */
  setPreview: (nodeId: string, entry: PrototypeTweaksPreviewEntry) => void
  /** Clears the room-level local preview cache entry for a prototype node. */
  clearPreview: (nodeId: string) => void
}

export type PrototypeTweakDocRead =
  { exists: false } | { exists: true; tweakValues: PrototypeTweakValues | undefined }

/** Reads a prototype node's current `data.tweakValues` directly from a whiteboard Y.Doc. */
export function readDocPrototypeTweakValues(doc: Y.Doc, nodeId: string): PrototypeTweakDocRead {
  const nodes = doc.getMap('nodes')
  const fields = nodes.get(nodeId)
  if (!(fields instanceof Y.Map)) return { exists: false }
  const data = fields.get('data')
  const raw =
    data && typeof data === 'object' && !Array.isArray(data)
      ? (data as Record<string, unknown>).tweakValues
      : undefined
  const tweakValues =
    raw && typeof raw === 'object' && !Array.isArray(raw)
      ? (raw as PrototypeTweakValues)
      : undefined
  return { exists: true, tweakValues }
}

/**
 * Rolls back a prototype node's `data.tweakValues` in a whiteboard Y.Doc from `expectedNext`
 * to `prevDiff` only when the node still holds `expectedNext`, so concurrent remote or AI edits
 * are never overwritten.
 */
export function rollbackDocPrototypeTweakValues(
  doc: Y.Doc,
  nodeId: string,
  expectedNext: PrototypeTweakValues | undefined,
  prevDiff: PrototypeTweakValues | undefined,
): 'rolled-back' | 'already-reverted' | 'diverged' {
  const current = readDocPrototypeTweakValues(doc, nodeId)
  if (!current.exists) return 'diverged'
  if (areTweakValuesEqual(current.tweakValues, expectedNext)) {
    const nodes = doc.getMap('nodes')
    const fields = nodes.get(nodeId)
    if (!(fields instanceof Y.Map)) return 'diverged'
    doc.transact(() => {
      const prevData = fields.get('data')
      const nextData: Record<string, unknown> =
        prevData && typeof prevData === 'object' && !Array.isArray(prevData)
          ? { ...(prevData as Record<string, unknown>) }
          : {}
      if (prevDiff && Object.keys(prevDiff).length > 0) {
        nextData.tweakValues = { ...prevDiff }
      } else {
        delete nextData.tweakValues
      }
      fields.set('data', nextData)
    })
    return 'rolled-back'
  }
  if (areTweakValuesEqual(current.tweakValues, prevDiff)) {
    return 'already-reverted'
  }
  return 'diverged'
}

export const PROTOTYPE_TWEAKS_BRIDGE_KEY: InjectionKey<PrototypeTweaksFeedbackBridge> = Symbol(
  'ariadne:prototype-tweaks-bridge',
)

export type PrototypeInspector = (
  viewportX: number,
  viewportY: number,
) => Promise<ArtifactCommentElement | null>

const inspectors = new Map<string, PrototypeInspector>()

/** Registers a mounted prototype node so comment pinning can query semantic hit elements. */
export function registerPrototypeInspector(
  nodeId: string,
  inspector: PrototypeInspector,
): () => void {
  inspectors.set(nodeId, inspector)
  return () => {
    if (inspectors.get(nodeId) === inspector) inspectors.delete(nodeId)
  }
}

export async function inspectPrototypePoint(
  nodeId: string,
  viewportX: number,
  viewportY: number,
  timeoutMs = 200,
): Promise<ArtifactCommentElement | null> {
  const inspector = inspectors.get(nodeId)
  if (!inspector) return null
  return await Promise.race([
    inspector(viewportX, viewportY).catch(() => null),
    new Promise<null>((resolve) => setTimeout(() => resolve(null), timeoutMs)),
  ])
}

export interface PrototypeHitTarget {
  prototypeNodeId: string
  viewportX: number
  viewportY: number
}

/**
 * Resolves a comment pin (normalized 0..1 on `anchorNode`) to logical viewport coordinates inside
 * either `anchorNode` itself (when it is a prototype) or an embedded prototype child inside an option.
 */
export function resolvePrototypeViewportPoint(
  doc: FlowDocument,
  anchorNodeId: string,
  pinX: number,
  pinY: number,
): PrototypeHitTarget | null {
  const anchor = doc.nodes.find((n) => n.id === anchorNodeId)
  if (!anchor) return null

  const aw = anchor.width ?? 0
  const ah = anchor.height ?? 0
  if (aw <= 0 || ah <= 0) return null

  const canvasX = anchor.x + pinX * aw
  const canvasY = anchor.y + pinY * ah

  let proto: FlowNode | undefined
  if (anchor.type === 'prototype') {
    proto = anchor
  } else {
    proto = doc.nodes.find((n) => {
      if (n.type !== 'prototype') return false
      const w = n.width ?? 0
      const h = n.height ?? 0
      return canvasX >= n.x && canvasX <= n.x + w && canvasY >= n.y && canvasY <= n.y + h
    })
  }
  if (!proto) return null

  const pw = proto.width ?? 0
  const ph = proto.height ?? 0
  const stageW = Math.max(pw, 1)
  const stageH = Math.max(ph - PROTOTYPE_HEADER_HEIGHT, 1)
  const localX = canvasX - proto.x
  const localY = canvasY - proto.y - PROTOTYPE_HEADER_HEIGHT
  if (localX < 0 || localX > stageW || localY < 0 || localY > stageH) return null

  const data = (proto.data ?? {}) as PrototypeNodeData
  const vw =
    typeof data.viewportWidth === 'number' && data.viewportWidth > 0
      ? data.viewportWidth
      : PROTOTYPE_DEFAULT_VIEWPORT_WIDTH
  const vh =
    typeof data.viewportHeight === 'number' && data.viewportHeight > 0
      ? data.viewportHeight
      : PROTOTYPE_DEFAULT_VIEWPORT_HEIGHT

  const scale = Math.min(stageW / vw, stageH / vh)
  if (scale <= 0) return null
  const renderedW = vw * scale
  const renderedH = vh * scale
  const offsetX = (stageW - renderedW) / 2
  const offsetY = (stageH - renderedH) / 2

  const vx = (localX - offsetX) / scale
  const vy = (localY - offsetY) / scale
  if (vx < 0 || vx > vw || vy < 0 || vy > vh) return null

  return {
    prototypeNodeId: proto.id,
    viewportX: Math.round(vx * 100) / 100,
    viewportY: Math.round(vy * 100) / 100,
  }
}
