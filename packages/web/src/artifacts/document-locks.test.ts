import { Schema } from 'prosemirror-model'
import { EditorState } from 'prosemirror-state'
import { describe, expect, it } from 'vitest'
import { Awareness } from 'y-protocols/awareness'
import * as Y from 'yjs'
import { documentLocks } from './document-locks'

const schema = new Schema({
  nodes: {
    doc: { content: 'block+' },
    block: { content: 'paragraph', attrs: { id: { default: null } } },
    paragraph: { content: 'text*' },
    text: {},
  },
})

describe('document block locks', () => {
  it('filters changes to the locked block and lets independent and remote changes through', () => {
    const ydoc = new Y.Doc()
    ydoc.clientID = 2 ** 52
    const awareness = new Awareness(ydoc)
    try {
      awareness.setLocalStateField('documentLocks', ['locked'])
      const block = (id: string, value: string) =>
        schema.node('block', { id }, schema.node('paragraph', null, schema.text(value)))
      const locked = block('locked', 'Original')
      const doc = schema.node('doc', null, [locked, block('other', 'Free to edit')])
      const extension = documentLocks(awareness, () => 'AI is editing this block')
      const plugins = extension.plugins?.({
        schema,
        messages: () => {
          throw new Error('unexpected messages lookup')
        },
      })
      const state = EditorState.create({ doc, plugins })
      const accepted = (tr: typeof state.tr) => state.applyTransaction(tr).transactions.length > 0

      expect(accepted(state.tr.insertText('x', 2))).toBe(false)
      expect(accepted(state.tr.delete(0, locked.nodeSize))).toBe(false)
      const move = state.tr.delete(0, locked.nodeSize)
      expect(accepted(move.insert(move.doc.content.size, locked))).toBe(false)
      expect(accepted(state.tr.insertText('x', locked.nodeSize + 2))).toBe(true)
      expect(accepted(state.tr.insert(0, block('new', 'Above the locked block')))).toBe(true)
      expect(accepted(state.tr.insertText('AI', 2).setMeta('remote', true))).toBe(true)
    } finally {
      ydoc.destroy()
    }
  })
})
