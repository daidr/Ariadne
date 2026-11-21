import { describe, expect, test } from 'vitest'
import {
  clampOffset,
  clampScale,
  effectiveSize,
  pointerDistance,
  pointerMidpoint,
  stepScale,
  zoomAroundPoint,
  type ViewportMetrics,
} from './image-viewport'

const centeredMetrics: ViewportMetrics = {
  viewport: { width: 400, height: 800 },
  baseSize: { width: 400, height: 300 },
  baseCenter: { x: 200, y: 400 },
  centerOffset: { x: 0, y: 0 },
}

describe('image viewport math', () => {
  test('clamps and steps zoom scale', () => {
    expect(clampScale(0.1, 0.25, 5)).toBe(0.25)
    expect(clampScale(8, 0.25, 5)).toBe(5)
    expect(clampScale(1.23456, 0.25, 5)).toBe(1.2346)
    expect(stepScale(1, 1)).toBe(1.25)
    expect(stepScale(1.25, -1)).toBe(1)
  })

  test('computes pointer distance and midpoint', () => {
    expect(pointerDistance({ x: 0, y: 0 }, { x: 30, y: 40 })).toBe(50)
    expect(pointerMidpoint({ x: 10, y: 20 }, { x: 50, y: 80 })).toEqual({ x: 30, y: 50 })
  })

  test('clamps pan offset to viewport bounds and centers axes that fit', () => {
    // At 1x, offset always resets to 0.
    expect(clampOffset({ x: 50, y: 50 }, 1, 0, centeredMetrics)).toEqual({ x: 0, y: 0 })

    // At 2x, rendered size is 800x600 in a 400x800 viewport:
    // X overflows by 400 -> bounds [-200, 200]; Y fits (600 <= 800) -> stays 0.
    expect(clampOffset({ x: 350, y: 120 }, 2, 0, centeredMetrics)).toEqual({ x: 200, y: 0 })
    expect(clampOffset({ x: -350, y: -120 }, 2, 0, centeredMetrics)).toEqual({ x: -200, y: 0 })
    expect(clampOffset({ x: 75, y: 40 }, 2, 0, centeredMetrics)).toEqual({ x: 75, y: 0 })

    // At 3x, rendered size is 1200x900 in a 400x800 viewport: X [-400, 400], Y [-50, 50].
    expect(clampOffset({ x: 200, y: 120 }, 3, 0, centeredMetrics)).toEqual({ x: 200, y: 50 })
  })

  test('accounts for 90-degree rotations and asymmetric center offsets', () => {
    expect(effectiveSize({ width: 400, height: 300 }, 2, 90)).toEqual({ width: 600, height: 800 })
    const shifted: ViewportMetrics = {
      viewport: { width: 400, height: 600 },
      baseSize: { width: 400, height: 400 },
      baseCenter: { x: 200, y: 280 },
      centerOffset: { x: 0, y: -20 },
    }
    // At 2x (800x800 in 400x600): halfOverflowY = 100, centerOffset.y = -20 -> [-80, 120].
    expect(clampOffset({ x: 0, y: 200 }, 2, 0, shifted)).toEqual({ x: 0, y: 120 })
    expect(clampOffset({ x: 0, y: -200 }, 2, 0, shifted)).toEqual({ x: 0, y: -80 })
  })

  test('anchors focal point during stationary zoom and moving two-finger pinch', () => {
    const fullMetrics: ViewportMetrics = {
      viewport: { width: 400, height: 400 },
      baseSize: { width: 400, height: 400 },
      baseCenter: { x: 200, y: 200 },
      centerOffset: { x: 0, y: 0 },
    }
    // Stationary zoom from 1x -> 2x around (300, 250) (local +100, +50):
    // offset shifts by (-100, -50) so that image point stays under (300, 250).
    const stationary = zoomAroundPoint(
      { scale: 1, offset: { x: 0, y: 0 } },
      2,
      { x: 300, y: 250 },
      { x: 300, y: 250 },
      0,
      fullMetrics,
    )
    expect(stationary).toEqual({ scale: 2, offset: { x: -100, y: -50 } })

    // Simultaneous pinch zoom + midpoint drift by (+30, -20).
    const moving = zoomAroundPoint(
      { scale: 1, offset: { x: 0, y: 0 } },
      2,
      { x: 300, y: 250 },
      { x: 330, y: 230 },
      0,
      fullMetrics,
    )
    expect(moving).toEqual({ scale: 2, offset: { x: -70, y: -70 } })
  })
})
