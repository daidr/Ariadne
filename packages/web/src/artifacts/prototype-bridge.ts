import type { ArtifactCommentElement } from '@ariadne/core'
import type { FlowDocument, FlowNode } from '@dou-ui/flow'
import type { InjectionKey, Ref } from 'vue'
import {
  PROTOTYPE_DEFAULT_VIEWPORT_HEIGHT,
  PROTOTYPE_DEFAULT_VIEWPORT_WIDTH,
  PROTOTYPE_HEADER_HEIGHT,
  type PrototypeNodeData,
} from './prototype-tweaks'

export interface PrototypeTweaksFeedbackBridge {
  /**
   * Commits final prototype tweak values silently to the whiteboard (without incrementing
   * pendingChanges or starting the auto-send countdown) and sends them as feedback immediately.
   * Returns true if a feedback round was recorded on the server.
   */
  submitTweaks: (options: { apply: () => void; rollback: () => void }) => Promise<boolean>
  /** Reactive flag indicating a feedback send is currently in flight for this artifact. */
  sending: Readonly<Ref<boolean>>
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
