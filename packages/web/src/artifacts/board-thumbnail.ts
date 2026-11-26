import { ExpiringCache } from '@ariadne/core/cache'
import { api } from '../api'

export const THUMBNAIL_LIMIT = 128
export const THUMBNAIL_TTL_MS = 10 * 60_000
export const THUMBNAIL_CONCURRENCY = 3
export const MAX_THUMBNAIL_CHARS = 256_000
export const THUMBNAIL_ROOT_MARGIN = '160px 0px'

const visibilityListeners = new Map<Element, () => void>()
let visibilityObserver: IntersectionObserver | null = null

function ensureVisibilityObserver(): IntersectionObserver | null {
  if (typeof IntersectionObserver === 'undefined') return null
  if (!visibilityObserver) {
    visibilityObserver = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue
          const listener = visibilityListeners.get(entry.target)
          if (!listener) continue
          visibilityListeners.delete(entry.target)
          visibilityObserver?.unobserve(entry.target)
          listener()
        }
        if (visibilityListeners.size === 0 && visibilityObserver) {
          visibilityObserver.disconnect()
          visibilityObserver = null
        }
      },
      { rootMargin: THUMBNAIL_ROOT_MARGIN },
    )
  }
  return visibilityObserver
}

/**
 * Notify once when `element` enters or approaches the viewport. Shares a single
 * IntersectionObserver across all whiteboard cards; falls back immediately when unavailable.
 */
export function observeThumbnailVisibility(element: Element, onVisible: () => void): () => void {
  const observer = ensureVisibilityObserver()
  if (!observer) {
    onVisible()
    return () => {}
  }
  visibilityListeners.set(element, onVisible)
  observer.observe(element)
  return () => {
    visibilityListeners.delete(element)
    visibilityObserver?.unobserve(element)
    if (visibilityListeners.size === 0 && visibilityObserver) {
      visibilityObserver.disconnect()
      visibilityObserver = null
    }
  }
}
interface Thumbnail {
  key: string
  colorMode: 'light' | 'dark'
  picture: Promise<string | null>
  resolve(value: string | null): void
  controller: AbortController
}

const waiting = new Map<string, Thumbnail>()
const drawn = new ExpiringCache<string, Thumbnail>(
  THUMBNAIL_LIMIT,
  THUMBNAIL_TTL_MS,
  (id, entry) => {
    waiting.delete(id)
    entry.controller.abort()
    entry.resolve(null)
  },
)
let activeWorkers = 0
let rampScheduled = false

/** A bounded queue as well as a bounded cache: evicted jobs never start rendering. */
export function boardThumbnail(
  id: string,
  updatedAt: number,
  colorMode: 'light' | 'dark',
): Promise<string | null> {
  const key = `${updatedAt}:${colorMode}`
  const kept = drawn.get(id)
  if (kept?.key === key) return kept.picture
  let resolve!: Thumbnail['resolve']
  const picture = new Promise<string | null>((done) => {
    resolve = done
  })
  const entry = { key, colorMode, picture, resolve, controller: new AbortController() }
  drawn.set(id, entry)
  waiting.set(id, entry)
  scheduleWorkers()
  return picture
}

export function clearBoardThumbnails(): void {
  drawn.clear()
}

function scheduleWorkers() {
  // Start the first worker synchronously so callers observe immediate dispatch and
  // abort semantics, then ramp additional concurrent workers in a microtask so a
  // synchronous burst evicts stale items before filling extra slots.
  if (activeWorkers === 0 && waiting.size > 0) {
    void runWorker()
  }
  if (!rampScheduled && activeWorkers < THUMBNAIL_CONCURRENCY && waiting.size > 0) {
    rampScheduled = true
    queueMicrotask(() => {
      rampScheduled = false
      while (activeWorkers < THUMBNAIL_CONCURRENCY && waiting.size > 0) {
        void runWorker()
      }
    })
  }
}

async function runWorker() {
  activeWorkers++
  try {
    while (waiting.size > 0) {
      const next = waiting.entries().next()
      if (next.done) break
      const [id, entry] = next.value
      waiting.delete(id)
      const timer = setTimeout(() => entry.controller.abort(), 30_000)
      try {
        const picture = await draw(id, entry.colorMode, entry.controller.signal)
        entry.resolve(drawn.peek(id) === entry ? picture : null)
      } catch {
        entry.resolve(null)
      } finally {
        clearTimeout(timer)
      }
    }
  } finally {
    activeWorkers--
  }
}

async function draw(
  id: string,
  colorMode: 'light' | 'dark',
  signal: AbortSignal,
): Promise<string | null> {
  const [{ artifact }, { renderFlowSVG }, { whiteboardExtensions }] = await Promise.all([
    api.getArtifact(id, { signal, versions: false, lightweight: true }),
    import('@dou-ui/flow'),
    import('./whiteboard-extensions'),
    import('@dou-ui/flow/style.css'),
  ])
  if (signal.aborted) return null
  const board = JSON.parse(artifact.content) as Parameters<typeof renderFlowSVG>[0]
  if (!board.nodes.length) return null
  const svg = await renderFlowSVG(board, {
    extensions: whiteboardExtensions(),
    colorMode,
    padding: 0,
    background: false,
  })
  if (signal.aborted || svg.length > MAX_THUMBNAIL_CHARS) return null
  const picture = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`
  return picture.length <= MAX_THUMBNAIL_CHARS ? picture : null
}
