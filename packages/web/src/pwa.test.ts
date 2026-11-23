// @vitest-environment happy-dom
import { effectScope } from 'vue'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import {
  DEFAULT_VIEWPORT_CONTENT,
  STANDALONE_VIEWPORT_CONTENT,
  buildDuoFrontViewportContent,
  duoFrontViewportScript,
  isStandalonePwa,
  readNativeFoldInput,
  resolveDuoBackAction,
  resolveDuoFrontWiden,
  resolveDuoRightRail,
  resolveFoldLayout,
  sanitizeVisualScale,
  syncFoldCssState,
  syncStandaloneViewport,
  usePwaEnvironment,
} from './pwa'

describe('isStandalonePwa', () => {
  afterEach(() => {
    document.documentElement.removeAttribute('data-pwa-simulate-standalone')
  })

  test('detects display-mode: standalone media query', () => {
    const win = {
      matchMedia: vi.fn().mockReturnValue({ matches: true }),
      navigator: {} as Navigator,
    }
    expect(isStandalonePwa(win, document)).toBe(true)
  })

  test('detects iOS navigator.standalone', () => {
    const win = {
      matchMedia: vi.fn().mockReturnValue({ matches: false }),
      navigator: { standalone: true } as unknown as Navigator,
    }
    expect(isStandalonePwa(win, document)).toBe(true)
  })

  test('returns false in regular browser tabs', () => {
    const win = {
      matchMedia: vi.fn().mockReturnValue({ matches: false }),
      navigator: { standalone: false } as unknown as Navigator,
    }
    expect(isStandalonePwa(win, document)).toBe(false)
  })

  test('honors explicit simulation attribute', () => {
    const win = {
      matchMedia: vi.fn().mockReturnValue({ matches: false }),
      navigator: {} as Navigator,
    }
    document.documentElement.setAttribute('data-pwa-simulate-standalone', 'true')
    expect(isStandalonePwa(win, document)).toBe(true)

    document.documentElement.setAttribute('data-pwa-simulate-standalone', 'false')
    expect(isStandalonePwa(win, document)).toBe(false)
  })
})

describe('syncStandaloneViewport', () => {
  let meta: HTMLMetaElement

  beforeEach(() => {
    meta = document.createElement('meta')
    meta.name = 'viewport'
    meta.content = DEFAULT_VIEWPORT_CONTENT
    document.head.appendChild(meta)
  })

  afterEach(() => {
    meta.remove()
    delete document.documentElement.dataset.pwaStandalone
  })

  test('locks viewport scaling only in standalone mode and restores it outside standalone', () => {
    expect(syncStandaloneViewport(true, document)).toBe(STANDALONE_VIEWPORT_CONTENT)
    expect(meta.content).toBe(STANDALONE_VIEWPORT_CONTENT)
    expect(document.documentElement.dataset.pwaStandalone).toBe('true')

    expect(syncStandaloneViewport(false, document)).toBe(DEFAULT_VIEWPORT_CONTENT)
    expect(meta.content).toBe(DEFAULT_VIEWPORT_CONTENT)
    expect(document.documentElement.dataset.pwaStandalone).toBeUndefined()
  })

  test('applies and clears iPhone Duo front-screen widened viewport cleanly', () => {
    const widened = syncStandaloneViewport(true, document, {
      duoFrontWiden: {
        active: true,
        posture: 'portrait',
        targetWidth: 466,
        rightInset: 84,
        compactHeight: false,
      },
    })
    expect(widened).toBe(buildDuoFrontViewportContent(466))
    expect(meta.content).toBe(
      'width=466, height=device-height, initial-scale=1.0, minimum-scale=1.0, maximum-scale=1.0, user-scalable=no, viewport-fit=cover',
    )
    expect(document.documentElement.dataset.duoFrontWiden).toBe('portrait')

    const restored = syncStandaloneViewport(true, document, {
      duoFrontWiden: {
        active: false,
        posture: null,
        targetWidth: null,
        rightInset: 0,
        compactHeight: false,
      },
    })
    expect(restored).toBe(STANDALONE_VIEWPORT_CONTENT)
    expect(meta.content).toBe(STANDALONE_VIEWPORT_CONTENT)
    expect(document.documentElement.dataset.duoFrontWiden).toBeUndefined()
  })
})

describe('resolveDuoFrontWiden', () => {
  test('activates for iPhone Duo outer display portrait and landscape-secondary in standalone PWA', () => {
    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 466,
        screenHeight: 678,
        innerWidth: 382,
        innerHeight: 678,
        orientationType: 'portrait-primary',
      }),
    ).toEqual({
      active: true,
      posture: 'portrait',
      targetWidth: 466,
      rightInset: 84,
      compactHeight: false,
    })

    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 466,
        screenHeight: 678,
        innerWidth: 594,
        innerHeight: 466,
        orientationType: 'landscape-secondary',
      }),
    ).toEqual({
      active: true,
      posture: 'landscape-secondary',
      targetWidth: 678,
      rightInset: 84,
      compactHeight: true,
    })
  })

  test('stays inactive outside standalone, in landscape-primary, and when native safe-area insets exist', () => {
    expect(
      resolveDuoFrontWiden({
        standalone: false,
        screenWidth: 466,
        screenHeight: 678,
        innerWidth: 382,
        innerHeight: 678,
        orientationType: 'portrait-primary',
      }).active,
    ).toBe(false)

    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 466,
        screenHeight: 678,
        innerWidth: 594,
        innerHeight: 466,
        orientationType: 'landscape-primary',
      }).active,
    ).toBe(false)

    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 466,
        screenHeight: 678,
        innerWidth: 466,
        innerHeight: 678,
        orientationType: 'portrait-primary',
        nativeRightInset: 84,
      }).active,
    ).toBe(false)

    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 466,
        screenHeight: 678,
        innerWidth: 678,
        innerHeight: 466,
        orientationType: 'landscape-secondary',
        nativeLeftInset: 84,
      }).active,
    ).toBe(false)
  })

  test('disables on unfolded inner display even when screen.width stays stale at 466', () => {
    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 466,
        screenHeight: 678,
        innerWidth: 951,
        innerHeight: 669,
        orientationType: 'landscape-secondary',
        alreadyWidened: true,
      }).active,
    ).toBe(false)

    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 466,
        screenHeight: 678,
        innerWidth: 669,
        innerHeight: 951,
        orientationType: 'portrait-primary',
        alreadyWidened: true,
      }).active,
    ).toBe(false)

    // Unfolded inner portrait with virtual keyboard open and stale screen size must stay inactive.
    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 466,
        screenHeight: 678,
        innerWidth: 669,
        innerHeight: 450,
        orientationType: 'portrait-primary',
        alreadyWidened: true,
      }).active,
    ).toBe(false)
  })

  test('stays active with virtual keyboard open and across portrait <-> landscape-secondary rotation transients', () => {
    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 466,
        screenHeight: 678,
        innerWidth: 466,
        innerHeight: 350,
        orientationType: 'portrait-primary',
        alreadyWidened: true,
      }),
    ).toEqual({
      active: true,
      posture: 'portrait',
      targetWidth: 466,
      rightInset: 84,
      compactHeight: false,
    })

    // Rotating from widened landscape-secondary (width=678) back to portrait transiently measures 678x678.
    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 466,
        screenHeight: 678,
        innerWidth: 678,
        innerHeight: 678,
        orientationType: 'portrait-primary',
        alreadyWidened: true,
      }),
    ).toEqual({
      active: true,
      posture: 'portrait',
      targetWidth: 466,
      rightInset: 84,
      compactHeight: false,
    })

    // Rotating from widened portrait (466x678) into landscape-secondary transiently measures 466x678
    // before innerHeight shrinks to 466.
    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 466,
        screenHeight: 678,
        innerWidth: 466,
        innerHeight: 678,
        orientationType: 'landscape-secondary',
        alreadyWidened: true,
      }),
    ).toEqual({
      active: true,
      posture: 'landscape-secondary',
      targetWidth: 678,
      rightInset: 84,
      compactHeight: true,
    })
  })

  test('falls back to viewport geometry when screen size is spoofed, keeping keyboard and rotation transitions active without matching split view or other iPhones', () => {
    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 390,
        screenHeight: 844,
        innerWidth: 382,
        innerHeight: 678,
        orientationType: 'portrait-primary',
      }),
    ).toEqual({
      active: true,
      posture: 'portrait',
      targetWidth: 466,
      rightInset: 84,
      compactHeight: false,
    })

    // Already widened under spoofed screen: portrait virtual keyboard and cross-orientation transients stay active.
    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 390,
        screenHeight: 844,
        innerWidth: 466,
        innerHeight: 350,
        orientationType: 'portrait-primary',
        alreadyWidened: true,
      }),
    ).toEqual({
      active: true,
      posture: 'portrait',
      targetWidth: 466,
      rightInset: 84,
      compactHeight: false,
    })

    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 390,
        screenHeight: 844,
        innerWidth: 466,
        innerHeight: 678,
        orientationType: 'landscape-secondary',
        alreadyWidened: true,
      }),
    ).toEqual({
      active: true,
      posture: 'landscape-secondary',
      targetWidth: 678,
      rightInset: 84,
      compactHeight: true,
    })

    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 390,
        screenHeight: 844,
        innerWidth: 466,
        innerHeight: 466,
        orientationType: 'landscape-secondary',
        alreadyWidened: true,
      }),
    ).toEqual({
      active: true,
      posture: 'landscape-secondary',
      targetWidth: 678,
      rightInset: 84,
      compactHeight: true,
    })

    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 390,
        screenHeight: 844,
        innerWidth: 678,
        innerHeight: 466,
        orientationType: 'portrait-primary',
        alreadyWidened: true,
      }),
    ).toEqual({
      active: true,
      posture: 'portrait',
      targetWidth: 466,
      rightInset: 84,
      compactHeight: false,
    })

    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 390,
        screenHeight: 844,
        innerWidth: 678,
        innerHeight: 678,
        orientationType: 'portrait-primary',
        alreadyWidened: true,
      }),
    ).toEqual({
      active: true,
      posture: 'portrait',
      targetWidth: 466,
      rightInset: 84,
      compactHeight: false,
    })

    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 390,
        screenHeight: 844,
        innerWidth: 678,
        innerHeight: 220,
        orientationType: 'landscape-secondary',
        alreadyWidened: true,
      }),
    ).toEqual({
      active: true,
      posture: 'landscape-secondary',
      targetWidth: 678,
      rightInset: 84,
      compactHeight: true,
    })

    // Unfolding to inner portrait with keyboard open must still deactivate under spoofed screen.
    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 390,
        screenHeight: 844,
        innerWidth: 669,
        innerHeight: 450,
        orientationType: 'portrait-primary',
        alreadyWidened: true,
      }).active,
    ).toBe(false)

    // Inner display split view (385pt) and standard iPhones must not trigger the fallback.
    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 951,
        screenHeight: 669,
        innerWidth: 385,
        innerHeight: 609,
        orientationType: 'portrait-primary',
      }).active,
    ).toBe(false)

    expect(
      resolveDuoFrontWiden({
        standalone: true,
        screenWidth: 393,
        screenHeight: 852,
        innerWidth: 393,
        innerHeight: 852,
        orientationType: 'portrait-primary',
      }).active,
    ).toBe(false)
  })

  test('sanitizes visualViewport.scale when building widened viewport content', () => {
    expect(sanitizeVisualScale(1.25)).toBe(1.25)
    expect(sanitizeVisualScale(-1)).toBe(1)
    expect(sanitizeVisualScale(Number.NaN)).toBe(1)
    expect(buildDuoFrontViewportContent(466, 1.25)).toContain('width=372.8, height=device-height')
    expect(buildDuoFrontViewportContent(466, -33554432)).toContain(
      'width=466, height=device-height',
    )
  })
})

function setDuoWindowGeometry(options: {
  screenWidth: number
  screenHeight: number
  innerWidth: number
  innerHeight: number
  orientationType?: string
}) {
  Object.defineProperty(window, 'screen', {
    configurable: true,
    value: {
      width: options.screenWidth,
      height: options.screenHeight,
      orientation: options.orientationType ? { type: options.orientationType } : undefined,
    },
  })
  Object.defineProperty(window, 'innerWidth', {
    configurable: true,
    value: options.innerWidth,
  })
  Object.defineProperty(window, 'innerHeight', {
    configurable: true,
    value: options.innerHeight,
  })
}

describe('duoFrontViewportScript', () => {
  let meta: HTMLMetaElement
  const originalScreen = Object.getOwnPropertyDescriptor(window, 'screen')
  const originalInnerWidth = Object.getOwnPropertyDescriptor(window, 'innerWidth')
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, 'innerHeight')

  beforeEach(() => {
    meta = document.createElement('meta')
    meta.name = 'viewport'
    meta.content = DEFAULT_VIEWPORT_CONTENT
    document.head.appendChild(meta)
  })

  afterEach(() => {
    meta.remove()
    document.documentElement.removeAttribute('data-pwa-simulate-standalone')
    delete document.documentElement.dataset.pwaStandalone
    delete document.documentElement.dataset.duoFrontWiden
    if (originalScreen) Object.defineProperty(window, 'screen', originalScreen)
    if (originalInnerWidth) Object.defineProperty(window, 'innerWidth', originalInnerWidth)
    if (originalInnerHeight) Object.defineProperty(window, 'innerHeight', originalInnerHeight)
  })

  test('widens viewport before first layout on Duo outer standalone portrait and skips non-standalone or landscape-primary', () => {
    setDuoWindowGeometry({
      screenWidth: 466,
      screenHeight: 678,
      innerWidth: 382,
      innerHeight: 678,
      orientationType: 'portrait-primary',
    })

    // Non-standalone tab stays untouched.
    document.documentElement.setAttribute('data-pwa-simulate-standalone', 'false')
    new Function(duoFrontViewportScript())()
    expect(meta.content).toBe(DEFAULT_VIEWPORT_CONTENT)
    expect(document.documentElement.dataset.duoFrontWiden).toBeUndefined()

    // Standalone outer portrait widens immediately.
    document.documentElement.setAttribute('data-pwa-simulate-standalone', 'true')
    new Function(duoFrontViewportScript())()
    expect(meta.content).toBe(buildDuoFrontViewportContent(466))
    expect(document.documentElement.dataset.duoFrontWiden).toBe('portrait')

    // Landscape-primary (left column) falls back to regular standalone viewport.
    delete document.documentElement.dataset.duoFrontWiden
    meta.content = DEFAULT_VIEWPORT_CONTENT
    setDuoWindowGeometry({
      screenWidth: 466,
      screenHeight: 678,
      innerWidth: 594,
      innerHeight: 466,
      orientationType: 'landscape-primary',
    })
    new Function(duoFrontViewportScript())()
    expect(meta.content).toBe(STANDALONE_VIEWPORT_CONTENT)
    expect(document.documentElement.dataset.duoFrontWiden).toBeUndefined()

    // Unfolded inner display with stale screen size stays regular standalone viewport.
    setDuoWindowGeometry({
      screenWidth: 466,
      screenHeight: 678,
      innerWidth: 951,
      innerHeight: 669,
      orientationType: 'landscape-secondary',
    })
    new Function(duoFrontViewportScript())()
    expect(meta.content).toBe(STANDALONE_VIEWPORT_CONTENT)
    expect(document.documentElement.dataset.duoFrontWiden).toBeUndefined()
  })
})

describe('resolveDuoRightRail', () => {
  test('activates for iPhone Duo folded portrait and unfolded landscape in standalone PWA', () => {
    expect(
      resolveDuoRightRail({
        standalone: true,
        insets: { top: 0, right: 84, bottom: 34, left: 0 },
        viewportWidth: 466,
        viewportHeight: 678,
      }),
    ).toEqual({ active: true, compactHeight: false })

    expect(
      resolveDuoRightRail({
        standalone: true,
        insets: { top: 0, right: 84, bottom: 34, left: 0 },
        viewportWidth: 951,
        viewportHeight: 669,
      }),
    ).toEqual({ active: true, compactHeight: false })
  })

  test('flags compactHeight only in short landscape postures, never in portrait', () => {
    expect(
      resolveDuoRightRail({
        standalone: true,
        insets: { top: 0, right: 84, bottom: 34, left: 0 },
        viewportWidth: 678,
        viewportHeight: 466,
      }),
    ).toEqual({ active: true, compactHeight: true })

    expect(
      resolveDuoRightRail({
        standalone: true,
        insets: { top: 0, right: 84, bottom: 34, left: 0 },
        viewportWidth: 466,
        viewportHeight: 480,
      }),
    ).toEqual({ active: true, compactHeight: false })
  })

  test('stays inactive outside standalone, on symmetric landscape phones, and on top/left insets', () => {
    expect(
      resolveDuoRightRail({
        standalone: false,
        insets: { top: 0, right: 84, bottom: 34, left: 0 },
        viewportWidth: 466,
        viewportHeight: 678,
      }),
    ).toEqual({ active: false, compactHeight: false })

    expect(
      resolveDuoRightRail({
        standalone: true,
        insets: { top: 0, right: 59, bottom: 21, left: 59 },
        viewportWidth: 852,
        viewportHeight: 393,
      }),
    ).toEqual({ active: false, compactHeight: false })

    expect(
      resolveDuoRightRail({
        standalone: true,
        insets: { top: 0, right: 0, bottom: 34, left: 84 },
        viewportWidth: 678,
        viewportHeight: 466,
      }),
    ).toEqual({ active: false, compactHeight: false })

    expect(
      resolveDuoRightRail({
        standalone: true,
        insets: { top: 82, right: 0, bottom: 34, left: 0 },
        viewportWidth: 669,
        viewportHeight: 951,
      }),
    ).toEqual({ active: false, compactHeight: false })
  })
})

describe('resolveDuoBackAction', () => {
  test('matches App.vue rendering priority across narrow levels, trace, starting, and sub-views', () => {
    expect(
      resolveDuoBackAction({
        mainKind: 'trace',
        starting: false,
        hasCurrentProject: true,
        layoutMode: 'medium',
        workspaceLevel: 'coordinator',
      }),
    ).toBe('main')

    // In narrow mode, projects list hides main views and has no higher level to go back to.
    expect(
      resolveDuoBackAction({
        mainKind: 'plan',
        starting: false,
        hasCurrentProject: true,
        layoutMode: 'narrow',
        workspaceLevel: 'projects',
      }),
    ).toBeNull()

    expect(
      resolveDuoBackAction({
        mainKind: 'trace',
        starting: false,
        hasCurrentProject: true,
        layoutMode: 'narrow',
        workspaceLevel: 'projects',
      }),
    ).toBeNull()

    // In narrow mode, backing out of the side pane always returns to the coordinator first.
    expect(
      resolveDuoBackAction({
        mainKind: 'artifacts',
        starting: false,
        hasCurrentProject: true,
        layoutMode: 'narrow',
        workspaceLevel: 'side',
      }),
    ).toBe('to-coordinator')

    // StartScreen takes precedence over plan/artifacts when creating a new project.
    expect(
      resolveDuoBackAction({
        mainKind: 'plan',
        starting: true,
        hasCurrentProject: true,
        layoutMode: 'medium',
        workspaceLevel: 'coordinator',
      }),
    ).toBe('cancel-new')

    expect(
      resolveDuoBackAction({
        mainKind: 'conversation',
        starting: true,
        hasCurrentProject: true,
        layoutMode: 'medium',
        workspaceLevel: 'coordinator',
      }),
    ).toBe('cancel-new')

    expect(
      resolveDuoBackAction({
        mainKind: 'artifacts',
        starting: false,
        hasCurrentProject: true,
        layoutMode: 'narrow',
        workspaceLevel: 'coordinator',
      }),
    ).toBe('main')

    expect(
      resolveDuoBackAction({
        mainKind: 'conversation',
        starting: false,
        hasCurrentProject: true,
        layoutMode: 'narrow',
        workspaceLevel: 'side',
      }),
    ).toBe('to-coordinator')

    expect(
      resolveDuoBackAction({
        mainKind: 'conversation',
        starting: false,
        hasCurrentProject: true,
        layoutMode: 'narrow',
        workspaceLevel: 'coordinator',
      }),
    ).toBe('to-projects')

    expect(
      resolveDuoBackAction({
        mainKind: 'conversation',
        starting: false,
        hasCurrentProject: true,
        layoutMode: 'narrow',
        workspaceLevel: 'projects',
      }),
    ).toBeNull()

    expect(
      resolveDuoBackAction({
        mainKind: 'conversation',
        starting: false,
        hasCurrentProject: true,
        layoutMode: 'medium',
        workspaceLevel: 'coordinator',
      }),
    ).toBeNull()
  })
})

describe('resolveFoldLayout', () => {
  test('aligns horizontal splitter to the physical hinge inside safe content width on iPhone Duo book pose', () => {
    // iPhone Duo open landscape half-folded: 951x669, right safe inset 84, 40px central hinge (455.5..495.5).
    const fold = resolveFoldLayout({
      supported: true,
      posture: 'folded',
      segments: [
        { left: 0, top: 0, right: 455.5, bottom: 669, width: 455.5, height: 669 },
        { left: 495.5, top: 0, right: 951, bottom: 669, width: 455.5, height: 669 },
      ],
      insets: { top: 0, right: 84, bottom: 34, left: 0 },
      viewportWidth: 951,
      viewportHeight: 669,
    })

    expect(fold).toEqual({
      supported: true,
      posture: 'folded',
      mode: 'horizontal',
      hingeGap: 40,
      horizontalSplitPercent: 54.84,
      verticalTopHeight: null,
      verticalBottomHeight: null,
    })
  })

  test('computes vertical top/bottom regions for tabletop/laptop fold pose', () => {
    const fold = resolveFoldLayout({
      supported: true,
      posture: 'folded',
      segments: [
        { left: 0, top: 0, right: 669, bottom: 455.5, width: 669, height: 455.5 },
        { left: 0, top: 495.5, right: 669, bottom: 951, width: 669, height: 455.5 },
      ],
      insets: { top: 82, right: 0, bottom: 34, left: 0 },
      viewportWidth: 669,
      viewportHeight: 951,
    })

    expect(fold).toEqual({
      supported: true,
      posture: 'folded',
      mode: 'vertical',
      hingeGap: 40,
      horizontalSplitPercent: null,
      verticalTopHeight: 456,
      verticalBottomHeight: 456,
    })
  })

  test('stays in none mode when flat (continuous) or unsupported', () => {
    expect(
      resolveFoldLayout({
        supported: true,
        posture: 'continuous',
        segments: [
          { left: 0, top: 0, right: 455.5, bottom: 669, width: 455.5, height: 669 },
          { left: 495.5, top: 0, right: 951, bottom: 669, width: 455.5, height: 669 },
        ],
        insets: { top: 0, right: 84, bottom: 34, left: 0 },
        viewportWidth: 951,
        viewportHeight: 669,
      }).mode,
    ).toBe('none')

    expect(
      resolveFoldLayout({
        supported: false,
        posture: 'folded',
        segments: [],
        insets: { top: 0, right: 0, bottom: 0, left: 0 },
        viewportWidth: 951,
        viewportHeight: 669,
      }).mode,
    ).toBe('none')
  })
})

describe('readNativeFoldInput & syncFoldCssState', () => {
  afterEach(() => {
    delete document.documentElement.dataset.devicePosture
    delete document.documentElement.dataset.foldMode
    document.documentElement.style.removeProperty('--ariadne-fold-gap')
    document.documentElement.style.removeProperty('--ariadne-fold-top-height')
    document.documentElement.style.removeProperty('--ariadne-fold-bottom-height')
  })

  test('reads navigator.devicePosture and window.viewport.segments using layout viewport dimensions', () => {
    const input = readNativeFoldInput({
      innerWidth: 951,
      innerHeight: 669,
      // Simulate an open virtual keyboard shrinking visualViewport without changing layout viewport.
      visualViewport: { width: 951, height: 340 },
      matchMedia: vi.fn().mockReturnValue({ matches: false }),
      navigator: {
        devicePosture: { type: 'folded' },
      } as unknown as Navigator,
      viewport: {
        segments: [
          {
            left: 0,
            top: 0,
            right: 455.5,
            bottom: 669,
            width: 455.5,
            height: 669,
          } as DOMRectReadOnly,
          {
            left: 495.5,
            top: 0,
            right: 951,
            bottom: 669,
            width: 455.5,
            height: 669,
          } as DOMRectReadOnly,
        ],
      },
    } as Parameters<typeof readNativeFoldInput>[0])

    expect(input.supported).toBe(true)
    expect(input.posture).toBe('folded')
    expect(input.segments).toHaveLength(2)
    expect(input.viewportWidth).toBe(951)
    expect(input.viewportHeight).toBe(669)
  })

  test('falls back to viewport-segment media queries when JS segments are absent', () => {
    const input = readNativeFoldInput({
      innerWidth: 669,
      innerHeight: 951,
      matchMedia: vi.fn((query: string) => ({
        matches:
          query === '(device-posture: folded)' || query === '(vertical-viewport-segments: 2)',
      })) as unknown as Window['matchMedia'],
      navigator: {} as Navigator,
    })

    expect(input.supported).toBe(true)
    expect(input.posture).toBe('folded')
    expect(input.segments).toHaveLength(2)
    expect(input.segments[0]!.bottom).toBe(475.5)
    expect(input.segments[1]!.top).toBe(475.5)
  })

  test('infers folded posture from Viewport Segments when Device Posture API is absent', () => {
    const singleSegment = readNativeFoldInput({
      innerWidth: 382,
      innerHeight: 678,
      matchMedia: vi.fn().mockReturnValue({ matches: false }),
      navigator: {} as Navigator,
      viewport: {
        segments: [
          {
            left: 0,
            top: 0,
            right: 382,
            bottom: 678,
            width: 382,
            height: 678,
          } as DOMRectReadOnly,
        ],
      },
    })

    expect(singleSegment.supported).toBe(true)
    expect(singleSegment.posture).toBe('continuous')
    expect(
      resolveFoldLayout({
        ...singleSegment,
        insets: { top: 0, right: 0, bottom: 34, left: 0 },
      }).mode,
    ).toBe('none')

    const twoSegments = readNativeFoldInput({
      innerWidth: 951,
      innerHeight: 669,
      matchMedia: vi.fn().mockReturnValue({ matches: false }),
      navigator: {} as Navigator,
      viewport: {
        segments: [
          {
            left: 0,
            top: 0,
            right: 455.5,
            bottom: 669,
            width: 455.5,
            height: 669,
          } as DOMRectReadOnly,
          {
            left: 495.5,
            top: 0,
            right: 951,
            bottom: 669,
            width: 455.5,
            height: 669,
          } as DOMRectReadOnly,
        ],
      },
    })

    expect(twoSegments.supported).toBe(true)
    expect(twoSegments.posture).toBe('folded')
    expect(
      resolveFoldLayout({
        ...twoSegments,
        insets: { top: 0, right: 84, bottom: 34, left: 0 },
      }),
    ).toEqual({
      supported: true,
      posture: 'folded',
      mode: 'horizontal',
      hingeGap: 40,
      horizontalSplitPercent: 54.84,
      verticalTopHeight: null,
      verticalBottomHeight: null,
    })

    const mediaQueryOnly = readNativeFoldInput({
      innerWidth: 951,
      innerHeight: 669,
      matchMedia: vi.fn((query: string) => ({
        matches: query === '(horizontal-viewport-segments: 2)',
      })) as unknown as Window['matchMedia'],
      navigator: {} as Navigator,
    })
    expect(mediaQueryOnly.supported).toBe(true)
    expect(mediaQueryOnly.posture).toBe('folded')
    expect(mediaQueryOnly.segments).toHaveLength(2)
  })

  test('keeps continuous posture when Device Posture API explicitly reports flat despite two segments', () => {
    const input = readNativeFoldInput({
      innerWidth: 951,
      innerHeight: 669,
      matchMedia: vi.fn().mockReturnValue({ matches: false }),
      navigator: {
        devicePosture: { type: 'continuous' },
      } as unknown as Navigator,
      viewport: {
        segments: [
          {
            left: 0,
            top: 0,
            right: 455.5,
            bottom: 669,
            width: 455.5,
            height: 669,
          } as DOMRectReadOnly,
          {
            left: 495.5,
            top: 0,
            right: 951,
            bottom: 669,
            width: 455.5,
            height: 669,
          } as DOMRectReadOnly,
        ],
      },
    })

    expect(input.supported).toBe(true)
    expect(input.posture).toBe('continuous')
    expect(
      resolveFoldLayout({
        ...input,
        insets: { top: 0, right: 84, bottom: 34, left: 0 },
      }).mode,
    ).toBe('none')
  })

  test('writes and clears CSS variables and data attributes cleanly', () => {
    syncFoldCssState(
      {
        supported: true,
        posture: 'folded',
        mode: 'vertical',
        hingeGap: 40,
        horizontalSplitPercent: null,
        verticalTopHeight: 456,
        verticalBottomHeight: 456,
      },
      document,
    )

    expect(document.documentElement.dataset.devicePosture).toBe('folded')
    expect(document.documentElement.dataset.foldMode).toBe('vertical')
    expect(document.documentElement.style.getPropertyValue('--ariadne-fold-gap')).toBe('40px')
    expect(document.documentElement.style.getPropertyValue('--ariadne-fold-top-height')).toBe(
      '456px',
    )
    expect(document.documentElement.style.getPropertyValue('--ariadne-fold-bottom-height')).toBe(
      '456px',
    )

    syncFoldCssState(
      {
        supported: false,
        posture: 'continuous',
        mode: 'none',
        hingeGap: 0,
        horizontalSplitPercent: null,
        verticalTopHeight: null,
        verticalBottomHeight: null,
      },
      document,
    )

    expect(document.documentElement.dataset.devicePosture).toBeUndefined()
    expect(document.documentElement.dataset.foldMode).toBeUndefined()
    expect(document.documentElement.style.getPropertyValue('--ariadne-fold-gap')).toBe('0px')
    expect(document.documentElement.style.getPropertyValue('--ariadne-fold-top-height')).toBe('')
  })
})

describe('usePwaEnvironment', () => {
  let meta: HTMLMetaElement

  beforeEach(() => {
    meta = document.createElement('meta')
    meta.name = 'viewport'
    meta.content = DEFAULT_VIEWPORT_CONTENT
    document.head.appendChild(meta)
  })

  afterEach(() => {
    meta.remove()
    document.documentElement.removeAttribute('data-pwa-simulate-standalone')
    delete document.documentElement.dataset.pwaStandalone
    delete document.documentElement.dataset.duoFrontWiden
  })

  test('prevents WebKit gesturestart zoom only while standalone is active and cleans up on dispose', () => {
    document.documentElement.setAttribute('data-pwa-simulate-standalone', 'true')
    const initialBodyChildren = document.body.children.length

    const scope = effectScope()
    const env = scope.run(() => usePwaEnvironment())!

    expect(env.standalone.value).toBe(true)
    expect(meta.content).toBe(STANDALONE_VIEWPORT_CONTENT)

    const standaloneGesture = new Event('gesturestart', { cancelable: true })
    document.dispatchEvent(standaloneGesture)
    expect(standaloneGesture.defaultPrevented).toBe(true)

    scope.stop()
    expect(document.body.children.length).toBe(initialBodyChildren)

    const afterDisposeGesture = new Event('gesturestart', { cancelable: true })
    document.dispatchEvent(afterDisposeGesture)
    expect(afterDisposeGesture.defaultPrevented).toBe(false)
  })

  test('activates Duo rail when ResizeObserver reports delayed safe-area insets without window resize', () => {
    document.documentElement.setAttribute('data-pwa-simulate-standalone', 'true')

    let rightInset = '0px'
    const originalGetComputedStyle = window.getComputedStyle.bind(window)
    const computedSpy = vi.spyOn(window, 'getComputedStyle').mockImplementation((el) => {
      const style = originalGetComputedStyle(el)
      if (el instanceof HTMLElement && el.style.paddingRight.includes('--ariadne-safe-right')) {
        return new Proxy(style, {
          get(target, prop) {
            if (prop === 'paddingRight') return rightInset
            return Reflect.get(target, prop)
          },
        })
      }
      return style
    })

    const observerState: { callback: ResizeObserverCallback | null } = { callback: null }
    const observed: Element[] = []
    const disconnect = vi.fn()
    const OriginalResizeObserver = globalThis.ResizeObserver
    globalThis.ResizeObserver = class MockResizeObserver {
      constructor(cb: ResizeObserverCallback) {
        observerState.callback = cb
      }
      observe(target: Element) {
        observed.push(target)
      }
      unobserve() {}
      disconnect() {
        disconnect()
      }
    } as unknown as typeof ResizeObserver

    const scope = effectScope()
    try {
      const env = scope.run(() => usePwaEnvironment())!
      expect(env.insets.right).toBe(0)
      expect(env.duoRail.value.active).toBe(false)
      expect(observed).toHaveLength(2)

      rightInset = '84px'
      observerState.callback?.([], {} as ResizeObserver)

      expect(env.insets.right).toBe(84)
      expect(env.duoRail.value.active).toBe(true)

      scope.stop()
      expect(disconnect).toHaveBeenCalledTimes(1)
    } finally {
      scope.stop()
      computedSpy.mockRestore()
      globalThis.ResizeObserver = OriginalResizeObserver
    }
  })

  test('re-measures safe-area insets on animation frame and pageshow after launch', () => {
    document.documentElement.setAttribute('data-pwa-simulate-standalone', 'true')

    let rightInset = '0px'
    const originalGetComputedStyle = window.getComputedStyle.bind(window)
    const computedSpy = vi.spyOn(window, 'getComputedStyle').mockImplementation((el) => {
      const style = originalGetComputedStyle(el)
      if (el instanceof HTMLElement && el.style.paddingRight.includes('--ariadne-safe-right')) {
        return new Proxy(style, {
          get(target, prop) {
            if (prop === 'paddingRight') return rightInset
            return Reflect.get(target, prop)
          },
        })
      }
      return style
    })

    const frames = new Map<number, FrameRequestCallback>()
    let nextFrameId = 1
    const rafSpy = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => {
      const id = nextFrameId++
      frames.set(id, cb)
      return id
    })
    const cancelSpy = vi.spyOn(window, 'cancelAnimationFrame').mockImplementation((id) => {
      frames.delete(id)
    })

    const flushFrames = (time: number) => {
      const queued = Array.from(frames.values())
      frames.clear()
      for (const cb of queued) cb(time)
    }

    const scope = effectScope()
    try {
      const env = scope.run(() => usePwaEnvironment())!
      expect(env.duoRail.value.active).toBe(false)
      expect(frames.size).toBe(1)

      rightInset = '84px'
      flushFrames(16)
      expect(env.insets.right).toBe(84)
      expect(env.duoRail.value.active).toBe(true)

      rightInset = '0px'
      window.dispatchEvent(new Event('pageshow'))
      expect(frames.size).toBe(1)
      flushFrames(32)
      expect(env.insets.right).toBe(0)
      expect(env.duoRail.value.active).toBe(false)

      window.dispatchEvent(new Event('pageshow'))
      expect(frames.size).toBe(1)
      scope.stop()
      expect(cancelSpy).toHaveBeenCalledTimes(1)
      expect(frames.size).toBe(0)
    } finally {
      scope.stop()
      computedSpy.mockRestore()
      rafSpy.mockRestore()
      cancelSpy.mockRestore()
    }
  })

  test('widens Duo outer standalone viewport, reacts to orientation/unfold, yields to native insets, and snaps scrollX back to 0', () => {
    vi.useFakeTimers()
    document.documentElement.setAttribute('data-pwa-simulate-standalone', 'true')

    const originalScreen = Object.getOwnPropertyDescriptor(window, 'screen')
    const originalInnerWidth = Object.getOwnPropertyDescriptor(window, 'innerWidth')
    const originalInnerHeight = Object.getOwnPropertyDescriptor(window, 'innerHeight')
    const originalScrollX = Object.getOwnPropertyDescriptor(window, 'scrollX')
    const originalScrollTo = window.scrollTo

    const orientationTarget = new EventTarget()
    let orientationType = 'portrait-primary'
    let screenWidth = 466
    let screenHeight = 678
    let innerWidth = 382
    let innerHeight = 678
    let currentScrollX = 0
    let nativeEnvRight = '0px'

    Object.defineProperty(window, 'screen', {
      configurable: true,
      get: () => ({
        width: screenWidth,
        height: screenHeight,
        orientation: {
          get type() {
            return orientationType
          },
          addEventListener: orientationTarget.addEventListener.bind(orientationTarget),
          removeEventListener: orientationTarget.removeEventListener.bind(orientationTarget),
        },
      }),
    })
    Object.defineProperty(window, 'innerWidth', {
      configurable: true,
      get: () => innerWidth,
    })
    Object.defineProperty(window, 'innerHeight', {
      configurable: true,
      get: () => innerHeight,
    })
    Object.defineProperty(window, 'scrollX', {
      configurable: true,
      get: () => currentScrollX,
    })

    const scrollToSpy = vi.fn((options?: ScrollToOptions | number) => {
      if (typeof options === 'object' && options?.left !== undefined) {
        currentScrollX = options.left
      }
    })
    window.scrollTo = scrollToSpy as unknown as typeof window.scrollTo

    const observerState: { callback: ResizeObserverCallback | null } = { callback: null }
    const observed: Element[] = []
    const OriginalResizeObserver = globalThis.ResizeObserver
    globalThis.ResizeObserver = class MockResizeObserver {
      constructor(cb: ResizeObserverCallback) {
        observerState.callback = cb
      }
      observe(target: Element) {
        observed.push(target)
      }
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver

    const originalGetComputedStyle = window.getComputedStyle.bind(window)
    const computedSpy = vi.spyOn(window, 'getComputedStyle').mockImplementation((el) => {
      const style = originalGetComputedStyle(el)
      if (el instanceof HTMLElement && el.style.paddingRight.includes('--ariadne-safe-right')) {
        return new Proxy(style, {
          get(target, prop) {
            if (prop === 'paddingLeft') return nativeEnvRight
            if (prop === 'paddingRight') {
              return document.documentElement.dataset.duoFrontWiden ? '84px' : nativeEnvRight
            }
            return Reflect.get(target, prop)
          },
        })
      }
      return style
    })

    const scope = effectScope()
    try {
      const env = scope.run(() => usePwaEnvironment())!
      expect(env.duoFrontWiden.value.active).toBe(true)
      expect(env.duoFrontWiden.value.targetWidth).toBe(466)
      expect(meta.content).toBe(buildDuoFrontViewportContent(466))
      expect(document.documentElement.dataset.duoFrontWiden).toBe('portrait')
      expect(env.insets.right).toBe(84)
      expect(env.duoRail.value).toEqual({ active: true, compactHeight: false })

      // Verify the native right inset is probed via padding-left (border-box observable).
      const topRightProbe = observed.find(
        (el) => el instanceof HTMLElement && el.style.paddingRight.includes('--ariadne-safe-right'),
      ) as HTMLElement | undefined
      expect(topRightProbe?.style.paddingLeft).toContain('safe-area-inset-right')

      // Opening the virtual keyboard in portrait (466x350) must NOT flip compactHeight to true.
      innerWidth = 466
      innerHeight = 350
      window.dispatchEvent(new Event('resize'))
      expect(env.duoFrontWiden.value).toEqual({
        active: true,
        posture: 'portrait',
        targetWidth: 466,
        rightInset: 84,
        compactHeight: false,
      })
      expect(env.duoRail.value).toEqual({ active: true, compactHeight: false })
      innerHeight = 678
      window.dispatchEvent(new Event('resize'))

      // Sideways drag while finger is down waits until touchend + settle delay before gliding back.
      currentScrollX = 48
      const touchStart = new Event('touchstart')
      Object.defineProperty(touchStart, 'touches', { value: [{}] })
      window.dispatchEvent(touchStart)
      window.dispatchEvent(new Event('scroll'))
      vi.advanceTimersByTime(300)
      expect(scrollToSpy).not.toHaveBeenCalled()

      const touchEnd = new Event('touchend')
      Object.defineProperty(touchEnd, 'touches', { value: [] })
      window.dispatchEvent(touchEnd)
      vi.advanceTimersByTime(250)
      expect(scrollToSpy).toHaveBeenCalledWith({ left: 0, behavior: 'smooth' })
      expect(currentScrollX).toBe(0)

      // Rotate to landscape-secondary (right column): even on the transient 466x678 frame before
      // innerHeight shrinks, widening stays active and switches cleanly to 678 + compactHeight.
      orientationType = 'landscape-secondary'
      orientationTarget.dispatchEvent(new Event('change'))
      expect(env.duoFrontWiden.value.active).toBe(true)
      expect(env.duoFrontWiden.value.targetWidth).toBe(678)
      expect(meta.content).toBe(buildDuoFrontViewportContent(678))
      expect(document.documentElement.dataset.duoFrontWiden).toBe('landscape-secondary')
      expect(env.duoRail.value).toEqual({ active: true, compactHeight: true })

      innerWidth = 594
      innerHeight = 466
      window.dispatchEvent(new Event('resize'))
      expect(env.duoFrontWiden.value.targetWidth).toBe(678)
      expect(env.duoRail.value).toEqual({ active: true, compactHeight: true })

      // Flip 180deg to landscape-primary (left column): disable widening cleanly.
      orientationType = 'landscape-primary'
      orientationTarget.dispatchEvent(new Event('change'))
      expect(env.duoFrontWiden.value.active).toBe(false)
      expect(meta.content).toBe(STANDALONE_VIEWPORT_CONTENT)
      expect(document.documentElement.dataset.duoFrontWiden).toBeUndefined()
      expect(env.insets.right).toBe(0)
      expect(env.duoRail.value.active).toBe(false)

      // Return to outer portrait, then unfold to inner display with stale screen.width=466.
      orientationType = 'portrait-primary'
      innerWidth = 382
      innerHeight = 678
      window.dispatchEvent(new Event('resize'))
      expect(env.duoFrontWiden.value.active).toBe(true)

      innerWidth = 951
      innerHeight = 669
      window.dispatchEvent(new Event('resize'))
      expect(env.duoFrontWiden.value.active).toBe(false)
      expect(meta.content).toBe(STANDALONE_VIEWPORT_CONTENT)

      // Fold back to outer portrait and simulate native WebKit exposing env(safe-area-inset-right)=84px
      // asynchronously via border-box ResizeObserver (without a window resize event).
      innerWidth = 466
      innerHeight = 678
      window.dispatchEvent(new Event('resize'))
      expect(env.duoFrontWiden.value.active).toBe(true)

      nativeEnvRight = '84px'
      observerState.callback?.([], {} as ResizeObserver)
      expect(env.duoFrontWiden.value.active).toBe(false)
      expect(meta.content).toBe(STANDALONE_VIEWPORT_CONTENT)
      expect(document.documentElement.dataset.duoFrontWiden).toBeUndefined()
      expect(env.insets.right).toBe(84)
      expect(env.duoRail.value.active).toBe(true)
    } finally {
      scope.stop()
      computedSpy.mockRestore()
      globalThis.ResizeObserver = OriginalResizeObserver
      window.scrollTo = originalScrollTo
      if (originalScreen) Object.defineProperty(window, 'screen', originalScreen)
      if (originalInnerWidth) Object.defineProperty(window, 'innerWidth', originalInnerWidth)
      if (originalInnerHeight) Object.defineProperty(window, 'innerHeight', originalInnerHeight)
      if (originalScrollX) Object.defineProperty(window, 'scrollX', originalScrollX)
      vi.useRealTimers()
    }
  })
})
