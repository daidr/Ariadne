// @vitest-environment happy-dom
import { effectScope } from 'vue'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import {
  DEFAULT_VIEWPORT_CONTENT,
  STANDALONE_VIEWPORT_CONTENT,
  isStandalonePwa,
  readNativeFoldInput,
  resolveDuoBackAction,
  resolveDuoRightRail,
  resolveFoldLayout,
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
})
