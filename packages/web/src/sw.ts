// Service Worker for Ariadne PWA: preserves NetworkFirst navigation, CacheFirst /assets/,
// complete /api/* bypass, and handles Web Push (RFC 8291/8292) + notification click routing.

interface PrecacheEntry {
  url: string
  revision?: string | null
}

interface SwWindowClient {
  url: string
  focused?: boolean
  visibilityState?: string
  focus(): Promise<SwWindowClient>
  postMessage(message: unknown): void
}

interface SwClients {
  claim(): Promise<void>
  matchAll(options?: {
    type?: 'window' | 'worker' | 'sharedworker' | 'all'
    includeUncontrolled?: boolean
  }): Promise<SwWindowClient[]>
  openWindow(url: string): Promise<SwWindowClient | null>
}

interface SwRegistration {
  showNotification(title: string, options?: NotificationOptions): Promise<void>
}

interface SwNavigator {
  setAppBadge?(contents?: number): Promise<void>
  clearAppBadge?(): Promise<void>
}

interface SwExtendableEvent extends Event {
  waitUntil(promise: Promise<unknown>): void
}

interface SwFetchEvent extends SwExtendableEvent {
  readonly request: Request
  respondWith(response: Response | Promise<Response>): void
}

interface SwPushMessageData {
  text(): string
  json(): unknown
}

interface SwPushEvent extends SwExtendableEvent {
  readonly data: SwPushMessageData | null
}

interface SwNotificationEvent extends SwExtendableEvent {
  readonly notification: Notification
}

interface SwGlobalScope {
  readonly location: Location
  readonly caches: CacheStorage
  readonly clients: SwClients
  readonly registration: SwRegistration
  readonly navigator: SwNavigator
  __WB_MANIFEST?: Array<PrecacheEntry | string>
  skipWaiting(): Promise<void>
  addEventListener(type: 'install', listener: (event: SwExtendableEvent) => void): void
  addEventListener(type: 'activate', listener: (event: SwExtendableEvent) => void): void
  addEventListener(type: 'fetch', listener: (event: SwFetchEvent) => void): void
  addEventListener(type: 'push', listener: (event: SwPushEvent) => void): void
  addEventListener(type: 'notificationclick', listener: (event: SwNotificationEvent) => void): void
}

export interface PushNotificationData {
  event?: string
  title?: string
  body?: string
  url?: string
  projectId?: string
  threadId?: string
  tag?: string
  needsMe?: number
}

const PRECACHE_NAME = 'ariadne-precache-v1'
const PAGES_CACHE = 'ariadne-pages'
const ASSETS_CACHE = 'ariadne-assets'
const ACTIVE_CACHES = new Set([PRECACHE_NAME, PAGES_CACHE, ASSETS_CACHE])
const NETWORK_TIMEOUT_MS = 5_000
const MAX_PAGE_ENTRIES = 20
const MAX_ASSET_ENTRIES = 200

declare global {
  interface Window {
    __WB_MANIFEST?: Array<PrecacheEntry | string>
  }
}

const sw = self as unknown as SwGlobalScope

// vite-plugin-pwa replaces the literal `self.__WB_MANIFEST` expression at build time.
// oxlint-disable-next-line no-underscore-dangle
const precacheManifest: Array<PrecacheEntry | string> = self.__WB_MANIFEST ?? []

function sanitizeTargetUrl(raw: unknown, origin: string): string {
  if (typeof raw !== 'string' || !raw.trim()) return '/'
  try {
    const parsed = new URL(raw, origin)
    if (parsed.origin !== origin) return '/'
    return `${parsed.pathname}${parsed.search}${parsed.hash}`
  } catch {
    return '/'
  }
}

export function parsePushPayload(
  rawText: string,
  origin: string,
): {
  title: string
  body: string
  tag: string
  url: string
  projectId?: string
  threadId?: string
  needsMe?: number
} {
  let parsed: PushNotificationData | null = null
  try {
    const candidate = JSON.parse(rawText) as unknown
    if (candidate && typeof candidate === 'object') {
      parsed = candidate as PushNotificationData
    }
  } catch {
    parsed = null
  }
  const title =
    typeof parsed?.title === 'string' && parsed.title.trim() ? parsed.title.trim() : 'Ariadne'
  const body =
    typeof parsed?.body === 'string'
      ? parsed.body
      : parsed === null && rawText.trim()
        ? rawText.trim().slice(0, 200)
        : ''
  const tag = typeof parsed?.tag === 'string' && parsed.tag.trim() ? parsed.tag.trim() : 'ariadne'
  const url = sanitizeTargetUrl(parsed?.url, origin)
  const projectId =
    typeof parsed?.projectId === 'string' && parsed.projectId ? parsed.projectId : undefined
  const threadId =
    typeof parsed?.threadId === 'string' && parsed.threadId ? parsed.threadId : undefined
  const needsMe =
    typeof parsed?.needsMe === 'number' && Number.isFinite(parsed.needsMe)
      ? Math.max(0, Math.floor(parsed.needsMe))
      : undefined
  return { title, body, tag, url, projectId, threadId, needsMe }
}

async function trimCache(cacheName: string, maxEntries: number) {
  const cache = await sw.caches.open(cacheName)
  const keys = await cache.keys()
  if (keys.length <= maxEntries) return
  await Promise.all(keys.slice(0, keys.length - maxEntries).map((key) => cache.delete(key)))
}

async function networkFirstNavigation(request: Request): Promise<Response> {
  const cache = await sw.caches.open(PAGES_CACHE)
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), NETWORK_TIMEOUT_MS)
  try {
    const response = await fetch(request, { signal: controller.signal })
    clearTimeout(timer)
    if (response.status === 200) {
      await cache.put(request, response.clone())
      void trimCache(PAGES_CACHE, MAX_PAGE_ENTRIES)
    }
    return response
  } catch {
    clearTimeout(timer)
    const cached = (await cache.match(request)) ?? (await cache.match('/'))
    if (cached) return cached
    return fetch(request)
  }
}

async function cacheFirstAsset(request: Request): Promise<Response> {
  const cache = await sw.caches.open(ASSETS_CACHE)
  const cached = await cache.match(request)
  if (cached) return cached
  const response = await fetch(request)
  if (response.status === 200) {
    await cache.put(request, response.clone())
    void trimCache(ASSETS_CACHE, MAX_ASSET_ENTRIES)
  }
  return response
}

async function updateBadge(needsMe: number | undefined) {
  if (needsMe === undefined) return
  try {
    if (needsMe > 0 && typeof sw.navigator.setAppBadge === 'function') {
      await sw.navigator.setAppBadge(needsMe)
    } else if (needsMe === 0 && typeof sw.navigator.clearAppBadge === 'function') {
      await sw.navigator.clearAppBadge()
    }
  } catch {
    // Badging API is optional and may be rejected on unsupported platforms.
  }
}

if (typeof sw.addEventListener === 'function' && typeof sw.skipWaiting === 'function') {
  sw.addEventListener('install', (event) => {
    event.waitUntil(
      (async () => {
        const urls = precacheManifest
          .map((entry) => (typeof entry === 'string' ? entry : entry.url))
          .filter((item): item is string => typeof item === 'string' && item.length > 0)
        if (urls.length > 0) {
          const cache = await sw.caches.open(PRECACHE_NAME)
          await Promise.allSettled(urls.map((item) => cache.add(item)))
        }
        await sw.skipWaiting()
      })(),
    )
  })

  sw.addEventListener('activate', (event) => {
    event.waitUntil(
      (async () => {
        const names = await sw.caches.keys()
        await Promise.all(
          names.filter((name) => !ACTIVE_CACHES.has(name)).map((name) => sw.caches.delete(name)),
        )
        await sw.clients.claim()
      })(),
    )
  })

  sw.addEventListener('fetch', (event) => {
    const { request } = event
    if (request.method !== 'GET') return
    const url = new URL(request.url)
    if (url.origin !== sw.location.origin) return
    if (url.pathname.startsWith('/api/')) return

    if (request.mode === 'navigate') {
      event.respondWith(networkFirstNavigation(request))
      return
    }
    if (url.pathname.startsWith('/assets/')) {
      event.respondWith(cacheFirstAsset(request))
    }
  })

  sw.addEventListener('push', (event) => {
    // Always display a visible notification for every push event so iOS/macOS WebKit
    // never counts a silent push toward subscription revocation.
    const rawText = event.data?.text() ?? ''
    const payload = parsePushPayload(rawText, sw.location.origin)
    event.waitUntil(
      Promise.all([
        updateBadge(payload.needsMe),
        sw.registration.showNotification(payload.title, {
          body: payload.body,
          icon: '/icons/icon-192.png',
          badge: '/icons/icon-192.png',
          tag: payload.tag,
          data: {
            url: payload.url,
            projectId: payload.projectId,
            threadId: payload.threadId,
          },
        }),
      ]),
    )
  })

  sw.addEventListener('notificationclick', (event) => {
    event.notification.close()
    const data = (event.notification.data ?? {}) as {
      url?: string
      projectId?: string
      threadId?: string
    }
    const targetUrl = sanitizeTargetUrl(data.url, sw.location.origin)
    event.waitUntil(
      (async () => {
        const windowClients = await sw.clients.matchAll({
          type: 'window',
          includeUncontrolled: true,
        })
        const sameOrigin = windowClients.filter((client) => {
          try {
            return new URL(client.url).origin === sw.location.origin
          } catch {
            return false
          }
        })
        const chosen =
          sameOrigin.find((client) => client.focused) ??
          sameOrigin.find((client) => client.visibilityState === 'visible') ??
          sameOrigin[0]
        if (chosen) {
          await chosen.focus()
          const message = {
            type: 'ariadne:navigate',
            url: targetUrl,
            projectId: data.projectId,
            threadId: data.threadId,
          }
          // ServiceWorker WindowClient.postMessage takes a single message argument (no targetOrigin).
          // oxlint-disable-next-line unicorn/require-post-message-target-origin
          chosen.postMessage(message)
          return
        }
        await sw.clients.openWindow(targetUrl)
      })(),
    )
  })
}
