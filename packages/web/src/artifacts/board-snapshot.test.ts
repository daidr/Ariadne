import type { FlowDocument, FlowNode } from '@dou-ui/flow'
import { describe, expect, test } from 'vitest'
import { nodesEqual, snapshotBoardNodes, touchedOptionDocuments } from './board-snapshot'

function optionNode(id: string, x: number, y: number, extra: Partial<FlowNode> = {}): FlowNode {
  return {
    id,
    type: 'option',
    x,
    y,
    width: 300,
    height: 200,
    data: { title: id },
    ...extra,
  }
}

function stickyNode(id: string, x: number, y: number, extra: Partial<FlowNode> = {}): FlowNode {
  return {
    id,
    type: 'sticky',
    x,
    y,
    width: 80,
    height: 40,
    data: { text: 'note' },
    ...extra,
  }
}

describe('nodesEqual', () => {
  test('matches deeply equal plain structures regardless of key order', () => {
    expect(
      nodesEqual({ a: 1, b: { x: [1, 2], y: true } }, { b: { y: true, x: [1, 2] }, a: 1 }),
    ).toBe(true)
    expect(nodesEqual({ a: 1, b: undefined }, { a: 1 })).toBe(true)
    expect(nodesEqual({ a: 1, b: 2 }, { a: 1, b: 3 })).toBe(false)
    expect(nodesEqual([1, 2], [1, 2, 3])).toBe(false)
  })
})

describe('snapshotBoardNodes & touchedOptionDocuments', () => {
  test('isolates snapshot from later mutations and returns only touched options', () => {
    const doc: FlowDocument = {
      version: 1,
      nodes: [
        optionNode('opt_a', 0, 0),
        optionNode('opt_b', 400, 0),
        stickyNode('note_a', 40, 40, { parent: 'opt_a' }),
        stickyNode('note_b', 440, 40),
      ],
      edges: [
        { id: 'e1', source: { node: 'opt_a' }, target: { node: 'note_a' } },
        { id: 'e2', source: { node: 'opt_a' }, target: { node: 'opt_b' } },
      ],
    }

    const before = snapshotBoardNodes(doc)
    expect(touchedOptionDocuments(doc, before)).toEqual([])

    // Mutate note_a in place: snapshot must remain unaffected and only opt_a is returned.
    doc.nodes[2]!.data = { text: 'updated note' }
    const touched = touchedOptionDocuments(doc, before)
    expect(touched).toHaveLength(1)
    expect(touched[0]!.nodes.map((n) => n.id)).toEqual(['opt_a', 'note_a'])
    expect(touched[0]!.edges.map((e) => e.id)).toEqual(['e1'])

    // Add a free-floating mark inside opt_b's bounds (without parent field).
    doc.nodes.push(stickyNode('mark_b', 460, 60))
    const touchedBoth = touchedOptionDocuments(doc, before)
    expect(touchedBoth).toHaveLength(2)
    expect(touchedBoth[1]!.nodes.map((n) => n.id)).toEqual(['opt_b', 'note_b', 'mark_b'])
    expect(touchedBoth[1]!.edges).toEqual([])
  })

  test('returns empty when board has no option nodes', () => {
    const doc: FlowDocument = {
      version: 1,
      nodes: [stickyNode('solo', 10, 10)],
      edges: [],
    }
    expect(touchedOptionDocuments(doc, null)).toEqual([])
  })
})
