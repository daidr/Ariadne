import { computed, onScopeDispose, reactive, ref, type ComputedRef, type InjectionKey } from 'vue'
import type { MainView, WorkspaceLevel } from './store'
import type { WorkspaceLayoutMode } from './workspace-layout'

export interface SafeAreaInsets {
  top: number
  right: number
  bottom: number
  left: number
}

export interface DuoRightRailState {
  /** True in standalone PWA mode when the right edge owns a wide asymmetric safe-area strip. */
  active: boolean
  /**
   * True in short landscape postures (e.g. folded iPhone Duo landscape-left, 678x466)
   * where the status/camera cutout moves to the bottom-right corner instead of top-right.
   */
  compactHeight: boolean
}

export type DevicePostureType = 'continuous' | 'folded'
export type FoldMode = 'none' | 'horizontal' | 'vertical'

export interface ViewportSegmentRect {
  left: number
  top: number
  right: number
  bottom: number
  width: number
  height: number
}

export interface FoldLayoutState {
  /** True when Device Posture API or Viewport Segments API is exposed by the browser. */
  supported: boolean
  posture: DevicePostureType
  mode: FoldMode
  /** Physical hinge width/height in CSS pixels between the two segments. */
  hingeGap: number
  /**
   * Percentage inside the safe content width that places a horizontal splitter at the
   * physical hinge centre, or null when not in a horizontal book fold.
   */
  horizontalSplitPercent: number | null
  /** Distance from viewport top to the upper edge of a horizontal crease (vertical segments). */
  verticalTopHeight: number | null
  /** Distance from the lower edge of a horizontal crease to viewport bottom. */
  verticalBottomHeight: number | null
}

export type DuoBackAction = 'main' | 'cancel-new' | 'to-coordinator' | 'to-projects' | null

export const foldLayoutKey: InjectionKey<ComputedRef<FoldLayoutState>> = Symbol('ariadne.fold')

export const DEFAULT_VIEWPORT_CONTENT = 'width=device-width, initial-scale=1.0, viewport-fit=cover'
export const STANDALONE_VIEWPORT_CONTENT =
  'width=device-width, initial-scale=1.0, maximum-scale=1.0, user-scalable=no, viewport-fit=cover'

/**
 * iPhone Duo reserves ~84pt on one vertical edge when folded upright or unfolded sideways,
 * whereas standard iPhones in landscape report symmetric ~47-59pt left/right insets.
 */
export const DUO_RIGHT_INSET_MIN = 64
export const DUO_RIGHT_ASYMMETRY_MIN = 40
export const DUO_COMPACT_HEIGHT_MAX = 520

interface DevicePostureLike extends EventTarget {
  readonly type: DevicePostureType
}

interface NavigatorWithPwaApis extends Navigator {
  standalone?: boolean
  devicePosture?: DevicePostureLike
}

interface ViewportWithSegments {
  readonly segments?: readonly DOMRectReadOnly[] | null
}

interface WindowWithPwaApis extends Window {
  viewport?: ViewportWithSegments
}

export function isStandalonePwa(
  win: Pick<Window, 'matchMedia' | 'navigator'> | undefined = typeof window !== 'undefined'
    ? window
    : undefined,
  doc: Pick<Document, 'documentElement'> | undefined = typeof document !== 'undefined'
    ? document
    : undefined,
): boolean {
  if (doc?.documentElement?.hasAttribute('data-pwa-simulate-standalone')) {
    return doc.documentElement.getAttribute('data-pwa-simulate-standalone') !== 'false'
  }
  if (!win) return false
  if (win.matchMedia?.('(display-mode: standalone)').matches) return true
  return (win.navigator as NavigatorWithPwaApis | undefined)?.standalone === true
}

export function syncStandaloneViewport(
  standalone: boolean,
  doc: Pick<Document, 'documentElement' | 'querySelector'> = document,
): string {
  const root = doc.documentElement
  if (standalone) root.dataset.pwaStandalone = 'true'
  else delete root.dataset.pwaStandalone

  const next = standalone ? STANDALONE_VIEWPORT_CONTENT : DEFAULT_VIEWPORT_CONTENT
  const meta = doc.querySelector<HTMLMetaElement>('meta[name="viewport"]')
  if (meta && meta.content !== next) {
    meta.content = next
  }
  return next
}

export function resolveDuoRightRail(options: {
  standalone: boolean
  insets: SafeAreaInsets
  viewportWidth: number
  viewportHeight: number
}): DuoRightRailState {
  const { standalone, insets, viewportWidth, viewportHeight } = options
  const active =
    standalone &&
    insets.right >= DUO_RIGHT_INSET_MIN &&
    insets.right - insets.left >= DUO_RIGHT_ASYMMETRY_MIN
  return {
    active,
    compactHeight:
      active &&
      viewportWidth > viewportHeight &&
      viewportHeight > 0 &&
      viewportHeight < DUO_COMPACT_HEIGHT_MAX,
  }
}

export function resolveFoldLayout(options: {
  supported: boolean
  posture: DevicePostureType
  segments: readonly ViewportSegmentRect[]
  insets: SafeAreaInsets
  viewportWidth: number
  viewportHeight: number
}): FoldLayoutState {
  const { supported, posture, segments, insets, viewportWidth, viewportHeight } = options
  const none: FoldLayoutState = {
    supported,
    posture,
    mode: 'none',
    hingeGap: 0,
    horizontalSplitPercent: null,
    verticalTopHeight: null,
    verticalBottomHeight: null,
  }
  if (!supported || posture !== 'folded' || segments.length < 2) return none

  const byLeft = segments.toSorted((a, b) => a.left - b.left)
  const leftSeg = byLeft[0]!
  const rightSeg = byLeft[byLeft.length - 1]!
  const isHorizontal =
    rightSeg.left >= leftSeg.right - 1 && Math.abs(leftSeg.top - rightSeg.top) <= 4
  if (isHorizontal && viewportWidth > 0) {
    const hingeStart = leftSeg.right
    const hingeEnd = rightSeg.left
    const hingeGap = Math.max(0, Math.round(hingeEnd - hingeStart))
    const hingeCenter = (hingeStart + hingeEnd) / 2
    const contentWidth = Math.max(1, viewportWidth - insets.left - insets.right)
    const rawPercent = ((hingeCenter - insets.left) / contentWidth) * 100
    if (rawPercent < 25 || rawPercent > 75) return none
    return {
      supported,
      posture,
      mode: 'horizontal',
      hingeGap,
      horizontalSplitPercent: Math.round(rawPercent * 100) / 100,
      verticalTopHeight: null,
      verticalBottomHeight: null,
    }
  }

  const byTop = segments.toSorted((a, b) => a.top - b.top)
  const topSeg = byTop[0]!
  const bottomSeg = byTop[byTop.length - 1]!
  const isVertical =
    bottomSeg.top >= topSeg.bottom - 1 && Math.abs(topSeg.left - bottomSeg.left) <= 4
  if (isVertical && viewportHeight > 0) {
    const hingeStart = topSeg.bottom
    const hingeEnd = bottomSeg.top
    const hingeGap = Math.max(0, Math.round(hingeEnd - hingeStart))
    const verticalTopHeight = Math.max(0, Math.round(topSeg.bottom))
    const verticalBottomHeight = Math.max(0, Math.round(viewportHeight - bottomSeg.top))
    if (verticalTopHeight < 120 || verticalBottomHeight < 120) return none
    return {
      supported,
      posture,
      mode: 'vertical',
      hingeGap,
      horizontalSplitPercent: null,
      verticalTopHeight,
      verticalBottomHeight,
    }
  }

  return none
}

export function readNativeFoldInput(
  win: Pick<Window, 'matchMedia' | 'navigator' | 'innerWidth' | 'innerHeight'> & {
    viewport?: ViewportWithSegments
  },
): {
  supported: boolean
  posture: DevicePostureType
  segments: ViewportSegmentRect[]
  viewportWidth: number
  viewportHeight: number
} {
  const nav = win.navigator as NavigatorWithPwaApis | undefined
  const viewportWidth = win.innerWidth
  const viewportHeight = win.innerHeight

  const postureFromJs = nav?.devicePosture?.type
  const postureFoldedQuery = win.matchMedia?.('(device-posture: folded)')?.matches === true
  const postureContinuousQuery = win.matchMedia?.('(device-posture: continuous)')?.matches === true
  const hasPostureApi =
    postureFromJs === 'folded' ||
    postureFromJs === 'continuous' ||
    postureFoldedQuery ||
    postureContinuousQuery

  const rawSegments = win.viewport?.segments
  let segments: ViewportSegmentRect[] = []
  let hasSegmentsApi = false

  if (rawSegments && rawSegments.length > 0) {
    hasSegmentsApi = true
    segments = Array.from(rawSegments, (rect) => ({
      left: rect.left,
      top: rect.top,
      right: rect.right,
      bottom: rect.bottom,
      width: rect.width,
      height: rect.height,
    }))
  } else {
    const horizontal2 = win.matchMedia?.('(horizontal-viewport-segments: 2)')?.matches === true
    const vertical2 = win.matchMedia?.('(vertical-viewport-segments: 2)')?.matches === true
    if (horizontal2 && viewportWidth > 0 && viewportHeight > 0) {
      hasSegmentsApi = true
      const half = viewportWidth / 2
      segments = [
        {
          left: 0,
          top: 0,
          right: half,
          bottom: viewportHeight,
          width: half,
          height: viewportHeight,
        },
        {
          left: half,
          top: 0,
          right: viewportWidth,
          bottom: viewportHeight,
          width: half,
          height: viewportHeight,
        },
      ]
    } else if (vertical2 && viewportWidth > 0 && viewportHeight > 0) {
      hasSegmentsApi = true
      const half = viewportHeight / 2
      segments = [
        { left: 0, top: 0, right: viewportWidth, bottom: half, width: viewportWidth, height: half },
        {
          left: 0,
          top: half,
          right: viewportWidth,
          bottom: viewportHeight,
          width: viewportWidth,
          height: half,
        },
      ]
    }
  }

  // Respect explicit Device Posture API when exposed; otherwise treat two or more viewport
  // segments (e.g. Safari 27.1+ where window.viewport.segments exists without devicePosture)
  // as a folded posture so hinge-aware layout activates automatically.
  const posture: DevicePostureType = hasPostureApi
    ? postureFromJs === 'folded' || postureFoldedQuery
      ? 'folded'
      : 'continuous'
    : segments.length >= 2
      ? 'folded'
      : 'continuous'

  return {
    supported: hasPostureApi || hasSegmentsApi,
    posture,
    segments,
    viewportWidth,
    viewportHeight,
  }
}

function setOrRemoveProperty(style: CSSStyleDeclaration, name: string, value: string | null): void {
  const current = style.getPropertyValue(name)
  if (value === null) {
    if (current) style.removeProperty(name)
  } else if (current !== value) {
    style.setProperty(name, value)
  }
}

export function syncFoldCssState(
  fold: FoldLayoutState,
  doc: Pick<Document, 'documentElement'> = document,
): void {
  const root = doc.documentElement
  if (fold.supported) {
    if (root.dataset.devicePosture !== fold.posture) root.dataset.devicePosture = fold.posture
  } else if ('devicePosture' in root.dataset) {
    delete root.dataset.devicePosture
  }

  if (fold.mode !== 'none') {
    if (root.dataset.foldMode !== fold.mode) root.dataset.foldMode = fold.mode
  } else if ('foldMode' in root.dataset) {
    delete root.dataset.foldMode
  }

  setOrRemoveProperty(root.style, '--ariadne-fold-gap', `${fold.hingeGap}px`)
  setOrRemoveProperty(
    root.style,
    '--ariadne-fold-top-height',
    fold.verticalTopHeight !== null ? `${fold.verticalTopHeight}px` : null,
  )
  setOrRemoveProperty(
    root.style,
    '--ariadne-fold-bottom-height',
    fold.verticalBottomHeight !== null ? `${fold.verticalBottomHeight}px` : null,
  )
}

export function resolveDuoBackAction(options: {
  mainKind: MainView['kind']
  starting: boolean
  hasCurrentProject: boolean
  layoutMode: WorkspaceLayoutMode
  workspaceLevel: WorkspaceLevel
}): DuoBackAction {
  const { mainKind, starting, hasCurrentProject, layoutMode, workspaceLevel } = options
  if (layoutMode === 'narrow') {
    if (workspaceLevel === 'projects') return null
    if (workspaceLevel === 'side') return 'to-coordinator'
  }
  if (mainKind === 'trace') return 'main'
  if (starting && hasCurrentProject) return 'cancel-new'
  if (mainKind !== 'conversation') return 'main'
  if (layoutMode === 'narrow') return 'to-projects'
  return null
}

function parsePixels(value: string): number {
  const parsed = Number.parseFloat(value)
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0
}

interface SafeAreaProbes {
  topRight: HTMLDivElement
  bottomLeft: HTMLDivElement
  read(win: Window): SafeAreaInsets
  remove(): void
}

function createProbeElement(doc: Document, extraStyles: readonly string[]): HTMLDivElement {
  const probe = doc.createElement('div')
  probe.setAttribute('aria-hidden', 'true')
  probe.style.cssText = [
    'position:fixed',
    'top:0',
    'left:0',
    'box-sizing:content-box',
    'width:1px',
    'height:1px',
    'visibility:hidden',
    'pointer-events:none',
    ...extraStyles,
  ].join(';')
  doc.body.appendChild(probe)
  return probe
}

/**
 * Splits safe-area probing across two opposite-corner elements so border-box
 * ResizeObserver catches asynchronous WebKit inset population after launch as
 * well as left<->right or top<->bottom swaps where summed padding stays equal.
 */
function createSafeAreaProbes(doc: Document): SafeAreaProbes | null {
  if (!doc.body) return null
  const topRight = createProbeElement(doc, [
    'padding-top:var(--ariadne-safe-top, env(safe-area-inset-top, 0px))',
    'padding-right:var(--ariadne-safe-right, env(safe-area-inset-right, 0px))',
  ])
  const bottomLeft = createProbeElement(doc, [
    'padding-bottom:var(--ariadne-safe-bottom, env(safe-area-inset-bottom, 0px))',
    'padding-left:var(--ariadne-safe-left, env(safe-area-inset-left, 0px))',
  ])
  return {
    topRight,
    bottomLeft,
    read(win) {
      const tr = win.getComputedStyle(topRight)
      const bl = win.getComputedStyle(bottomLeft)
      return {
        top: parsePixels(tr.paddingTop),
        right: parsePixels(tr.paddingRight),
        bottom: parsePixels(bl.paddingBottom),
        left: parsePixels(bl.paddingLeft),
      }
    },
    remove() {
      topRight.remove()
      bottomLeft.remove()
    },
  }
}

/**
 * Tracks PWA standalone mode, live safe-area insets, iPhone Duo right-rail geometry, and
 * experimental foldable APIs (Device Posture + Viewport Segments), while tightening
 * page-level pinch zoom in standalone windows without interfering with custom viewports.
 */
export function usePwaEnvironment() {
  const standalone = ref(isStandalonePwa())
  const insets = reactive<SafeAreaInsets>({ top: 0, right: 0, bottom: 0, left: 0 })
  const viewportWidth = ref(typeof window !== 'undefined' ? window.innerWidth : 0)
  const viewportHeight = ref(typeof window !== 'undefined' ? window.innerHeight : 0)
  const foldSupported = ref(false)
  const foldPosture = ref<DevicePostureType>('continuous')
  const foldSegments = ref<ViewportSegmentRect[]>([])

  const duoRail = computed(() =>
    resolveDuoRightRail({
      standalone: standalone.value,
      insets,
      viewportWidth: viewportWidth.value,
      viewportHeight: viewportHeight.value,
    }),
  )

  const fold = computed<FoldLayoutState>(() =>
    resolveFoldLayout({
      supported: foldSupported.value,
      posture: foldPosture.value,
      segments: foldSegments.value,
      insets,
      viewportWidth: viewportWidth.value,
      viewportHeight: viewportHeight.value,
    }),
  )

  if (typeof window !== 'undefined' && typeof document !== 'undefined') {
    const win = window as WindowWithPwaApis
    const probes = createSafeAreaProbes(document)

    let measuring = false
    let rafId: number | null = null
    const measure = () => {
      if (measuring) return
      measuring = true
      try {
        const nextStandalone = isStandalonePwa(win, document)
        standalone.value = nextStandalone
        syncStandaloneViewport(nextStandalone, document)

        if (probes) {
          const nextInsets = probes.read(win)
          insets.top = nextInsets.top
          insets.right = nextInsets.right
          insets.bottom = nextInsets.bottom
          insets.left = nextInsets.left
        }

        const foldInput = readNativeFoldInput(win)
        viewportWidth.value = foldInput.viewportWidth
        viewportHeight.value = foldInput.viewportHeight
        foldSupported.value = foldInput.supported
        foldPosture.value = foldInput.posture
        foldSegments.value = foldInput.segments
        syncFoldCssState(fold.value, document)
      } finally {
        measuring = false
      }
    }

    const scheduleMeasure = () => {
      if (rafId !== null) return
      if (typeof win.requestAnimationFrame === 'function') {
        rafId = win.requestAnimationFrame(() => {
          rafId = null
          measure()
        })
      } else {
        measure()
      }
    }

    const onOrientationChange = () => {
      measure()
      scheduleMeasure()
    }

    const onVisibilityChange = () => {
      if (document.visibilityState === 'visible') {
        measure()
        scheduleMeasure()
      }
    }

    const blockStandaloneGestureZoom = (event: Event) => {
      if (standalone.value) event.preventDefault()
    }

    measure()
    // Re-read on the next animation frame because iOS standalone PWAs often populate
    // env(safe-area-inset-*) asynchronously after first paint or viewport meta updates.
    scheduleMeasure()

    const mediaQueries = [
      win.matchMedia?.('(display-mode: standalone)'),
      win.matchMedia?.('(device-posture: folded)'),
      win.matchMedia?.('(horizontal-viewport-segments: 2)'),
      win.matchMedia?.('(vertical-viewport-segments: 2)'),
    ].filter((query): query is MediaQueryList => !!query)

    for (const query of mediaQueries) {
      query.addEventListener?.('change', measure)
    }

    const postureApi = (win.navigator as NavigatorWithPwaApis | undefined)?.devicePosture
    postureApi?.addEventListener?.('change', measure)

    win.addEventListener('resize', measure, { passive: true })
    win.addEventListener('orientationchange', onOrientationChange, { passive: true })
    win.addEventListener('pageshow', scheduleMeasure, { passive: true })
    document.addEventListener('visibilitychange', onVisibilityChange)
    document.addEventListener('gesturestart', blockStandaloneGestureZoom, { passive: false })

    const resizeObserver =
      probes && typeof ResizeObserver !== 'undefined'
        ? new ResizeObserver(() => {
            measure()
          })
        : null
    if (probes && resizeObserver) {
      resizeObserver.observe(probes.topRight, { box: 'border-box' })
      resizeObserver.observe(probes.bottomLeft, { box: 'border-box' })
    }

    const observer =
      typeof MutationObserver !== 'undefined'
        ? new MutationObserver(() => {
            measure()
          })
        : null
    observer?.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['style', 'data-pwa-simulate-standalone'],
    })

    onScopeDispose(() => {
      if (rafId !== null) {
        win.cancelAnimationFrame?.(rafId)
        rafId = null
      }
      for (const query of mediaQueries) {
        query.removeEventListener?.('change', measure)
      }
      postureApi?.removeEventListener?.('change', measure)
      win.removeEventListener('resize', measure)
      win.removeEventListener('orientationchange', onOrientationChange)
      win.removeEventListener('pageshow', scheduleMeasure)
      document.removeEventListener('visibilitychange', onVisibilityChange)
      document.removeEventListener('gesturestart', blockStandaloneGestureZoom)
      resizeObserver?.disconnect()
      observer?.disconnect()
      probes?.remove()
    })
  }

  return {
    standalone,
    insets,
    duoRail,
    fold,
  }
}
