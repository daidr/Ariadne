import { describe, expect, test } from 'vitest'
import { boardFromItems, boardProblems, mergeBoard } from './whiteboard'

const WORDS = [
  '服务',
  'api',
  '数据库',
  'queue',
  '缓存层',
  'auth',
  '前端 SPA',
  '调度器',
  'logger',
  '外部服务',
]

/** A random board: a chain of up to 14 items, each joined from one or two earlier ones, most connectors labelled. */
function randomBoard(next: () => number) {
  const n = 3 + Math.floor(next() * 12)
  const items = Array.from({ length: n }, (_, i) => ({
    id: `n${i}`,
    text: `${WORDS[i % WORDS.length]}\n${'说明文字'.repeat(1 + Math.floor(next() * 4))}`,
  }))
  const connectors: { from: string; to: string; label?: string }[] = []
  for (let i = 1; i < n; i++) {
    for (let k = 1 + Math.floor(next() * 2); k > 0; k--) {
      const from = `n${Math.floor(next() * i)}`
      if (connectors.some((c) => c.from === from && c.to === `n${i}`)) continue
      const label =
        next() < 0.7
          ? WORDS[Math.floor(next() * WORDS.length)]!.repeat(1 + Math.floor(next() * 2))
          : undefined
      connectors.push({ from, to: `n${i}`, ...(label ? { label } : {}) })
    }
  }
  return { items, connectors }
}

describe('whiteboard layout', () => {
  test('random boards: no overlapping items, cut-off text or connectors through items; labels rarely meet', () => {
    let seed = 1
    const next = () => (seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648
    for (const direction of ['down', 'right'] as const) {
      let withLabelProblems = 0
      for (let t = 0; t < 200; t++) {
        const { items, connectors } = randomBoard(next)
        const problems = boardProblems(boardFromItems(items, connectors, direction))
        expect(
          problems.filter((p) => !p.includes('label')),
          `${direction} board ${t}`,
        ).toEqual([])
        if (problems.length) withLabelProblems++
      }
      // Labels in the same gap between ranks can still meet on dense boards; the model is told.
      expect(withLabelProblems).toBeLessThan(200 * 0.08)
    }
  })

  test('a sketch keeps its picture and is stretched until nothing overlaps', () => {
    // Four options in a tight ring, with a labelled connector between two neighbours.
    const board = boardFromItems(
      [
        { id: 'n', text: '北', x: 100, y: 0 },
        { id: 'e', text: '东', x: 200, y: 60 },
        { id: 's', text: '南', x: 100, y: 120 },
        { id: 'w', text: '西', x: 0, y: 60 },
      ],
      [{ from: 'w', to: 'e', label: '一段相当长的连线标签' }],
    )
    const at = Object.fromEntries(board.nodes.map((n) => [n.id, n]))
    expect(at.n!.y).toBeLessThan(at.e!.y)
    expect(at.e!.x).toBeGreaterThan(at.w!.x)
    expect(at.s!.y).toBeGreaterThan(at.e!.y)
    expect(boardProblems(board)).toEqual([])
  })

  test('the check finds what a user made overlap, and an item that grows pushes down what is below', () => {
    const board = boardFromItems(
      [
        { id: 'a', text: 'A' },
        { id: 'b', text: 'B' },
        { id: 'c', text: 'C' },
      ],
      [
        { from: 'a', to: 'b' },
        { from: 'b', to: 'c', label: '然后' },
      ],
    )
    expect(boardProblems(board)).toEqual([])
    const moved = structuredClone(board)
    const c = moved.nodes.find((n) => n.id === 'c')!
    c.y = moved.nodes.find((n) => n.id === 'a')!.y + 10
    c.x = moved.nodes.find((n) => n.id === 'a')!.x
    expect(boardProblems(moved)).toContain('items "a" and "c" overlap')
    expect(boardProblems(moved)).toContain('connector "b-c" runs through item "a"')

    const longer = mergeBoard(board, {
      items: [{ id: 'b', text: '一段很长的说明文字，'.repeat(8) }],
      connectors: [],
      removeItems: [],
      removeConnectors: [],
    })
    expect(longer.nodes.find((n) => n.id === 'b')!.height).toBeGreaterThan(90)
    expect(boardProblems(longer).filter((p) => p.includes('overlap'))).toEqual([])
  })
})
