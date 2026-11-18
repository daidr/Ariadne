import { defineEditorExtension } from '@dou-ui/editor'
import type { Node } from 'prosemirror-model'
import { Plugin, PluginKey } from 'prosemirror-state'
import { Decoration, DecorationSet } from 'prosemirror-view'
import type { Awareness } from 'y-protocols/awareness'

const lockKey = new PluginKey('ariadne-document-locks')
// The collaboration room reserves this range for the server's writer.
const serverAwarenessStart = 2 ** 52

function readLocks(awareness: Awareness) {
  const locks = new Set<string>()
  for (const [client, state] of awareness.getStates()) {
    if (client < serverAwarenessStart || !Array.isArray(state.documentLocks)) continue
    for (const id of state.documentLocks) {
      if (typeof id === 'string') locks.add(id)
    }
  }
  return locks
}

function documentBlocks(doc: Node, locks: ReadonlySet<string>) {
  const blocks = new Map<string, { node: Node; pos: number }>()
  doc.descendants((node, pos) => {
    if (node.type.name === 'block') {
      const id = node.attrs.id
      if (typeof id === 'string' && locks.has(id)) blocks.set(id, { node, pos })
    }
  })
  return blocks
}

function parentBlock(doc: Node, pos: number): unknown {
  const point = doc.resolve(pos)
  for (let depth = point.depth; depth > 0; depth--) {
    const node = point.node(depth)
    if (node.type.name === 'block') return node.attrs.id
  }
}

/** Use the server's awareness to protect editor transactions that touch its blocks. */
export function documentLocks(awareness: Awareness, label: () => string) {
  return defineEditorExtension({
    name: 'ariadneDocumentLocks',
    plugins() {
      let locks = readLocks(awareness)
      return [
        new Plugin({
          key: lockKey,
          filterTransaction(transaction, state) {
            // DouUI tags updates it reads from the Yjs document as remote.
            if (!transaction.docChanged || transaction.getMeta('remote') || !locks.size) {
              return true
            }
            const after = documentBlocks(transaction.doc, locks)
            for (const [id, before] of documentBlocks(state.doc, locks)) {
              const next = after.get(id)
              const position = transaction.mapping.mapResult(before.pos, 1)
              if (
                !next ||
                position.deletedAcross ||
                position.pos !== next.pos ||
                parentBlock(state.doc, before.pos) !== parentBlock(transaction.doc, next.pos) ||
                !before.node.sameMarkup(next.node) ||
                !before.node.firstChild?.eq(next.node.firstChild!)
              ) {
                return false
              }
            }
            return true
          },
          props: {
            decorations(state) {
              if (!locks.size) return null
              const decorations = Array.from(
                documentBlocks(state.doc, locks).values(),
                ({ node, pos }) =>
                  Decoration.node(pos, pos + node.nodeSize, {
                    'data-ai-editing': '',
                    title: label(),
                  }),
              )
              return DecorationSet.create(state.doc, decorations)
            },
          },
          view(view) {
            const changed = () => {
              const next = readLocks(awareness)
              if (next.size === locks.size && [...next].every((id) => locks.has(id))) return
              locks = next
              view.dispatch(view.state.tr.setMeta(lockKey, true))
            }
            awareness.on('change', changed)
            return { destroy: () => awareness.off('change', changed) }
          },
        }),
      ]
    },
  })
}
