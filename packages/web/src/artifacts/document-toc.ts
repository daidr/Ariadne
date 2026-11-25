import type { PartialBlock } from '@dou-ui/editor'

/** A heading entry displayed in the document's left table of contents. */
export interface DocumentTocHeading {
  id: string
  /** Heading level (1–6). */
  level: number
  /** Visual indentation depth starting at 0 from the shallowest heading present. */
  depth: number
  text: string
}

export type DocumentTocMode = 'docked' | 'rail' | 'hidden'

export interface DocumentTocLayout {
  mode: DocumentTocMode
  /** Panel width in px when shown (docked or floating peek). */
  width: number
  /** Right edge of the docked TOC relative to the document shell's left edge, or 0 when not docked. */
  dockedRight: number
}

export interface VerticalRange {
  top: number
  bottom: number
}

export interface TableCollisionBox {
  /** Visible left edge of the rendered table in viewport coordinates: Math.max(frame.left, table.left). */
  visibleLeft: number
  /** Table frame top in viewport coordinates. */
  top: number
  /** Table frame bottom in viewport coordinates. */
  bottom: number
  /** Table frame height in px. */
  height: number
}

export interface TocCollisionInput {
  /** Right edge of the expanded (docked) TOC in viewport coordinates. */
  dockedTocRight: number
  /** Scroll container visible vertical range in viewport coordinates. */
  viewport: VerticalRange
  /** Expanded TOC vertical range in viewport coordinates, when known. */
  tocVertical?: VerticalRange | null
  /** Table frames currently rendered in the document. */
  tables: readonly TableCollisionBox[]
  /** Whether the TOC is already collapsed due to collision (applies exit hysteresis). */
  currentlyCollided: boolean
}

export interface HeadingPosition {
  id: string
  /** Heading top relative to the scroll container's visible top edge. */
  relativeTop: number
}

export interface ActiveHeadingInput {
  headings: readonly HeadingPosition[]
  /** Distance below the scroll container top where a heading counts as reached. */
  activationOffset?: number
  scrollTop: number
  clientHeight: number
  scrollHeight: number
}

export interface DocumentTocMiniBar {
  id: string
  depth: number
  active: boolean
}

/** Left inset of the docked TOC inside the document shell. */
export const TOC_LEFT = 16
/** Breathing room between the docked TOC and the reading column (keeps clear of DouEditor's block handle). */
export const TOC_READING_GAP = 48
/** Minimum width for the docked TOC before collapsing into the edge rail. */
export const TOC_MIN_WIDTH = 140
/** Maximum width for the docked TOC on wide viewports. */
export const TOC_MAX_WIDTH = 216
/** Minimum reading column left offset where the collapsed left-edge rail stays clear of the block handle. */
export const TOC_RAIL_MIN_READING_LEFT = 92
/** Width of the floating peek card when hovering or toggling the collapsed rail. */
export const TOC_PEEK_WIDTH = 216

/** Horizontal buffer (px) before a leftward-scrolling table triggers TOC collapse. */
export const TOC_COLLISION_ENTER_GAP = 16
/** Horizontal buffer (px) required before a collapsed TOC restores, avoiding edge jitter. */
export const TOC_COLLISION_EXIT_GAP = 28
/** Minimum visible height (px) for a scrolled table below the TOC box to still trigger collapse. */
export const TOC_TABLE_VISIBLE_MIN_PX = 40
/** Default vertical offset (px) below the scroll container top for scrollspy activation. */
export const TOC_ACTIVATION_OFFSET = 36

function readBlockPlainText(content: PartialBlock['content']): string {
  if (typeof content === 'string') return content.replace(/\s+/g, ' ').trim()
  if (!Array.isArray(content)) return ''
  let text = ''
  for (const item of content) {
    if (item.type === 'text') {
      text += item.text
    } else if (item.type === 'break') {
      text += ' '
    } else if (item.type === 'inline' && typeof item.props?.label === 'string') {
      text += item.props.label
    }
  }
  return text.replace(/\s+/g, ' ').trim()
}

/**
 * Extracts non-empty heading blocks in document order and normalizes their indentation depth
 * relative to the shallowest heading level present.
 */
export function extractDocumentHeadings(blocks: readonly PartialBlock[]): DocumentTocHeading[] {
  const raw: { id: string; level: number; text: string }[] = []
  const visit = (list: readonly PartialBlock[]) => {
    for (const block of list) {
      if (block.type === 'heading' && typeof block.id === 'string' && block.id.length > 0) {
        const text = readBlockPlainText(block.content)
        if (text) {
          const parsed = Number(block.props?.level)
          const level = Number.isFinite(parsed) ? Math.min(6, Math.max(1, Math.round(parsed))) : 1
          raw.push({ id: block.id, level, text })
        }
      }
      if (Array.isArray(block.children) && block.children.length > 0) {
        visit(block.children)
      }
    }
  }
  visit(blocks)
  if (!raw.length) return []
  const minLevel = Math.min(...raw.map((item) => item.level))
  return raw.map((item) => ({
    id: item.id,
    level: item.level,
    depth: item.level - minLevel,
    text: item.text,
  }))
}

/** Structural equality check to avoid redundant TOC updates while editing body text. */
export function sameDocumentHeadings(
  a: readonly DocumentTocHeading[],
  b: readonly DocumentTocHeading[],
): boolean {
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    const x = a[i]!
    const y = b[i]!
    if (x.id !== y.id || x.level !== y.level || x.depth !== y.depth || x.text !== y.text) {
      return false
    }
  }
  return true
}

/**
 * Chooses how the left TOC should display based on the measured pixel distance from the
 * document shell's left edge to the reading column's left edge. Never squeezes reading text.
 */
export function computeDocumentTocLayout(readingLeft: number): DocumentTocLayout {
  if (!Number.isFinite(readingLeft) || readingLeft <= 0) {
    return { mode: 'hidden', width: 0, dockedRight: 0 }
  }
  const available = readingLeft - TOC_LEFT - TOC_READING_GAP
  if (available >= TOC_MIN_WIDTH) {
    const width = Math.min(TOC_MAX_WIDTH, Math.floor(available))
    return { mode: 'docked', width, dockedRight: TOC_LEFT + width }
  }
  if (readingLeft >= TOC_RAIL_MIN_READING_LEFT) {
    return { mode: 'rail', width: TOC_PEEK_WIDTH, dockedRight: 0 }
  }
  return { mode: 'hidden', width: 0, dockedRight: 0 }
}

/**
 * Detects whether any visible wide table has scrolled leftward into the docked TOC zone.
 * Uses enter/exit hysteresis so pausing right on the threshold never causes rapid toggling.
 */
export function isTocCollidingWithTables(input: TocCollisionInput): boolean {
  const threshold =
    input.dockedTocRight +
    (input.currentlyCollided ? TOC_COLLISION_EXIT_GAP : TOC_COLLISION_ENTER_GAP)
  for (const table of input.tables) {
    if (table.height <= 0) continue
    const visibleHeight =
      Math.min(table.bottom, input.viewport.bottom) - Math.max(table.top, input.viewport.top)
    if (visibleHeight <= 0) continue
    const overlapsToc =
      !!input.tocVertical &&
      table.bottom > input.tocVertical.top &&
      table.top < input.tocVertical.bottom
    const meaningfullyVisible =
      visibleHeight >= Math.min(TOC_TABLE_VISIBLE_MIN_PX, table.height * 0.5)
    if (!overlapsToc && !meaningfullyVisible) continue
    if (table.visibleLeft <= threshold) return true
  }
  return false
}

/** Picks the currently active heading for the scrollspy indicator. */
export function pickActiveHeadingId(input: ActiveHeadingInput): string | null {
  if (!input.headings.length) return null
  if (input.scrollTop > 0 && input.scrollTop + input.clientHeight >= input.scrollHeight - 2) {
    return input.headings.at(-1)!.id
  }
  const cutoff = (input.activationOffset ?? TOC_ACTIVATION_OFFSET) + 4
  let active = input.headings[0]!.id
  for (const heading of input.headings) {
    if (heading.relativeTop <= cutoff) {
      active = heading.id
    }
  }
  return active
}

/**
 * Downsamples headings into compact mini-bars for the collapsed left-edge rail so very long
 * documents remain visually calm while always reflecting the active section.
 */
export function sampleTocMiniBars(
  headings: readonly DocumentTocHeading[],
  activeId: string | null,
  maxBars = 18,
): DocumentTocMiniBar[] {
  if (!headings.length) return []
  if (headings.length <= maxBars) {
    return headings.map((heading) => ({
      id: heading.id,
      depth: heading.depth,
      active: heading.id === activeId,
    }))
  }
  const indices: number[] = []
  const step = (headings.length - 1) / (maxBars - 1)
  for (let i = 0; i < maxBars; i++) {
    indices.push(Math.round(i * step))
  }
  const activeIndex = activeId ? headings.findIndex((heading) => heading.id === activeId) : -1
  let activeBarIndex = -1
  if (activeIndex >= 0) {
    let bestDist = Infinity
    for (let i = 0; i < indices.length; i++) {
      const dist = Math.abs(indices[i]! - activeIndex)
      if (dist < bestDist) {
        bestDist = dist
        activeBarIndex = i
      }
    }
  }
  return indices.map((headingIndex, barIndex) => {
    const heading = headings[headingIndex]!
    return {
      id: heading.id,
      depth: heading.depth,
      active: barIndex === activeBarIndex,
    }
  })
}
