/** Native/JS compatibility oracle for real editor, whiteboard and dashboard states. */
import * as Y from 'yjs'
import * as encoding from 'lib0/encoding'
import * as decoding from 'lib0/decoding'
import * as sync from 'y-protocols/sync'
import { bridges, type SharedKind } from '../shared-content'

const input = (await Bun.stdin.json()) as {
  kind: SharedKind
  state?: string
  reply?: string
  content?: string
}
const bridge = bridges[input.kind]
if (!bridge) throw new Error('unknown kind')
const doc = new Y.Doc()
try {
  const board = {
    version: 1,
    nodes: [
      {
        id: 'n1',
        type: 'shape',
        x: 0,
        y: 0,
        width: 160,
        height: 80,
        data: { shape: 'rect', text: '图形 🧵' },
        comments: ['comment-1'],
        commentPins: { 'comment-1': { x: 0.5, y: 0.5, number: 1 } },
      },
    ],
    edges: [],
  }
  const content = {
    document:
      '# 标题\n\n普通文字，**加粗**，emoji 🧵。\n\n| A | B |\n| --- | --- |\n| 甲 | 乙 |\n\n```dou-whiteboard\n' +
      JSON.stringify({ height: 240, board }) +
      '\n```',
    whiteboard: JSON.stringify(board),
    dashboard: JSON.stringify({
      version: 1,
      data: { totals: { n: 3 } },
      dashboard: {
        version: 1,
        widgets: [{ id: 'total', type: 'stat', props: { source: 'totals', field: 'n' } }],
      },
    }),
  }
  if (input.state) Y.applyUpdate(doc, Buffer.from(input.state, 'base64'))
  else bridge.seed(doc, input.content ?? content[input.kind])
  if (input.reply) {
    const decoder = decoding.createDecoder(Buffer.from(input.reply, 'base64'))
    if (decoding.readVarUint(decoder) !== 0) throw new Error('expected sync reply')
    sync.readSyncMessage(decoder, encoding.createEncoder(), doc, 'go')
  }
  const rendered = bridge.read(doc)
  const state = Buffer.from(Y.encodeStateAsUpdate(doc)).toString('base64')
  const encoder = encoding.createEncoder()
  encoding.writeVarUint(encoder, 0)
  sync.writeSyncStep1(encoder, doc)
  console.info(
    JSON.stringify({
      state,
      snapshot: doc.toJSON(),
      content: rendered,
      sync: Buffer.from(encoding.toUint8Array(encoder)).toString('base64'),
    }),
  )
} finally {
  doc.destroy()
}
