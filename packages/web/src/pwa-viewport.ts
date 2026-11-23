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

/**
 * iPhone Duo outer display dimensions in CSS points (measured on iOS 27.x).
 * WebKit lays pages out beside the 84pt side column (382pt portrait / 594pt landscape)
 * and reports safe-area-inset-right: 0px even though the WKWebView covers the full display.
 */
export const DUO_FRONT_SHORT_SIDE = 466
export const DUO_FRONT_LONG_SIDE = 678
export const DUO_FRONT_COLUMN_WIDTH = 84
export const DUO_FRONT_MAX_SIDE = 700

export type DuoFrontWidenPosture = 'portrait' | 'landscape-secondary'

export interface DuoFrontWidenState {
  /** True when explicitly widening the layout viewport across iPhone Duo's right 84pt strip. */
  active: boolean
  posture: DuoFrontWidenPosture | null
  targetWidth: number | null
  rightInset: number
  compactHeight: boolean
}

export interface DuoFrontWidenInput {
  standalone: boolean
  screenWidth: number
  screenHeight: number
  innerWidth: number
  innerHeight: number
  visualScale?: number | null
  orientationType?: string | null
  nativeRightInset?: number
  nativeLeftInset?: number
  alreadyWidened?: boolean
}

const INACTIVE_WIDEN: DuoFrontWidenState = {
  active: false,
  posture: null,
  targetWidth: null,
  rightInset: 0,
  compactHeight: false,
}

export function sanitizeVisualScale(scale: number | null | undefined): number {
  return typeof scale === 'number' && Number.isFinite(scale) && scale >= 0.5 && scale <= 3
    ? scale
    : 1
}

export function buildDuoFrontViewportContent(
  targetWidth: number,
  visualScale?: number | null,
): string {
  const scale = sanitizeVisualScale(visualScale)
  const width = Math.round((targetWidth / scale) * 100) / 100
  return `width=${width}, height=device-height, initial-scale=1.0, minimum-scale=1.0, maximum-scale=1.0, user-scalable=no, viewport-fit=cover`
}

function nearly(a: number, b: number, tolerance = 1.5): boolean {
  return Math.abs(a - b) <= tolerance
}

/**
 * Determines whether to apply the iPhone Duo outer-display viewport widening workaround.
 *
 * Only activates in standalone PWA mode when:
 * - WebKit does not already expose native side safe-area insets (>= 64px)
 * - The device is on the Duo outer screen (466x678), never the unfolded inner display (669x951)
 * - The 84pt system column sits on the right edge (portrait or landscape-secondary),
 *   skipping landscape-primary where the column sits on the left and needs horizontal scroll offset.
 */
export function resolveDuoFrontWiden(input: DuoFrontWidenInput): DuoFrontWidenState {
  const {
    standalone,
    screenWidth,
    screenHeight,
    innerWidth,
    innerHeight,
    visualScale,
    orientationType,
    nativeRightInset = 0,
    nativeLeftInset = 0,
    alreadyWidened = false,
  } = input

  if (!standalone) return INACTIVE_WIDEN
  if (nativeRightInset >= DUO_RIGHT_INSET_MIN || nativeLeftInset >= DUO_RIGHT_INSET_MIN) {
    return INACTIVE_WIDEN
  }

  const scale = sanitizeVisualScale(visualScale)
  const hasViewport = innerWidth > 0 && innerHeight > 0
  const cssWidth = hasViewport ? innerWidth * scale : 0
  const cssHeight = hasViewport ? innerHeight * scale : 0

  // Inner display (669x951 / 951x669) always exceeds 700pt on its long edge, even if
  // screen.width stays stale at 466 after unfolding without a reload.
  if (hasViewport && Math.max(cssWidth, cssHeight) > DUO_FRONT_MAX_SIDE) {
    return INACTIVE_WIDEN
  }

  if (orientationType === 'landscape-primary') return INACTIVE_WIDEN

  let posture: DuoFrontWidenPosture | null = null
  if (orientationType?.startsWith('portrait')) {
    posture = 'portrait'
  } else if (orientationType === 'landscape-secondary') {
    posture = 'landscape-secondary'
  } else if (!orientationType && (!hasViewport || cssHeight >= cssWidth)) {
    posture = 'portrait'
  }
  if (!posture) return INACTIVE_WIDEN

  const isPortrait = posture === 'portrait'
  const targetWidth = isPortrait ? DUO_FRONT_SHORT_SIDE : DUO_FRONT_LONG_SIDE
  const unwidenedWidth = targetWidth - DUO_FRONT_COLUMN_WIDTH

  const screenShort = Math.min(screenWidth, screenHeight)
  const screenLong = Math.max(screenWidth, screenHeight)
  const matchesScreen =
    screenShort > 0 &&
    nearly(screenShort, DUO_FRONT_SHORT_SIDE, 2) &&
    nearly(screenLong, DUO_FRONT_LONG_SIDE, 2)

  const portraitUnwidenedWidth = DUO_FRONT_SHORT_SIDE - DUO_FRONT_COLUMN_WIDTH
  const matchesPortraitWidthForTransition =
    nearly(cssWidth, DUO_FRONT_SHORT_SIDE, 2) || nearly(cssWidth, portraitUnwidenedWidth, 2)

  let matchesOuterDisplay = false
  if (matchesScreen) {
    if (!hasViewport) {
      matchesOuterDisplay = true
    } else if (isPortrait) {
      // Allow shorter heights when the virtual keyboard is open, while still excluding
      // an unfolded inner-portrait viewport (669pt wide) if screen.width stayed stale.
      matchesOuterDisplay =
        cssHeight <= 690 &&
        (cssWidth <= DUO_FRONT_SHORT_SIDE + 4 ||
          (alreadyWidened && nearly(cssWidth, DUO_FRONT_LONG_SIDE, 2)))
    } else {
      // Also allow the portrait -> landscape-secondary transient frame where screen.orientation
      // flips before innerHeight shrinks from 678pt down to 466pt.
      matchesOuterDisplay =
        cssHeight <= 480 ||
        (alreadyWidened && cssHeight <= 690 && matchesPortraitWidthForTransition)
    }
  } else if (hasViewport) {
    // Fallback when Advanced Fingerprinting Protection spoofs screen.width/height:
    // no other iPhone reports a 382pt or 594pt viewport at Duo's outer-screen height.
    if (alreadyWidened) {
      const knownOuterWidth =
        nearly(cssWidth, unwidenedWidth) ||
        nearly(cssWidth, DUO_FRONT_SHORT_SIDE, 2) ||
        nearly(cssWidth, DUO_FRONT_LONG_SIDE, 2)
      const validWidenedHeight = isPortrait
        ? cssHeight <= 690
        : cssHeight <= 480 || (cssHeight <= 690 && matchesPortraitWidthForTransition)
      matchesOuterDisplay = knownOuterWidth && validWidenedHeight
    } else {
      const heightMatches = isPortrait
        ? cssHeight > 500 && cssHeight <= 690
        : cssHeight > 300 && cssHeight <= 480
      matchesOuterDisplay = heightMatches && nearly(cssWidth, unwidenedWidth)
    }
  }

  if (!matchesOuterDisplay) return INACTIVE_WIDEN

  return {
    active: true,
    posture,
    targetWidth,
    rightInset: DUO_FRONT_COLUMN_WIDTH,
    compactHeight: posture === 'landscape-secondary',
  }
}

/**
 * Inline snippet injected into `<head>` right after `<meta name="viewport">` so iPhone Duo's
 * very first standalone PWA layout is already widened to 466/678pt (WebKit otherwise keeps
 * the page clipped at 382/594pt across reloads if first layout ran narrow).
 */
export function duoFrontViewportScript(): string {
  return `(()=>{try{var r=document.documentElement,a=r.getAttribute('data-pwa-simulate-standalone'),s=a!==null?a!=='false':(matchMedia('(display-mode: standalone)').matches||navigator.standalone===true);if(!s)return;r.dataset.pwaStandalone='true';var m=document.querySelector('meta[name="viewport"]');if(!m)return;var vv=window.visualViewport,z=vv&&vv.scale>=0.5&&vv.scale<=3?vv.scale:1,iw=window.innerWidth,ih=window.innerHeight,hv=iw>0&&ih>0,cw=hv?iw*z:0,ch=hv?ih*z:0;if(hv&&Math.max(cw,ch)>${DUO_FRONT_MAX_SIDE}){m.content='${STANDALONE_VIEWPORT_CONTENT}';return;}var ot=screen.orientation&&screen.orientation.type,p=ot==='landscape-primary'?null:ot&&ot.indexOf('portrait')===0?'portrait':ot==='landscape-secondary'?'landscape-secondary':!ot&&(!hv||ch>=cw)?'portrait':null;if(!p){m.content='${STANDALONE_VIEWPORT_CONTENT}';return;}var sw=Math.min(screen.width||0,screen.height||0),sl=Math.max(screen.width||0,screen.height||0),ms=sw>0&&Math.abs(sw-${DUO_FRONT_SHORT_SIDE})<=2&&Math.abs(sl-${DUO_FRONT_LONG_SIDE})<=2,ip=p==='portrait',tw=ip?${DUO_FRONT_SHORT_SIDE}:${DUO_FRONT_LONG_SIDE},uw=tw-${DUO_FRONT_COLUMN_WIDTH},ok=ms?(!hv||(ip?ch<=690&&cw<=${DUO_FRONT_SHORT_SIDE + 4}:ch<=480)):(hv&&(ip?ch>500&&ch<=690:ch>300&&ch<=480)&&Math.abs(cw-uw)<=1.5);if(ok){var w=Math.round((tw/z)*100)/100;m.content='width='+w+', height=device-height, initial-scale=1.0, minimum-scale=1.0, maximum-scale=1.0, user-scalable=no, viewport-fit=cover';r.dataset.duoFrontWiden=p;}else{m.content='${STANDALONE_VIEWPORT_CONTENT}';}}catch{}})();`
}
