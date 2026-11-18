import { ExpiringCache } from '@ariadne/core/cache'
import { api } from '../api'

export const THUMBNAIL_LIMIT = 64
export const THUMBNAIL_TTL_MS = 5 * 60_000
export const MAX_THUMBNAIL_CHARS = 256_000
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
let running = false

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
  void renderWaiting()
  return picture
}

export function clearBoardThumbnails(): void {
  drawn.clear()
}

async function renderWaiting() {
  if (running) return
  running = true
  try {
    while (waiting.size) {
      const [id, entry] = waiting.entries().next().value!
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
    running = false
  }
}

async function draw(
  id: string,
  colorMode: 'light' | 'dark',
  signal: AbortSignal,
): Promise<string | null> {
  const [{ artifact }, { renderFlowSVG }, { whiteboardExtensions }] = await Promise.all([
    api.getArtifact(id, { signal, versions: false }),
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
