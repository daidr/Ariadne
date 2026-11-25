import { reactive } from 'vue'
import { api, type PushSubscriptionView } from './api'
import { isStandalonePwa } from './pwa'

export type PushUnsupportedReason = 'insecure' | 'ios-install' | 'unsupported'

export interface PushSupport {
  supported: boolean
  reason: PushUnsupportedReason | null
}

interface BadgeCapableNavigator {
  setAppBadge?: (contents?: number) => Promise<void>
  clearAppBadge?: () => Promise<void>
}

export interface PushSupportEnvironment {
  isSecureContext: boolean
  userAgent: string
  maxTouchPoints: number
  standalone: boolean
  hasServiceWorker: boolean
  hasPushManager: boolean
  hasNotification: boolean
}

function readBrowserEnvironment(): PushSupportEnvironment {
  const hasWindow = typeof window !== 'undefined'
  const hasNavigator = typeof navigator !== 'undefined'
  return {
    isSecureContext: hasWindow ? window.isSecureContext : false,
    userAgent: hasNavigator ? navigator.userAgent : '',
    maxTouchPoints: hasNavigator ? (navigator.maxTouchPoints ?? 0) : 0,
    standalone: hasWindow ? isStandalonePwa() : false,
    hasServiceWorker: hasNavigator && 'serviceWorker' in navigator,
    hasPushManager: hasWindow && 'PushManager' in window,
    hasNotification: typeof Notification !== 'undefined',
  }
}

export function isAppleMobile(userAgent: string, maxTouchPoints = 0): boolean {
  if (/iPad|iPhone|iPod/.test(userAgent)) return true
  return /Macintosh/.test(userAgent) && maxTouchPoints > 1
}

/** Determine whether the current browser context can subscribe to Web Push. */
export function detectPushSupport(
  env: PushSupportEnvironment = readBrowserEnvironment(),
): PushSupport {
  if (!env.isSecureContext) {
    return { supported: false, reason: 'insecure' }
  }
  if (isAppleMobile(env.userAgent, env.maxTouchPoints) && !env.standalone && !env.hasPushManager) {
    return { supported: false, reason: 'ios-install' }
  }
  if (!env.hasServiceWorker || !env.hasPushManager || !env.hasNotification) {
    if (isAppleMobile(env.userAgent, env.maxTouchPoints) && !env.standalone) {
      return { supported: false, reason: 'ios-install' }
    }
    return { supported: false, reason: 'unsupported' }
  }
  return { supported: true, reason: null }
}

/** Decode a URL-safe base64 VAPID public key into an ArrayBuffer-backed Uint8Array. */
export function urlBase64ToUint8Array(base64Url: string): Uint8Array<ArrayBuffer> {
  const trimmed = base64Url.trim()
  const padding = '='.repeat((4 - (trimmed.length % 4)) % 4)
  const base64 = (trimmed + padding).replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(base64)
  const buffer = new ArrayBuffer(raw.length)
  const bytes = new Uint8Array(buffer)
  for (let i = 0; i < raw.length; i++) {
    bytes[i] = raw.charCodeAt(i)
  }
  return bytes
}

/** Encode an ArrayBuffer (e.g. PushSubscription p256dh/auth key) as unpadded base64url. */
export function arrayBufferToBase64Url(buffer: ArrayBuffer | null): string {
  if (!buffer) return ''
  const bytes = new Uint8Array(buffer)
  let binary = ''
  for (const byte of bytes) {
    binary += String.fromCharCode(byte)
  }
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '')
}

/** Build a short, readable default label for the current browser/device. */
export function defaultDeviceLabel(
  userAgent = typeof navigator !== 'undefined' ? navigator.userAgent : '',
  maxTouchPoints = typeof navigator !== 'undefined' ? (navigator.maxTouchPoints ?? 0) : 0,
): string {
  let os = ''
  if (/iPhone/.test(userAgent)) os = 'iPhone'
  else if (/iPad/.test(userAgent) || (/Macintosh/.test(userAgent) && maxTouchPoints > 1))
    os = 'iPad'
  else if (/Android/.test(userAgent)) os = 'Android'
  else if (/Mac OS X|Macintosh/.test(userAgent)) os = 'macOS'
  else if (/Windows/.test(userAgent)) os = 'Windows'
  else if (/Linux/.test(userAgent)) os = 'Linux'

  let browser = ''
  if (/Edg\//.test(userAgent)) browser = 'Edge'
  else if (/Firefox\//.test(userAgent)) browser = 'Firefox'
  else if (/Chrome\//.test(userAgent) || /CriOS\//.test(userAgent)) browser = 'Chrome'
  else if (/Safari\//.test(userAgent)) browser = 'Safari'

  const parts = [os, browser].filter(Boolean)
  return parts.length ? parts.join(' · ') : 'Browser'
}

function readPermission(): NotificationPermission | 'unsupported' {
  return typeof Notification !== 'undefined' ? Notification.permission : 'unsupported'
}

export const pushState = reactive<{
  endpoint: string | null
  serverActive: boolean
  permission: NotificationPermission | 'unsupported'
  busy: boolean
}>({
  endpoint: null,
  serverActive: false,
  permission: readPermission(),
  busy: false,
})

/** True when this browser has an active server-side Web Push subscription. */
export function isWebPushActiveForDevice(): boolean {
  return pushState.serverActive
}

async function currentRegistration(): Promise<ServiceWorkerRegistration | null> {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return null
  try {
    return (await navigator.serviceWorker.getRegistration()) ?? null
  } catch {
    return null
  }
}

/** Sync local browser PushSubscription state and optional server-side active status. */
export async function syncBrowserPushState(options?: {
  subscriptions?: PushSubscriptionView[]
  webPushActive?: boolean
}): Promise<string | null> {
  pushState.permission = readPermission()
  const reg = await currentRegistration()
  let endpoint: string | null = null
  if (reg?.pushManager) {
    try {
      const sub = await reg.pushManager.getSubscription()
      endpoint = sub?.endpoint ?? null
    } catch {
      endpoint = null
    }
  }
  pushState.endpoint = endpoint
  if (options?.subscriptions !== undefined) {
    pushState.serverActive = Boolean(
      options.webPushActive &&
      endpoint &&
      options.subscriptions.some((s) => s.endpoint === endpoint),
    )
  }
  return endpoint
}

/** Lightweight startup sync so desktop notifications deduplicate cleanly against active Web Push. */
export async function refreshPushActiveFromServer(): Promise<void> {
  try {
    const settings = await api.getNotificationSettings()
    await syncBrowserPushState({
      subscriptions: settings.subscriptions,
      webPushActive: settings.enabled && settings.webPushEnabled,
    })
  } catch {
    await syncBrowserPushState()
  }
}

export type SubscribeDeviceResult =
  | { ok: true; subscription: PushSubscriptionView | null; subscriptions: PushSubscriptionView[] }
  | { ok: false; reason: PushUnsupportedReason | 'denied' | 'missing-key' }

function sameApplicationServerKey(
  existing: ArrayBuffer | null | undefined,
  expected: Uint8Array,
): boolean {
  if (!existing || existing.byteLength !== expected.byteLength) return false
  const actual = new Uint8Array(existing)
  for (let i = 0; i < actual.length; i++) {
    if (actual[i] !== expected[i]) return false
  }
  return true
}

/** Request permission if needed, subscribe this browser's PushManager, and register on the server. */
export async function subscribeCurrentDevice(
  vapidPublicKey: string,
  label?: string,
): Promise<SubscribeDeviceResult> {
  const support = detectPushSupport()
  if (!support.supported) {
    return { ok: false, reason: support.reason ?? 'unsupported' }
  }
  if (!vapidPublicKey.trim()) {
    return { ok: false, reason: 'missing-key' }
  }

  pushState.busy = true
  try {
    let permission = Notification.permission
    if (permission === 'default') {
      permission = await Notification.requestPermission()
    }
    pushState.permission = permission
    if (permission !== 'granted') {
      return { ok: false, reason: 'denied' }
    }

    const registration =
      (await navigator.serviceWorker.getRegistration()) ?? (await navigator.serviceWorker.ready)
    const applicationServerKey = urlBase64ToUint8Array(vapidPublicKey)

    let subscription = await registration.pushManager.getSubscription()
    if (
      subscription &&
      !sameApplicationServerKey(subscription.options?.applicationServerKey, applicationServerKey)
    ) {
      await subscription.unsubscribe().catch(() => {})
      subscription = null
    }
    if (!subscription) {
      subscription = await registration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey,
      })
    }

    const p256dh = arrayBufferToBase64Url(subscription.getKey('p256dh'))
    const auth = arrayBufferToBase64Url(subscription.getKey('auth'))
    const response = await api.subscribePush({
      endpoint: subscription.endpoint,
      p256dh,
      auth,
      label: (label?.trim() || defaultDeviceLabel()).slice(0, 80),
      userAgent: navigator.userAgent,
    })
    pushState.endpoint = subscription.endpoint
    pushState.serverActive = true
    return {
      ok: true,
      subscription: response.subscription,
      subscriptions: response.subscriptions,
    }
  } finally {
    pushState.busy = false
  }
}

/** Unsubscribe this browser from PushManager and remove the endpoint from the server. */
export async function unsubscribeCurrentDevice(): Promise<PushSubscriptionView[]> {
  pushState.busy = true
  try {
    const reg = await currentRegistration()
    const subscription = reg?.pushManager ? await reg.pushManager.getSubscription() : null
    const endpoint = subscription?.endpoint ?? pushState.endpoint
    let subscriptions: PushSubscriptionView[] = []
    if (endpoint) {
      const response = await api.unsubscribePush(endpoint)
      subscriptions = response.subscriptions
    }
    if (subscription) {
      await subscription.unsubscribe().catch(() => {})
    }
    pushState.endpoint = null
    pushState.serverActive = false
    return subscriptions
  } finally {
    pushState.busy = false
  }
}

/** Keep the PWA app badge in sync with the total needsMe counter. */
export function syncAppBadge(waiting: number): void {
  if (typeof navigator === 'undefined') return
  const nav = navigator as unknown as BadgeCapableNavigator
  try {
    if (waiting > 0 && typeof nav.setAppBadge === 'function') {
      void nav.setAppBadge(waiting).catch(() => {})
    } else if (waiting <= 0 && typeof nav.clearAppBadge === 'function') {
      void nav.clearAppBadge().catch(() => {})
    }
  } catch {
    // Ignore unsupported badge environments.
  }
}

export interface ServiceWorkerNavigationTarget {
  url?: string
  projectId?: string
  threadId?: string
}

export interface NotificationNavigationActions {
  currentProjectId: () => string | null
  hasFullView: () => boolean
  openProject: (projectId: string) => Promise<void>
  openThread: (threadId: string) => Promise<void>
  closeFull: () => void
  openCoordinator: () => void
}

/**
 * Route an in-app notification click to its project/thread or back to the coordinator
 * conversation (closing any full-screen overlay so narrow PWAs reveal the target view).
 */
export async function navigateToNotificationTarget(
  target: ServiceWorkerNavigationTarget,
  actions: NotificationNavigationActions,
): Promise<void> {
  const { projectId, threadId } = target
  if (projectId && actions.currentProjectId() !== projectId) {
    await actions.openProject(projectId)
  }
  if (threadId) {
    if (actions.hasFullView()) actions.closeFull()
    await actions.openThread(threadId)
    return
  }
  if (projectId) {
    if (actions.hasFullView()) actions.closeFull()
    actions.openCoordinator()
  }
}

/** Listen for navigation messages dispatched by the Service Worker on notification clicks. */
export function watchServiceWorkerNavigation(
  onNavigate: (target: ServiceWorkerNavigationTarget) => void,
): () => void {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) {
    return () => {}
  }
  const handler = (event: MessageEvent) => {
    const data = event.data as Record<string, unknown> | null | undefined
    if (!data || data.type !== 'ariadne:navigate') return
    onNavigate({
      url: typeof data.url === 'string' ? data.url : undefined,
      projectId: typeof data.projectId === 'string' ? data.projectId : undefined,
      threadId: typeof data.threadId === 'string' ? data.threadId : undefined,
    })
  }
  navigator.serviceWorker.addEventListener('message', handler)
  return () => navigator.serviceWorker.removeEventListener('message', handler)
}
