export interface Point {
  x: number
  y: number
}

export interface Size {
  width: number
  height: number
}

export interface ViewportMetrics {
  /** Visible stage bounds in CSS pixels. */
  viewport: Size
  /** Untransformed image size (at scale 1, rotation 0). */
  baseSize: Size
  /** Untransformed image center in viewport client coordinates. */
  baseCenter: Point
  /** Shift between `baseCenter` and the stage's geometric center (e.g. asymmetric padding). */
  centerOffset: Point
}

export interface TransformState {
  scale: number
  offset: Point
}

const SCALE_PRECISION = 10_000
const OFFSET_PRECISION = 100

/** Clamp a zoom factor to `[min, max]` while preserving smooth precision for pinch gestures. */
export function clampScale(value: number, min: number, max: number): number {
  const clamped = Math.min(max, Math.max(min, value))
  return Math.round(clamped * SCALE_PRECISION) / SCALE_PRECISION
}

/** Step zoom by a constant ratio so each increment feels visually even. */
export function stepScale(scale: number, direction: 1 | -1): number {
  return direction > 0 ? scale * 1.25 : scale / 1.25
}

/** Distance between two pointer coordinates in CSS pixels. */
export function pointerDistance(a: Point, b: Point): number {
  return Math.hypot(b.x - a.x, b.y - a.y)
}

/** Midpoint between two pointer coordinates in CSS pixels. */
export function pointerMidpoint(a: Point, b: Point): Point {
  return { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }
}

/** Effective rendered dimensions after scaling and 90-degree rotations. */
export function effectiveSize(baseSize: Size, scale: number, rotate: number): Size {
  const quarterTurn = Math.abs(Math.round(rotate / 90)) % 2 === 1
  return quarterTurn
    ? { width: baseSize.height * scale, height: baseSize.width * scale }
    : { width: baseSize.width * scale, height: baseSize.height * scale }
}

function clampAxis(
  value: number,
  renderedSize: number,
  viewportSize: number,
  centerOffset: number,
): number {
  if (renderedSize <= viewportSize || viewportSize <= 0) return 0
  const halfOverflow = (renderedSize - viewportSize) / 2
  const min = -halfOverflow - centerOffset
  const max = halfOverflow - centerOffset
  const clamped = Math.min(max, Math.max(min, value))
  return Math.round(clamped * OFFSET_PRECISION) / OFFSET_PRECISION
}

/**
 * Keep the zoomed image covering the viewport without leaving empty gaps or drifting off-screen.
 * When an axis fits inside the viewport, it centers back to `0`.
 */
export function clampOffset(
  offset: Point,
  scale: number,
  rotate: number,
  metrics: ViewportMetrics,
): Point {
  if (scale <= 1) return { x: 0, y: 0 }
  const rendered = effectiveSize(metrics.baseSize, scale, rotate)
  return {
    x: clampAxis(offset.x, rendered.width, metrics.viewport.width, metrics.centerOffset.x),
    y: clampAxis(offset.y, rendered.height, metrics.viewport.height, metrics.centerOffset.y),
  }
}

/**
 * Zoom from `state.scale` to `nextScale` while keeping the image point under `fromClient`
 * anchored at `toClient` (supporting both stationary focal zoom and moving two-finger pinch).
 */
export function zoomAroundPoint(
  state: TransformState,
  nextScale: number,
  fromClient: Point,
  toClient: Point,
  rotate: number,
  metrics: ViewportMetrics,
): TransformState {
  if (state.scale <= 0) {
    return { scale: nextScale, offset: { x: 0, y: 0 } }
  }
  const fromLocalX = fromClient.x - metrics.baseCenter.x
  const fromLocalY = fromClient.y - metrics.baseCenter.y
  const toLocalX = toClient.x - metrics.baseCenter.x
  const toLocalY = toClient.y - metrics.baseCenter.y
  const ratio = nextScale / state.scale
  const rawOffset: Point = {
    x: toLocalX - ratio * (fromLocalX - state.offset.x),
    y: toLocalY - ratio * (fromLocalY - state.offset.y),
  }
  return {
    scale: nextScale,
    offset: clampOffset(rawOffset, nextScale, rotate, metrics),
  }
}
