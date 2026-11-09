import type { WhiteboardContent } from '@ariadne/core'
import { editorKit } from '@dou-ui/editor'
import { fillMap, readMap, updateMap } from '@dou-ui/editor/collaboration'
import { blocksToMarkdown, markdownToBlocks, type DOMParserLike } from '@dou-ui/editor/markdown'
import { fillMaps, readMaps, updateMaps } from '@dou-ui/flow/collaboration'
import { Window } from 'happy-dom'
import type * as Y from 'yjs'

/**
 * How each kind edited together lives in a Yjs document, in the places the dou-ui
 * components' `collaboration` read it from: a document's blocks in the `document` map, a
 * whiteboard's nodes and edges in the `nodes` and `edges` maps. The pages use the same
 * names (see the web's shared editing).
 */
export const sharedKinds = ['document', 'whiteboard'] as const
export type SharedKind = (typeof sharedKinds)[number]

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
}

const extensions = [editorKit()]

/** The server has no browser: Markdown is read into blocks through happy-dom's HTML reader. */
let reader: DOMParserLike | null = null
const blocksOf = (markdown: string) => {
  if (!reader) {
    const window = new Window()
    reader = new window.DOMParser() as unknown as DOMParserLike
  }
  return markdownToBlocks(markdown, extensions, { domParser: reader })
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
}

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
}

export const bridges: Record<SharedKind, SharedBridge> = {
  document: documentBridge,
  whiteboard: whiteboardBridge,
}
