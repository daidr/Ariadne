// @vitest-environment happy-dom
import { describe, expect, test, vi } from 'vitest'
import {
  arrayBufferToBase64Url,
  defaultDeviceLabel,
  detectPushSupport,
  navigateToNotificationTarget,
  syncAppBadge,
  urlBase64ToUint8Array,
} from './push'
import { parsePushPayload } from './sw'

describe('Web Push helpers', () => {
  test('round-trips base64url VAPID and subscription keys', () => {
    const sample = new Uint8Array([4, 251, 130, 0, 127, 255, 16, 42, 199])
    const encoded = arrayBufferToBase64Url(sample.buffer)
    expect(encoded).not.toMatch(/[+/=]/)
    const decoded = urlBase64ToUint8Array(encoded)
    expect(Array.from(decoded)).toEqual(Array.from(sample))
  })

  test('detects HTTPS, iOS standalone install requirement, and browser support', () => {
    expect(
      detectPushSupport({
        isSecureContext: false,
        userAgent: 'Mozilla/5.0 Chrome/130.0',
        maxTouchPoints: 0,
        standalone: false,
        hasServiceWorker: true,
        hasPushManager: true,
        hasNotification: true,
      }),
    ).toEqual({ supported: false, reason: 'insecure' })

    expect(
      detectPushSupport({
        isSecureContext: true,
        userAgent:
          'Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 Safari/604.1',
        maxTouchPoints: 5,
        standalone: false,
        hasServiceWorker: true,
        hasPushManager: false,
        hasNotification: false,
      }),
    ).toEqual({ supported: false, reason: 'ios-install' })

    expect(
      detectPushSupport({
        isSecureContext: true,
        userAgent:
          'Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 Safari/604.1',
        maxTouchPoints: 5,
        standalone: true,
        hasServiceWorker: true,
        hasPushManager: true,
        hasNotification: true,
      }),
    ).toEqual({ supported: true, reason: null })

    expect(
      detectPushSupport({
        isSecureContext: true,
        userAgent: 'Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/131.0',
        maxTouchPoints: 0,
        standalone: false,
        hasServiceWorker: true,
        hasPushManager: false,
        hasNotification: true,
      }),
    ).toEqual({ supported: false, reason: 'unsupported' })
  })

  test('builds readable device labels across desktop and mobile user agents', () => {
    expect(
      defaultDeviceLabel(
        'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36',
        0,
      ),
    ).toBe('macOS · Chrome')
    expect(
      defaultDeviceLabel(
        'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1',
        5,
      ),
    ).toBe('iPhone · Safari')
    expect(
      defaultDeviceLabel(
        'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/18.0 Safari/605.1.15',
        5,
      ),
    ).toBe('iPad · Safari')
    expect(
      defaultDeviceLabel(
        'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0',
        0,
      ),
    ).toBe('Windows · Edge')
  })

  test('syncs app badge count and clears badge at zero', () => {
    const setAppBadge = vi.fn(async () => {})
    const clearAppBadge = vi.fn(async () => {})
    Object.defineProperty(navigator, 'setAppBadge', { value: setAppBadge, configurable: true })
    Object.defineProperty(navigator, 'clearAppBadge', { value: clearAppBadge, configurable: true })

    syncAppBadge(3)
    expect(setAppBadge).toHaveBeenCalledWith(3)
    expect(clearAppBadge).not.toHaveBeenCalled()

    syncAppBadge(0)
    expect(clearAppBadge).toHaveBeenCalledTimes(1)
  })
})

describe('Service Worker parsePushPayload', () => {
  test('parses valid push payload and rejects cross-origin URLs', () => {
    const parsed = parsePushPayload(
      JSON.stringify({
        title: '等你回答 · Ariadne',
        body: '确认发布？',
        tag: 'ariadne:thr_1',
        url: '/projects/prj_1?pane=side&tab=thread%3Athr_1',
        projectId: 'prj_1',
        threadId: 'thr_1',
        needsMe: 4,
      }),
      'https://ariadne.example.com',
    )
    expect(parsed).toEqual({
      title: '等你回答 · Ariadne',
      body: '确认发布？',
      tag: 'ariadne:thr_1',
      url: '/projects/prj_1?pane=side&tab=thread%3Athr_1',
      projectId: 'prj_1',
      threadId: 'thr_1',
      needsMe: 4,
    })

    const unsafe = parsePushPayload(
      JSON.stringify({
        title: 'Alert',
        url: 'https://evil.example.com/phish',
        needsMe: -3,
      }),
      'https://ariadne.example.com',
    )
    expect(unsafe.url).toBe('/')
    expect(unsafe.needsMe).toBe(0)
  })

  test('falls back cleanly on non-JSON push payloads', () => {
    const fallback = parsePushPayload('plain text reminder', 'https://ariadne.example.com')
    expect(fallback.title).toBe('Ariadne')
    expect(fallback.body).toBe('plain text reminder')
    expect(fallback.url).toBe('/')
  })
})

describe('navigateToNotificationTarget', () => {
  test('switches back to coordinator and closes fullscreen when clicking current project coordinator notification', async () => {
    let full = true
    const calls: string[] = []
    await navigateToNotificationTarget(
      { projectId: 'prj_1' },
      {
        currentProjectId: () => 'prj_1',
        hasFullView: () => full,
        openProject: async (id) => {
          calls.push(`project:${id}`)
        },
        openThread: async (id) => {
          calls.push(`thread:${id}`)
        },
        closeFull: () => {
          full = false
          calls.push('closeFull')
        },
        openCoordinator: () => {
          calls.push('coordinator')
        },
      },
    )
    expect(calls).toEqual(['closeFull', 'coordinator'])
  })

  test('closes fullscreen before opening thread on current project', async () => {
    let full = true
    const calls: string[] = []
    await navigateToNotificationTarget(
      { projectId: 'prj_1', threadId: 'thr_9' },
      {
        currentProjectId: () => 'prj_1',
        hasFullView: () => full,
        openProject: async (id) => {
          calls.push(`project:${id}`)
        },
        openThread: async (id) => {
          calls.push(`thread:${id}`)
        },
        closeFull: () => {
          full = false
          calls.push('closeFull')
        },
        openCoordinator: () => {
          calls.push('coordinator')
        },
      },
    )
    expect(calls).toEqual(['closeFull', 'thread:thr_9'])
  })

  test('switches project then opens coordinator or thread for a different project', async () => {
    let current: string | null = 'prj_1'
    const calls: string[] = []
    const actions = {
      currentProjectId: () => current,
      hasFullView: () => false,
      openProject: async (id: string) => {
        current = id
        calls.push(`project:${id}`)
      },
      openThread: async (id: string) => {
        calls.push(`thread:${id}`)
      },
      closeFull: () => {
        calls.push('closeFull')
      },
      openCoordinator: () => {
        calls.push('coordinator')
      },
    }

    await navigateToNotificationTarget({ projectId: 'prj_2' }, actions)
    expect(calls).toEqual(['project:prj_2', 'coordinator'])

    calls.length = 0
    await navigateToNotificationTarget({ projectId: 'prj_3', threadId: 'thr_7' }, actions)
    expect(calls).toEqual(['project:prj_3', 'thread:thr_7'])
  })
})
