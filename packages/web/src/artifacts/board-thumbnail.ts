import { api } from '../api'

/**
 * A whiteboard drawn as a picture for its card, by `@dou-ui/flow` the way the editor draws
 * it. One board is drawn at a time; a picture is kept until the board changes.
 */
const drawn = new Map<string, { key: string; picture: Promise<string | null> }>()
let queue: Promise<unknown> = Promise.resolve()

export function boardThumbnail(
  id: string,
  updatedAt: number,
  colorMode: 'light' | 'dark',
): Promise<string | null> {
  const key = `${updatedAt}:${colorMode}`
  const kept = drawn.get(id)
  if (kept?.key === key) return kept.picture
  const picture = queue.then(() => draw(id, colorMode)).catch(() => null)
  queue = picture
  drawn.set(id, { key, picture })
  return picture
}

async function draw(id: string, colorMode: 'light' | 'dark'): Promise<string | null> {
  const [{ artifact }, { renderFlowSVG }, { whiteboardExtensions }] = await Promise.all([
    api.getArtifact(id),
    import('@dou-ui/flow'),
    import('./whiteboard-extensions'),
    import('@dou-ui/flow/style.css'),
  ])
  const board = JSON.parse(artifact.content) as Parameters<typeof renderFlowSVG>[0]
  if (!board.nodes.length) return null
  const svg = await renderFlowSVG(board, {
    extensions: whiteboardExtensions(),
    colorMode,
    padding: 0,
    background: false,
  })
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`
}
