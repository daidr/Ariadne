import { Ariadne, type WhiteboardContent } from '@ariadne/core'
import { readMap, updateMap } from '@dou-ui/editor/collaboration'
import { afterEach, expect, test } from 'vitest'
import * as Y from 'yjs'
import { bridges, documentBoard, documentExtensions } from './shared-content'
import { SharedArtifacts } from './shared-artifacts'
import { documentAnchorWhere } from './artifact-feedback'

const docs: Y.Doc[] = []
afterEach(() => docs.splice(0).forEach((doc) => doc.destroy()))
const seeded = (content: string) => {
  const doc = new Y.Doc()
  docs.push(doc)
  bridges.document.seed(doc, content)
  return doc
}
const extensions = documentExtensions
const board = (label: string): WhiteboardContent => ({
  version: 1,
  nodes: [
    {
      id: 'same',
      type: 'shape',
      x: 0,
      y: 0,
      width: 160,
      height: 80,
      data: { shape: 'rect', text: label },
    },
  ],
  edges: [],
})
const fence = (value: WhiteboardContent) =>
  `\`\`\`dou-whiteboard\n${JSON.stringify({ height: 240, board: value })}\n\`\`\``

test('an agent editing a paragraph from its base preserves text inserted earlier by the user', () => {
  const base = '# 方案\n\n先检查接口，再核对页面。'
  const doc = seeded(base)
  const paragraph = [...doc.getMap('document').values()].find(
    (value) => value instanceof Y.Map && value.get('type') === 'paragraph',
  ) as Y.Map<unknown>
  ;(paragraph.get('text') as Y.Text).insert(0, '用户补充：')
  bridges.document.update(doc, base.replace('页面', '交互'), base)
  expect(bridges.document.read(doc)).toContain('用户补充：先检查接口，再核对交互。')
})

test('malformed embedded connectors leave valid nodes and connections available', () => {
  const value = board('保留图形')
  value.edges = [
    { id: 'ok', source: { node: 'same' }, target: { node: 'same' } },
    { id: 'missing-node', source: { node: 'absent' }, target: { node: 'same' } },
    { id: 'missing-endpoint' },
  ] as WhiteboardContent['edges']
  const doc = seeded(fence(value))
  const id = readMap(doc.getMap('document'), { extensions }).blocks[0]!.id
  const result = documentBoard(doc, id)!
  expect(result.board.nodes).toHaveLength(1)
  expect(result.board.edges.map((edge) => edge.id)).toEqual(['ok'])
})

test('an agent changing an embedded node preserves another field changed by a user in that same block', () => {
  const base = fence(board('原始说明'))
  const doc = seeded(base)
  const map = doc.getMap('document')
  const current = readMap(map, { extensions })
  const next = structuredClone(current)
  const value = next.blocks[0]!.props!.board as unknown as WhiteboardContent
  value.nodes[0]!.x = 320
  value.nodes[0]!.comments = ['user-comment']
  updateMap(map, next, { extensions, base: current })
  bridges.document.update(doc, base.replace('原始说明', '代理改写'), base)
  const after = readMap(map, { extensions }).blocks[0]!.props!.board as unknown as WhiteboardContent
  expect(after.nodes[0]).toMatchObject({
    x: 320,
    comments: ['user-comment'],
    data: { text: '代理改写' },
  })
})

test('comments distinguish repeated node ids across document boards and survive saved shared state', () => {
  const app = new Ariadne({ path: ':memory:' })
  const shared = new SharedArtifacts(app)
  try {
    const project = app.createProject('P')
    const artifact = app.createArtifact(project.id, {
      kind: 'document',
      title: 'Plan',
      author: 'user',
      content: `${fence(board('第一张'))}\n\n${fence(board('第二张'))}`,
    })
    const link = shared.join(artifact.id, { send() {}, close() {} })
    const doc = new Y.Doc()
    docs.push(doc)
    Y.applyUpdate(doc, app.readSharedState(artifact.id)!.state)
    const blocks = readMap(doc.getMap('document'), { extensions }).blocks
    const anchor = { block: blocks[1]!.id, node: 'same' }
    app.commentOnArtifact(artifact.id, 'user', {
      type: 'create',
      threadId: 'cth-1',
      commentId: 'cmt-1',
      quote: '',
      anchor,
      body: '修改第二张',
    })
    expect(documentAnchorWhere(shared, artifact.id, anchor, 'cth-1')).toContain('in whiteboard 2')
    expect(documentAnchorWhere(shared, artifact.id, anchor, 'cth-1')).toContain('第二张')
    link.close()
    const restored = new SharedArtifacts(app)
    try {
      expect(restored.commentBoard(artifact.id, blocks[1]!.id)?.index).toBe(2)
      expect((restored as unknown as { rooms: Map<string, unknown> }).rooms.size).toBe(0)
    } finally {
      restored.dispose()
    }
  } finally {
    shared.dispose()
    app.close()
  }
})
