import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'
import {
  clampOffset,
  clampScale,
  pointerDistance,
  pointerMidpoint,
  stepScale,
  zoomAroundPoint,
  type Point,
  type ViewportMetrics,
} from './image-viewport'

export interface UseImageViewportOptions {
  stage: Ref<HTMLElement | null>
  image: Ref<HTMLImageElement | null>
  minScale?: () => number
  maxScale?: () => number
  doubleTapScale?: number
  closeOnMask?: () => boolean
  onMaskTap?: () => void
}

interface PinchState {
  startDistance: number
  startMidpoint: Point
  startScale: number
  startOffset: Point
  minPinchScale: number
}

interface DragState {
  pointerId: number
  startX: number
  startY: number
  startOffset: Point
  captured: boolean
}

interface TapCandidate {
  pointerId: number
  pointerType: string
  isMask: boolean
  isImage: boolean
  startX: number
  startY: number
  moved: boolean
}

const DRAG_THRESHOLD = 4
const TAP_MOVE_THRESHOLD = 8
const DOUBLE_TAP_MS = 300
const DOUBLE_TAP_DISTANCE = 32
const WHEEL_STEP_INTERVAL_MS = 40

function preventGesture(event: Event) {
  event.preventDefault()
}

function trySetPointerCapture(element: HTMLElement | null, pointerId: number) {
  try {
    element?.setPointerCapture?.(pointerId)
  } catch {
    // Synthetic events or browsers without capture for this pointer still work via window listeners.
  }
}

function tryReleasePointerCapture(element: HTMLElement | null, pointerId: number) {
  try {
    if (element?.hasPointerCapture?.(pointerId)) {
      element.releasePointerCapture(pointerId)
    }
  } catch {
    // Ignore already-released pointers.
  }
}

/**
 * Interactive image viewport state and gesture handlers:
 * multi-touch pinch zoom around the midpoint, zoomed single-finger / mouse pan with boundary
 * clamping, touch double-tap & mouse double-click zoom/reset, wheel & trackpad zoom, and mask tap.
 */
export function useImageViewport(options: UseImageViewportOptions) {
  const minScale = () => options.minScale?.() ?? 0.25
  const maxScale = () => options.maxScale?.() ?? 5
  const doubleTapScale = options.doubleTapScale ?? 2
  const closeOnMask = () => options.closeOnMask?.() ?? false

  const scale = ref(1)
  const offset = ref<Point>({ x: 0, y: 0 })
  const rotate = ref(0)
  const flip = ref<{ x: number; y: number }>({ x: 1, y: 1 })
  const interacting = ref(false)

  const movable = computed(() => scale.value > 1)
  const transformStyle = computed(
    () =>
      `translate3d(${offset.value.x}px, ${offset.value.y}px, 0) scale(${scale.value}) scale(${flip.value.x}, ${flip.value.y}) rotate(${rotate.value}deg)`,
  )

  const pointers = new Map<number, Point>()
  let pinch: PinchState | null = null
  let drag: DragState | null = null
  let tapCandidate: TapCandidate | null = null
  let wasMultiTouch = false
  let lastTouchTap: { time: number; x: number; y: number } | null = null
  let lastTouchDoubleTapAt = -Infinity
  let wheelAt = -Infinity
  let suppressNextMaskClick = false
  let windowListening = false

  function readMetrics(): ViewportMetrics {
    const stageEl = options.stage.value
    const imgEl = options.image.value
    if (!stageEl) {
      return {
        viewport: { width: 0, height: 0 },
        baseSize: { width: 0, height: 0 },
        baseCenter: { x: 0, y: 0 },
        centerOffset: { x: 0, y: 0 },
      }
    }

    const stageRect = stageEl.getBoundingClientRect()
    const viewport = { width: stageRect.width, height: stageRect.height }
    const stageCenter: Point = {
      x: stageRect.left + stageRect.width / 2,
      y: stageRect.top + stageRect.height / 2,
    }
    if (!imgEl) {
      return {
        viewport,
        baseSize: viewport,
        baseCenter: stageCenter,
        centerOffset: { x: 0, y: 0 },
      }
    }

    // Prefer layout offsetWidth/offsetHeight: CSS transforms do not distort them, even mid-transition.
    let baseWidth = imgEl.offsetWidth
    let baseHeight = imgEl.offsetHeight
    let baseCenter: Point

    if (baseWidth > 0 && baseHeight > 0) {
      if (imgEl.offsetParent === stageEl) {
        baseCenter = {
          x: stageRect.left + stageEl.clientLeft + imgEl.offsetLeft + baseWidth / 2,
          y: stageRect.top + stageEl.clientTop + imgEl.offsetTop + baseHeight / 2,
        }
      } else {
        const imgRect = imgEl.getBoundingClientRect()
        baseCenter = {
          x: imgRect.left + imgRect.width / 2 - offset.value.x,
          y: imgRect.top + imgRect.height / 2 - offset.value.y,
        }
      }
    } else {
      const imgRect = imgEl.getBoundingClientRect()
      const currentScale = scale.value || 1
      const quarterTurn = Math.abs(Math.round(rotate.value / 90)) % 2 === 1
      baseWidth = (quarterTurn ? imgRect.height : imgRect.width) / currentScale
      baseHeight = (quarterTurn ? imgRect.width : imgRect.height) / currentScale
      baseCenter =
        imgRect.width > 0 && imgRect.height > 0
          ? {
              x: imgRect.left + imgRect.width / 2 - offset.value.x,
              y: imgRect.top + imgRect.height / 2 - offset.value.y,
            }
          : stageCenter
    }

    return {
      viewport,
      baseSize: { width: baseWidth, height: baseHeight },
      baseCenter,
      centerOffset: {
        x: baseCenter.x - stageCenter.x,
        y: baseCenter.y - stageCenter.y,
      },
    }
  }

  function reclamp() {
    offset.value = clampOffset(offset.value, scale.value, rotate.value, readMetrics())
  }

  function setWindowListeners(active: boolean) {
    if (typeof window === 'undefined' || windowListening === active) return
    windowListening = active
    if (active) {
      window.addEventListener('pointermove', onPointermove, { passive: false })
      window.addEventListener('pointerup', onPointerup)
      window.addEventListener('pointercancel', onPointercancel)
    } else {
      window.removeEventListener('pointermove', onPointermove)
      window.removeEventListener('pointerup', onPointerup)
      window.removeEventListener('pointercancel', onPointercancel)
    }
  }

  function cancelGestures() {
    const stageEl = options.stage.value
    for (const pointerId of pointers.keys()) {
      tryReleasePointerCapture(stageEl, pointerId)
    }
    pointers.clear()
    pinch = null
    drag = null
    tapCandidate = null
    wasMultiTouch = false
    interacting.value = false
    setWindowListeners(false)
  }

  function resetZoom() {
    cancelGestures()
    scale.value = 1
    offset.value = { x: 0, y: 0 }
  }

  function reset() {
    cancelGestures()
    scale.value = 1
    rotate.value = 0
    flip.value = { x: 1, y: 1 }
    offset.value = { x: 0, y: 0 }
    lastTouchTap = null
  }

  function zoomTo(nextScale: number, focalClient?: Point) {
    const targetScale = clampScale(nextScale, minScale(), maxScale())
    if (targetScale <= 1) {
      scale.value = targetScale
      offset.value = { x: 0, y: 0 }
      return
    }
    const metrics = readMetrics()
    const focal = focalClient ?? metrics.baseCenter
    const next = zoomAroundPoint(
      { scale: scale.value, offset: offset.value },
      targetScale,
      focal,
      focal,
      rotate.value,
      metrics,
    )
    scale.value = next.scale
    offset.value = next.offset
  }

  function zoomBy(direction: 1 | -1, focalClient?: Point) {
    zoomTo(stepScale(scale.value, direction), focalClient)
  }

  function toggleZoomAt(focalClient: Point) {
    if (scale.value <= 1.01) {
      zoomTo(doubleTapScale, focalClient)
    } else {
      scale.value = 1
      offset.value = { x: 0, y: 0 }
    }
  }

  function turn(degrees: number) {
    rotate.value += degrees
    reclamp()
  }

  function mirror(axis: 'x' | 'y') {
    flip.value = { ...flip.value, [axis]: -flip.value[axis] }
  }

  function startPinchFromPointers() {
    const active = [...pointers.values()]
    const first = active[0]
    const second = active[1]
    if (!first || !second) return
    pinch = {
      startDistance: Math.max(pointerDistance(first, second), 1),
      startMidpoint: pointerMidpoint(first, second),
      startScale: scale.value,
      startOffset: { ...offset.value },
      minPinchScale: Math.min(1, Math.max(minScale(), scale.value)),
    }
  }

  function onPointerdown(event: PointerEvent) {
    if (event.pointerType === 'mouse' && event.button !== 0) return
    const stageEl = options.stage.value
    if (!stageEl) return

    suppressNextMaskClick = false
    pointers.set(event.pointerId, { x: event.clientX, y: event.clientY })
    setWindowListeners(true)

    if (pointers.size === 1) {
      wasMultiTouch = false
      pinch = null
      const target = event.target as Node | null
      const isMask = target === stageEl
      const imgEl = options.image.value
      const isImage = !!imgEl && (target === imgEl || (!!target && imgEl.contains(target)))
      tapCandidate = {
        pointerId: event.pointerId,
        pointerType: event.pointerType,
        isMask,
        isImage,
        startX: event.clientX,
        startY: event.clientY,
        moved: false,
      }
      if (scale.value > 1) {
        drag = {
          pointerId: event.pointerId,
          startX: event.clientX,
          startY: event.clientY,
          startOffset: { ...offset.value },
          captured: false,
        }
      } else {
        drag = null
      }
      return
    }

    if (pointers.size >= 2) {
      wasMultiTouch = true
      tapCandidate = null
      lastTouchTap = null
      drag = null
      interacting.value = true
      for (const pointerId of pointers.keys()) {
        trySetPointerCapture(stageEl, pointerId)
      }
      startPinchFromPointers()
      event.preventDefault()
    }
  }

  function onPointermove(event: PointerEvent) {
    if (!pointers.has(event.pointerId)) return
    pointers.set(event.pointerId, { x: event.clientX, y: event.clientY })

    if (pinch && pointers.size >= 2) {
      const active = [...pointers.values()]
      const first = active[0]!
      const second = active[1]!
      const currentDistance = Math.max(pointerDistance(first, second), 1)
      const currentMidpoint = pointerMidpoint(first, second)
      const rawScale = pinch.startScale * (currentDistance / pinch.startDistance)
      const nextScale = clampScale(rawScale, pinch.minPinchScale, maxScale())
      const next = zoomAroundPoint(
        { scale: pinch.startScale, offset: pinch.startOffset },
        nextScale,
        pinch.startMidpoint,
        currentMidpoint,
        rotate.value,
        readMetrics(),
      )
      scale.value = next.scale
      offset.value = next.offset
      event.preventDefault()
      return
    }

    if (tapCandidate && tapCandidate.pointerId === event.pointerId) {
      if (
        Math.hypot(event.clientX - tapCandidate.startX, event.clientY - tapCandidate.startY) >
        TAP_MOVE_THRESHOLD
      ) {
        tapCandidate.moved = true
      }
    }

    if (drag && drag.pointerId === event.pointerId && scale.value > 1) {
      const dx = event.clientX - drag.startX
      const dy = event.clientY - drag.startY
      if (!drag.captured && Math.hypot(dx, dy) > DRAG_THRESHOLD) {
        drag.captured = true
        interacting.value = true
        trySetPointerCapture(options.stage.value, event.pointerId)
      }
      if (drag.captured) {
        offset.value = clampOffset(
          { x: drag.startOffset.x + dx, y: drag.startOffset.y + dy },
          scale.value,
          rotate.value,
          readMetrics(),
        )
        event.preventDefault()
      }
    }
  }

  function finishPointer(event: PointerEvent, cancelled: boolean) {
    if (!pointers.has(event.pointerId)) {
      if (pointers.size === 0) {
        interacting.value = false
        pinch = null
        drag = null
        setWindowListeners(false)
      }
      return
    }

    pointers.delete(event.pointerId)
    tryReleasePointerCapture(options.stage.value, event.pointerId)

    // Pinch -> single-finger pan handoff without jumping.
    if (pinch && pointers.size === 1) {
      pinch = null
      const entry = pointers.entries().next().value
      if (entry && scale.value > 1) {
        const [remainingId, remainingPoint] = entry
        drag = {
          pointerId: remainingId,
          startX: remainingPoint.x,
          startY: remainingPoint.y,
          startOffset: { ...offset.value },
          captured: true,
        }
        interacting.value = true
      } else {
        drag = null
        interacting.value = false
      }
      return
    }

    // 3+ fingers -> 2 fingers: re-anchor pinch smoothly.
    if (pinch && pointers.size >= 2) {
      startPinchFromPointers()
      return
    }

    if (pointers.size > 0) return

    const candidate = tapCandidate
    const hadMultiTouch = wasMultiTouch
    const hadDrag = !!drag?.captured

    pinch = null
    drag = null
    tapCandidate = null
    wasMultiTouch = false
    interacting.value = false
    setWindowListeners(false)
    reclamp()

    // Any completed pointer sequence owns its trailing click on the stage so edge slips never close the modal.
    suppressNextMaskClick = true

    if (hadMultiTouch || hadDrag || candidate?.moved) {
      return
    }

    if (cancelled || !candidate || candidate.pointerId !== event.pointerId) return

    if (candidate.isMask && event.target === options.stage.value && closeOnMask()) {
      lastTouchTap = null
      options.onMaskTap?.()
      return
    }

    if (candidate.pointerType !== 'mouse' && (candidate.isImage || !closeOnMask())) {
      const now = event.timeStamp > 0 ? event.timeStamp : Date.now()
      if (
        lastTouchTap &&
        now - lastTouchTap.time <= DOUBLE_TAP_MS &&
        Math.hypot(event.clientX - lastTouchTap.x, event.clientY - lastTouchTap.y) <=
          DOUBLE_TAP_DISTANCE
      ) {
        lastTouchTap = null
        lastTouchDoubleTapAt = Date.now()
        toggleZoomAt({ x: event.clientX, y: event.clientY })
      } else {
        lastTouchTap = { time: now, x: event.clientX, y: event.clientY }
      }
    }
  }

  function onPointerup(event: PointerEvent) {
    finishPointer(event, false)
  }

  function onPointercancel(event: PointerEvent) {
    finishPointer(event, true)
  }

  function onLostpointercapture(event: PointerEvent) {
    if (!pointers.has(event.pointerId)) return
    finishPointer(event, true)
  }

  function onStageClick(event: MouseEvent) {
    if (suppressNextMaskClick) {
      suppressNextMaskClick = false
      return
    }
    if (event.target === options.stage.value && closeOnMask()) {
      options.onMaskTap?.()
    }
  }

  function onDblclick(event: MouseEvent) {
    if (Date.now() - lastTouchDoubleTapAt < 500) return
    toggleZoomAt({ x: event.clientX, y: event.clientY })
  }

  function onWheel(event: WheelEvent) {
    event.preventDefault()
    if (event.deltaY === 0) return
    const focal: Point | undefined =
      Number.isFinite(event.clientX) && Number.isFinite(event.clientY)
        ? { x: event.clientX, y: event.clientY }
        : undefined
    if (event.ctrlKey) {
      const factor = Math.exp(-event.deltaY * 0.01)
      zoomTo(scale.value * factor, focal)
      return
    }
    const now = event.timeStamp > 0 ? event.timeStamp : performance.now()
    if (now - wheelAt < WHEEL_STEP_INTERVAL_MS) return
    wheelAt = now
    zoomBy(event.deltaY < 0 ? 1 : -1, focal)
  }

  function onTouchmove(event: TouchEvent) {
    // Belt-and-suspenders prevention for iOS Safari / WebViews where multi-touch can leak page zoom.
    if (event.touches.length > 1 || scale.value > 1) {
      event.preventDefault()
    }
  }

  watch(
    options.stage,
    (stageEl, _previous, cleanup) => {
      if (!stageEl) return
      stageEl.addEventListener('touchmove', onTouchmove, { passive: false })
      stageEl.addEventListener('gesturestart', preventGesture, { passive: false })
      stageEl.addEventListener('gesturechange', preventGesture, { passive: false })
      stageEl.addEventListener('gestureend', preventGesture, { passive: false })
      if (typeof window !== 'undefined') {
        window.addEventListener('resize', reclamp)
      }
      cleanup(() => {
        stageEl.removeEventListener('touchmove', onTouchmove)
        stageEl.removeEventListener('gesturestart', preventGesture)
        stageEl.removeEventListener('gesturechange', preventGesture)
        stageEl.removeEventListener('gestureend', preventGesture)
        if (typeof window !== 'undefined') {
          window.removeEventListener('resize', reclamp)
        }
      })
    },
    { immediate: true },
  )

  onScopeDispose(() => {
    cancelGestures()
  })

  return {
    scale,
    offset,
    rotate,
    flip,
    interacting,
    movable,
    transformStyle,
    reset,
    resetZoom,
    zoomTo,
    zoomBy,
    toggleZoomAt,
    turn,
    mirror,
    reclamp,
    cancelGestures,
    stageListeners: {
      onPointerdown,
      onLostpointercapture,
      onClick: onStageClick,
      onWheel,
    },
    imageListeners: {
      onDblclick,
      onLoad: reclamp,
    },
  }
}
