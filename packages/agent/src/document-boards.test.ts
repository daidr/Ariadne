import type { WhiteboardContent } from '@ariadne/core'
import { describe, expect, test } from 'vitest'
import * as Y from 'yjs'
import { boardSoFar, documentFromView, documentView } from './document-boards'
import { bridges } from './shared-content'

const board = (items: object[], connectors: object[] = []) =>
  `\`\`\`whiteboard\n${JSON.stringify({ items, connectors })}\n\`\`\``

const flow = board(
  [
    { id: 'a', kind: 'rect', text: '下单' },
    { id: 'b', kind: 'diamond', text: '有库存？' },
    { id: 'c', kind: 'rect', text: '发货' },
  ],
  [
    { from: 'a', to: 'b' },
    { from: 'b', to: 'c', label: '有' },
  ],
)

/** The stored boards of a document, in order. */
function boardsOf(markdown: string): { height: number; board: WhiteboardContent }[] {
  return [...markdown.matchAll(/^```dou-whiteboard\n(.*)\n```$/gm)].map(
    (m) => JSON.parse(m[1]!) as { height: number; board: WhiteboardContent },
  )
}

describe('whiteboards in a document', () => {
  test('a new board is laid out and stored the way dou-ui reads it', () => {
    const written = documentFromView(`# 流程\n\n${flow}\n\n结尾`, '')
    expect(written.problems).toEqual([])
    const [stored] = boardsOf(written.content)
    expect(stored!.board.nodes.map((n) => n.id)).toEqual(['a', 'b', 'c'])
    expect(stored!.board.nodes.every((n) => Number.isFinite(n.x) && Number.isFinite(n.y))).toBe(
      true,
    )
    expect(stored!.height).toBeGreaterThanOrEqual(160)
    expect(stored!.height).toBeLessThanOrEqual(480)
    expect(written.content.startsWith('# 流程\n\n```dou-whiteboard\n')).toBe(true)
    expect(written.content.endsWith('```\n\n结尾')).toBe(true)
  })

  test('the model reads a board as items and connectors, without pixels', () => {
    const stored = documentFromView(flow, '').content
    const view = documentView(stored)
    expect(view.content).not.toContain('dou-whiteboard')
    expect(view.content).not.toContain('"x"')
    expect(view.content).toContain('{"id":"b","kind":"diamond","text":"有库存？"}')
    // One item a line, so an edit can replace a line.
    expect(view.content.split('\n').filter((l) => l.startsWith('{"id":')).length).toBe(5)
  })

  test("a fence written back as read keeps the board as it is, with the user's changes", () => {
    const stored = documentFromView(`前言\n\n${flow}`, '').content
    // The user moved an item and drew a line.
    const [{ board: before }] = boardsOf(stored) as [{ board: WhiteboardContent; height: number }]
    before.nodes[0]!.x += 300
    before.nodes.push({ id: 'ink', type: 'ink', x: 0, y: 0, data: { points: [[0, 0]] } })
    const edited = stored.replace(
      /^```dou-whiteboard\n.*\n```$/m,
      `\`\`\`dou-whiteboard\n${JSON.stringify({ height: 500, board: before })}\n\`\`\``,
    )
    const view = documentView(edited).content
    const next = documentFromView(view.replace('前言', '新的前言'), edited)
    expect(next.content).toBe(edited.replace('前言', '新的前言'))
    expect(boardsOf(next.content)[0]!.height).toBe(500)
  })

  test('a changed fence merges by id, removes what it leaves out, keeps drawings', () => {
    const stored = documentFromView(flow, '').content
    const [{ board: before }] = boardsOf(stored) as [{ board: WhiteboardContent; height: number }]
    before.nodes.push({ id: 'ink', type: 'ink', x: 0, y: 0, data: { points: [[0, 0]] } })
    const withInk = `\`\`\`dou-whiteboard\n${JSON.stringify({ height: 300, board: before })}\n\`\`\``
    const view = documentView(withInk).content
    expect(view).toContain('{"id":"ink","kind":"drawing"}')
    const changed = view
      .replace('"text":"发货"', '"text":"安排发货"')
      .split('\n')
      .filter((line) => !line.includes('"from":"b","to":"c"'))
      .join('\n')
      .replace(
        '{"id":"c","kind":"rect","text":"安排发货"}',
        '{"id":"c","kind":"rect","text":"安排发货"},\n{"id":"d","kind":"rect","text":"补货"}',
      )
    const next = documentFromView(changed, withInk)
    const [{ board: after, height }] = boardsOf(next.content) as [
      { board: WhiteboardContent; height: number },
    ]
    expect(height).toBe(300)
    expect(after.nodes.map((n) => n.id).toSorted()).toEqual(['a', 'b', 'c', 'd', 'ink'])
    expect(after.nodes.find((n) => n.id === 'c')!.data!.text).toBe('安排发货')
    expect(after.edges.map((e) => `${e.source.node}>${e.target.node}`)).toEqual(['a>b'])
    // What the user drew is as it was.
    expect(after.nodes.find((n) => n.id === 'ink')).toEqual(before.nodes.at(-1))
  })

  test('boards are matched by their place among the boards', () => {
    const one = board([{ id: 'x', kind: 'rect', text: '一' }])
    const two = board([{ id: 'y', kind: 'rect', text: '二' }])
    const stored = documentFromView(`${one}\n\n${two}`, '').content
    const view = documentView(stored).content
    // A new board goes in front, the second is changed: the first stays as it was.
    const three = board([{ id: 'z', kind: 'sticky', text: '三' }])
    const fences = view.split('\n\n')
    const changed = [three, fences[0], fences[1]!.replace('二', '贰')].join('\n\n')
    const next = boardsOf(documentFromView(changed, stored).content)
    const was = boardsOf(stored)
    expect(next.map((b) => b.board.nodes[0]!.id)).toEqual(['z', 'x', 'y'])
    expect(next[1]).toEqual(was[0])
    expect(next[2]!.board.nodes[0]!.data!.text).toBe('贰')
    expect(next[2]!.board.nodes[0]!.x).toBe(was[1]!.board.nodes[0]!.x)
  })

  test('mistakes are refused with the board they are on', () => {
    expect(() => documentFromView('```whiteboard\n{"items": [\n```', '')).toThrow(/whiteboard 1/)
    expect(() => documentFromView(board([{ id: 'p', kind: 'pending' }]), '')).toThrow(
      /in a document/,
    )
    expect(() => documentFromView('```whiteboard\n{"items": []}', '')).toThrow(/not closed/)
  })

  test('while it is written, an unfinished board shows as a placeholder', () => {
    const half = '# 流程\n\n```whiteboard\n{"items": [\n{"id":"a","kind":"rect","text":"下'
    const shown = documentFromView(half, '', true)
    expect(shown.content).toBe('# 流程\n\n<pending>画板</pending>')
  })

  test('a board begun in the middle of a document does not take in what follows', () => {
    const half = '```whiteboard\n{"items": [\n{"id":"a","kind":"rect","text":"下'
    expect(boardSoFar(half)).toBe('<pending>画板</pending>')
    expect(boardSoFar(`前言\n\n${half}`)).toBe('前言\n\n<pending>画板</pending>')
    expect(boardSoFar(flow)).toBe(flow)
  })

  test('problems are listed by board', () => {
    const crowded = board([
      { id: 'a', kind: 'rect', text: '甲', x: 0, y: 0 },
      { id: 'b', kind: 'rect', text: '乙', x: 0, y: 0 },
    ])
    const stored = documentFromView(crowded, '').content
    const [{ board: b }] = boardsOf(stored) as [{ board: WhiteboardContent; height: number }]
    b.nodes[1]!.x = b.nodes[0]!.x
    b.nodes[1]!.y = b.nodes[0]!.y
    const overlapping = `\`\`\`dou-whiteboard\n${JSON.stringify({ board: b })}\n\`\`\``
    expect(documentView(overlapping).problems).toEqual(['whiteboard 1: items "a" and "b" overlap'])
  })

  test('a document edited together keeps its boards through the room', () => {
    const stored = documentFromView(`# 标题\n\n${flow}\n\n正文`, '').content
    const doc = new Y.Doc()
    bridges.document.seed(doc, stored)
    const read = bridges.document.read(doc)
    expect(boardsOf(read)).toEqual(boardsOf(stored))
    bridges.document.update(doc, read.replace('正文', '新正文'), read)
    expect(boardsOf(bridges.document.read(doc))).toEqual(boardsOf(stored))
  })
})
