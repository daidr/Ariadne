import {
  fileUrl,
  MAX_FILE_BYTES,
  type WhiteboardContent,
  type WhiteboardEdge,
  type WhiteboardNode,
} from '@ariadne/core'
import { imageType } from './message-images'
import { ToolInputError, type ToolContext } from './tool-helpers'

/**
 * Options laid out on a whiteboard for the user to compare and pick: each one a frame
 * with its title, its screenshots one under another and a few lines of text, the frames
 * side by side. A new version of an option goes under the one it revises, joined by a
 * dashed connector, so the user's marks on the first stay where they are.
 */

export const OPTION_SCHEMA = {
  type: 'object',
  properties: {
    id: { type: 'string' },
    title: { type: 'string', description: 'Short: what sets this option apart.' },
    note: { type: 'string', description: 'One line beside the title.' },
    text: { type: 'string', description: 'A few lines under the pictures, if they need it.' },
    images: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          path: {
            type: 'string',
            description: 'A PNG, JPEG, GIF or WebP in your workspace, such as a screenshot.',
          },
          file: {
            type: 'string',
            description: 'Or a file:fil_… image already in the project, e.g. from a thread.',
          },
          alt: { type: 'string', description: 'What it shows.' },
        },
      },
    },
    revises: {
      type: 'string',
      description: 'The option this is a new version of; it goes under that one.',
    },
  },
  required: ['id', 'title'],
} as const

const WIDTH = 360
const PAD = 16
const HEAD = 40
const GAP = 12
const BETWEEN = 48
const TEXT_LINE = 22

interface Picture {
  url: string
  alt: string
  width: number
  height: number
}

/** The size of a PNG, GIF, JPEG or WebP from its first bytes; `null` when it can't tell. */
export function imageSize(b: Uint8Array): { width: number; height: number } | null {
  const view = new DataView(b.buffer, b.byteOffset, b.byteLength)
  try {
    if (b[0] === 0x89 && b[1] === 0x50)
      return { width: view.getUint32(16), height: view.getUint32(20) }
    if (b[0] === 0x47 && b[1] === 0x49) {
      return { width: view.getUint16(6, true), height: view.getUint16(8, true) }
    }
    if (b[0] === 0xff && b[1] === 0xd8) {
      for (let at = 2; at + 9 < b.length;) {
        if (b[at] !== 0xff) return null
        const marker = b[at + 1]!
        const length = view.getUint16(at + 2)
        // Start-of-frame markers carry the size; not the ones for tables and colour.
        if (marker >= 0xc0 && marker <= 0xcf && ![0xc4, 0xc8, 0xcc].includes(marker)) {
          return { width: view.getUint16(at + 7), height: view.getUint16(at + 5) }
        }
        at += 2 + length
      }
      return null
    }
    if (b[0] === 0x52 && b[8] === 0x57) {
      const chunk = String.fromCharCode(b[12]!, b[13]!, b[14]!, b[15]!)
      if (chunk === 'VP8X') {
        const w = 1 + (b[24]! | (b[25]! << 8) | (b[26]! << 16))
        const h = 1 + (b[27]! | (b[28]! << 8) | (b[29]! << 16))
        return { width: w, height: h }
      }
      if (chunk === 'VP8 ') {
        return {
          width: view.getUint16(26, true) & 0x3fff,
          height: view.getUint16(28, true) & 0x3fff,
        }
      }
      if (chunk === 'VP8L') {
        const bits = view.getUint32(21, true)
        return { width: (bits & 0x3fff) + 1, height: ((bits >> 14) & 0x3fff) + 1 }
      }
    }
  } catch {
    return null
  }
  return null
}

/** An image the model named, kept in the project's files. */
async function picture(ctx: ToolContext, given: Record<string, unknown>): Promise<Picture> {
  const alt = typeof given.alt === 'string' ? given.alt : ''
  if (typeof given.file === 'string' && given.file) {
    const id = given.file.replace(/^file:/, '')
    let data: Uint8Array
    try {
      const file = ctx.app.getFile(id)
      if (!file.mime.startsWith('image/')) throw new Error('not an image')
      data = file.data
    } catch {
      throw new ToolInputError(`"${given.file}" is not an image of this project`)
    }
    const size = imageSize(data)
    if (!size) throw new ToolInputError(`could not read the size of "${given.file}"`)
    return { url: fileUrl(id), alt, ...size }
  }
  const path = typeof given.path === 'string' ? given.path.trim() : ''
  if (!path) throw new ToolInputError('every image needs a "path" or a "file"')
  const type = imageType(path)
  if (!type) throw new ToolInputError(`"${path}" is not a PNG, JPEG, GIF or WebP`)
  if (ctx.role !== 'thread') {
    throw new ToolInputError(
      'You have no workspace: name images by the file:fil_… links threads sent instead.',
    )
  }
  let data: Uint8Array
  try {
    data = await (await ctx.workspace()).readBytes(path, MAX_FILE_BYTES)
  } catch (error) {
    throw new ToolInputError(`could not read "${path}": ${(error as Error).message}`)
  }
  if (!type.magic(data)) throw new ToolInputError(`"${path}" is not the image its name says`)
  const size = imageSize(data)
  if (!size) throw new ToolInputError(`could not read the size of "${path}"`)
  const stored = ctx.app.saveFile(ctx.projectId, { mime: type.mime, data })
  return { url: stored.url, alt, ...size }
}

const str = (value: unknown) => (typeof value === 'string' ? value : undefined)

/** Lines of text as they wrap in a frame, counting about 26 characters a line. */
function lines(text: string): number {
  return text.split('\n').reduce((n, line) => n + Math.max(1, Math.ceil([...line].length / 26)), 0)
}

/**
 * The options the model gave, as frames with what is inside them, added to `board`: new
 * ones in a row to the right of everything (a revision under the option it revises), one
 * given again (by id) redrawn in its place. The user's marks inside an option stay.
 */
export async function placeOptions(
  ctx: ToolContext,
  board: WhiteboardContent,
  given: Record<string, unknown>[],
  pick: 'one' | 'many' | undefined,
): Promise<WhiteboardContent> {
  const nodes = [...board.nodes]
  const edges = [...board.edges]
  const right = () =>
    nodes.reduce((max, n) => Math.max(max, n.x + (n.width ?? 0)), Number.NEGATIVE_INFINITY)
  for (const option of given) {
    const id = str(option.id)
    const title = str(option.title)?.trim()
    if (!id || !title) throw new ToolInputError('every option needs an "id" and a "title"')
    const images = Array.isArray(option.images) ? option.images : []
    const pictures: Picture[] = []
    for (const image of images)
      pictures.push(await picture(ctx, (image ?? {}) as Record<string, unknown>))
    const text = str(option.text)?.trim() ?? ''

    const existing = nodes.find((n) => n.id === id)
    if (existing && existing.type !== 'option') {
      throw new ToolInputError(`"${id}" is already an item that is not an option`)
    }
    const revises = str(option.revises)
    const base = revises ? nodes.find((n) => n.id === revises && n.type === 'option') : undefined
    if (revises && !base) throw new ToolInputError(`there is no option "${revises}" to revise`)
    // Where the frame goes: where it was, under the one it revises, or right of the rest.
    const x = existing?.x ?? base?.x ?? (nodes.length ? right() + BETWEEN : 0)
    const y =
      existing?.y ??
      (base
        ? base.y + (base.height ?? 0) + BETWEEN
        : (nodes.find((n) => n.type === 'option')?.y ?? 0))

    // What was inside before and is the model's (images, the text) goes; the user's stays.
    const mine = new Set([
      `${id}-text`,
      ...Array.from({ length: 50 }, (_, i) => `${id}-image-${i + 1}`),
    ])
    for (let i = nodes.length - 1; i >= 0; i--) if (mine.has(nodes[i]!.id)) nodes.splice(i, 1)

    let at = y + HEAD
    const inner: WhiteboardNode[] = []
    for (const [i, p] of pictures.entries()) {
      const width = WIDTH - 2 * PAD
      const height = Math.round((p.height / p.width) * width)
      inner.push({
        id: `${id}-image-${i + 1}`,
        type: 'image',
        x: x + PAD,
        y: at,
        width,
        height,
        parent: id,
        data: { src: p.url, ...(p.alt ? { alt: p.alt } : {}) },
      })
      at += height + GAP
    }
    if (text) {
      const height = lines(text) * TEXT_LINE + 8
      inner.push({
        id: `${id}-text`,
        type: 'text',
        x: x + PAD,
        y: at,
        width: WIDTH - 2 * PAD,
        height,
        parent: id,
        data: { text },
      })
      at += height + GAP
    }
    const height = Math.max(at - y + PAD - (inner.length ? GAP : 0), 120)
    const data: Record<string, unknown> = {
      ...existing?.data,
      title,
      ...(str(option.note) !== undefined ? { note: str(option.note) } : {}),
      ...(pick === 'many' ? { pick: 'many' } : {}),
    }
    const frame: WhiteboardNode = {
      ...existing,
      id,
      type: 'option',
      x,
      y,
      width: WIDTH,
      // Keep room for what the user put below the pictures.
      height: Math.max(height, existing?.height ?? 0),
      data,
    }
    if (existing) nodes.splice(nodes.indexOf(existing), 1, frame)
    else nodes.push(frame)
    nodes.push(...inner)
    if (base && !edges.some((e) => e.source.node === base.id && e.target.node === id)) {
      edges.push({
        id: `${base.id}-${id}`,
        type: 'connector',
        source: { node: base.id, port: 'bottom' },
        target: { node: id, port: 'top-in' },
        data: { route: 'straight', dash: 'dashed' },
      } satisfies WhiteboardEdge)
    }
  }
  return { version: 1, nodes, edges }
}
