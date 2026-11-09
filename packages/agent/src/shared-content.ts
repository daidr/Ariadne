import { pendingBlock, type WhiteboardContent } from '@ariadne/core'
import { blockElement, defineEditorExtension, editorKit, textBlock } from '@dou-ui/editor'
import { fillMap, readMap, updateMap } from '@dou-ui/editor/collaboration'
import { blocksToMarkdown, markdownToBlocks, type DOMParserLike } from '@dou-ui/editor/markdown'
import { fillMaps, readMaps, updateMaps } from '@dou-ui/flow/collaboration'
import { Window } from 'happy-dom'
import * as Y from 'yjs'

/**
 * How each kind edited together lives in a Yjs document, in the places the dou-ui
 * components' `collaboration` read it from: a document's blocks in the `document` map, a
 * whiteboard's nodes and edges in the `nodes` and `edges` maps. The pages use the same
 * names (see the web's shared editing).
 */
export const sharedKinds = ['document', 'whiteboard'] as const
export type SharedKind = (typeof sharedKinds)[number]

/** A change to one of the shared types, as Yjs reports it. */
export type SharedEvent = Parameters<Parameters<Y.Map<unknown>['observeDeep']>[0]>[0][number]

export interface SharedBridge {
  /** Write the content into the empty document. */
  seed(doc: Y.Doc, content: string): void
  /** The content the document holds, as the artifact stores it. */
  read(doc: Y.Doc): string
  /**
   * Change the document from `base` (what is in it now when left out) to `content`,
   * touching only what differs, so others' changes since `base` stay.
   */
  update(doc: Y.Doc, content: string, base?: string): void
  /** The shared types a change of the content lands in. */
  parts(doc: Y.Doc): Y.Map<unknown>[]
  /**
   * Where a writer is after a change, as the others' editors read it from awareness: the
   * fields to share, from what the change did to `parts`; `null` when it shows nowhere.
   */
  presence(doc: Y.Doc, events: SharedEvent[]): Record<string, unknown> | null
}

/** The blocks a document is written in: the editor's own and the placeholder. */
export const documentExtensions = [
  editorKit(),
  pendingBlock({ defineEditorExtension, textBlock, blockElement }),
]
const extensions = documentExtensions

/** The server has no browser: Markdown is read into blocks through happy-dom's HTML reader. */
let reader: DOMParserLike | null = null
/** The last texts read: a document written piece by piece reads each piece twice. */
const read = new Map<string, ReturnType<typeof markdownToBlocks>>()
const blocksOf = (markdown: string) => {
  const known = read.get(markdown)
  if (known) return known
  if (!reader) {
    const window = new Window()
    reader = new window.DOMParser() as unknown as DOMParserLike
  }
  const blocks = markdownToBlocks(markdown, extensions, { domParser: reader })
  if (read.size >= 4) read.delete(read.keys().next().value!)
  read.set(markdown, blocks)
  return blocks
}

/** Where in a text a change ended: after what it put in, or where it took out. */
function endOf(delta: { insert?: unknown; retain?: number; delete?: number }[]): number {
  let at = 0
  let end = 0
  for (const op of delta) {
    if (op.retain) at += op.retain
    else if (op.insert !== undefined) {
      at += typeof op.insert === 'string' ? op.insert.length : 1
      end = at
    } else if (op.delete) end = at
  }
  return end
}

/** A block's place in the document as order keys from the top level down. */
function placeOf(map: Y.Map<unknown>, id: string): string[] {
  const keys: string[] = []
  let at: unknown = id
  for (let depth = 0; typeof at === 'string' && depth < 64; depth++) {
    const block: unknown = map.get(at)
    if (!(block instanceof Y.Map)) break
    keys.unshift(String(block.get('order') ?? ''))
    at = block.get('parent')
  }
  return keys
}

const later = (a: string[], b: string[]) => {
  for (let i = 0; i < Math.min(a.length, b.length); i++) {
    if (a[i] !== b[i]) return a[i]! > b[i]!
  }
  return a.length > b.length
}

/**
 * The cursor after a change to a document: at the end of what was written last, furthest
 * down. The editor has no way yet for a writer without an editor to show its cursor
 * (daidr/dou-ui#409), so this writes the `cursor` field its editors share among themselves.
 */
function documentCursor(doc: Y.Doc, events: SharedEvent[]) {
  const map = doc.getMap('document')
  type Spot = { block: string; cell?: string; text: Y.Text; index: number }
  const spots: Spot[] = []
  /** Where a table cell is in its table: its row's place, then its column's. */
  const cellPlace = (block: string, cell: string): string[] => {
    const stored = map.get(block)
    if (!(stored instanceof Y.Map)) return []
    const [row = '', column = ''] = cell.split(':')
    const order = (field: string, id: string) => {
      const entry = (stored.get(field) as Y.Map<unknown> | undefined)?.get(id)
      return entry instanceof Y.Map ? String(entry.get('order') ?? '') : ''
    }
    return [order('rows', row), order('columns', column)]
  }
  /** The end of a block's words: of a table, of its last cell with any. */
  const endOfBlock = (block: string) => {
    const stored = map.get(block)
    if (!(stored instanceof Y.Map)) return
    const cells = stored.get('cells')
    if (cells instanceof Y.Map) {
      let last: Spot | null = null
      for (const [cell, text] of cells.entries()) {
        if (!(text instanceof Y.Text) || !text.length) continue
        if (!last || later(cellPlace(block, cell), cellPlace(block, last.cell!))) {
          last = { block, cell, text, index: text.length }
        }
      }
      if (last) return void spots.push(last)
    }
    const text = stored.get('text')
    if (text instanceof Y.Text) spots.push({ block, text, index: text.length })
  }
  for (const event of events) {
    const [block, field, cell] = event.path
    if (!event.path.length) {
      for (const [id, change] of event.changes.keys) if (change.action !== 'delete') endOfBlock(id)
    } else if (typeof block !== 'string') {
      continue
    } else if (event.target instanceof Y.Text && field === 'text') {
      spots.push({ block, text: event.target, index: endOf(event.delta as never) })
    } else if (event.target instanceof Y.Text && field === 'cells' && typeof cell === 'string') {
      spots.push({ block, cell, text: event.target, index: endOf(event.delta as never) })
    } else if (field === 'cells' || field === 'rows' || field === 'columns') {
      // Cells put in whole, or rows added: the table's last words.
      endOfBlock(block)
    } else if (event.path.length === 1 && event.keys.has('text')) {
      endOfBlock(block)
    }
  }
  // The furthest down: by the block's place, then inside a table by the cell's. A table's
  // own text is empty and above its cells.
  const placeIn = (spot: Spot) => (spot.cell ? ['1', ...cellPlace(spot.block, spot.cell)] : ['0'])
  let last: Spot | null = null
  let lastPlace: string[] = []
  for (const spot of spots) {
    if (!map.has(spot.block)) continue
    const place = placeOf(map, spot.block)
    if (last) {
      const same = spot.block === last.block
      if (same ? later(placeIn(last), placeIn(spot)) : !later(place, lastPlace)) continue
    }
    last = spot
    lastPlace = place
  }
  if (!last) return null
  const position = Y.relativePositionToJSON(
    Y.createRelativePositionFromTypeIndex(last.text, Math.min(last.index, last.text.length)),
  )
  const place = { block: last.block, ...(last.cell ? { cell: last.cell } : {}), position }
  return { cursor: { anchor: place, head: place } }
}

const documentBridge: SharedBridge = {
  seed(doc, content) {
    fillMap(doc.getMap('document'), { version: 1, blocks: blocksOf(content) }, { extensions })
  },
  read(doc) {
    return blocksToMarkdown(
      readMap(doc.getMap('document'), { extensions }).blocks as never,
      extensions,
    )
  },
  update(doc, content, base) {
    updateMap(
      doc.getMap('document'),
      { version: 1, blocks: blocksOf(content) },
      {
        extensions,
        ...(base === undefined ? {} : { base: { version: 1, blocks: blocksOf(base) } }),
      },
    )
  },
  parts: (doc) => [doc.getMap('document')],
  presence: documentCursor,
}

/** The fields of a node that say where it is, not what it is. */
const PLACE = new Set(['x', 'y', 'width', 'height', 'z'])

const boardMaps = (doc: Y.Doc) => ({ nodes: doc.getMap('nodes'), edges: doc.getMap('edges') })

const whiteboardBridge: SharedBridge = {
  seed(doc, content) {
    fillMaps(boardMaps(doc), JSON.parse(content) as never)
  },
  read(doc) {
    return JSON.stringify(readMaps(boardMaps(doc)) as WhiteboardContent)
  },
  update(doc, content, base) {
    updateMaps(
      boardMaps(doc),
      JSON.parse(content) as never,
      base === undefined ? {} : { base: JSON.parse(base) as never },
    )
  },
  parts: (doc) => [doc.getMap('nodes'), doc.getMap('edges')],
  // The items written, outlined as a selection (`FlowPresence`); one that was only moved
  // out of another's way is not where the writer is.
  presence(doc, events) {
    const nodes = doc.getMap('nodes')
    const ids = new Set<string>()
    for (const event of events) {
      const [id] = event.path
      if (typeof id !== 'string') {
        for (const [key, change] of event.changes.keys) if (change.action !== 'delete') ids.add(key)
      } else if (event.path.length > 1 || [...event.keys.keys()].some((key) => !PLACE.has(key))) {
        ids.add(id)
      }
    }
    const written = [...ids].filter((id) => nodes.has(id) && !doc.getMap('edges').has(id))
    if (!written.length) return null
    return { flow: { moving: [], selection: { nodes: written, edges: [] }, path: null } }
  },
}

export const bridges: Record<SharedKind, SharedBridge> = {
  document: documentBridge,
  whiteboard: whiteboardBridge,
}
