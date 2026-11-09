import { describe, expect, test } from 'vitest'
import type { DockNode } from 'dou-ui'
import {
  type SideLayout,
  close,
  closeOthers,
  viewOf,
  defaultLayout,
  focusedGroup,
  focusedThread,
  groupsOf,
  move,
  open,
  openThreads,
  parseLayout,
  prune,
  split,
  threadKey,
} from './layout'

const tabs = (layout: SideLayout) => groupsOf(layout.root).map((g) => g.tabs)
const shape = (node: DockNode): unknown =>
  node.type === 'group' ? node.tabs : [node.direction, shape(node.start), shape(node.end)]

describe('side layout', () => {
  test('starts with the lists in one group, the plan in front', () => {
    const layout = defaultLayout()
    expect(tabs(layout)).toEqual([['plan', 'threads', 'artifacts', 'memory']])
    expect(focusedGroup(layout).active).toBe('plan')
  })

  test('opens a thread after the tab in front, and brings an open one forward', () => {
    let layout = open(defaultLayout(), threadKey('a'))
    expect(tabs(layout)).toEqual([['plan', 'thread:a', 'threads', 'artifacts', 'memory']])
    expect(focusedThread(layout)).toBe('a')
    layout = open(layout, 'memory')
    layout = open(layout, threadKey('a'))
    expect(tabs(layout)[0]).toHaveLength(5)
    expect(focusedThread(layout)).toBe('a')
  })

  test('replace takes the place of the thread in front, not of a list', () => {
    let layout = open(defaultLayout(), threadKey('a'))
    layout = open(layout, threadKey('b'), { replace: true })
    expect(tabs(layout)).toEqual([['plan', 'thread:b', 'threads', 'artifacts', 'memory']])
    layout = open(layout, 'threads')
    layout = open(layout, threadKey('c'), { replace: true })
    expect(tabs(layout)[0]).toContain('thread:b')
    expect(tabs(layout)[0]).toContain('thread:c')
  })

  test('opens beside in a new group, moving a tab that was open', () => {
    let layout = open(defaultLayout(), threadKey('a'), { beside: 'horizontal' })
    expect(shape(layout.root)).toEqual([
      'horizontal',
      ['plan', 'threads', 'artifacts', 'memory'],
      ['thread:a'],
    ])
    expect(focusedThread(layout)).toBe('a')
    // Alone in its group already: nothing moves.
    expect(open(layout, threadKey('a'), { beside: 'vertical' }).root).toEqual(layout.root)
    layout = open(layout, 'plan', { beside: 'vertical' })
    expect(shape(layout.root)).toEqual([
      'horizontal',
      ['threads', 'artifacts', 'memory'],
      ['vertical', ['thread:a'], ['plan']],
    ])
  })

  test('splits a tab out, and an emptied group gives its room back', () => {
    let layout = split(defaultLayout(), 'threads', 'vertical')
    expect(shape(layout.root)).toEqual(['vertical', ['plan', 'artifacts', 'memory'], ['threads']])
    expect(focusedGroup(layout).tabs).toEqual(['threads'])
    // The last tab of a group cannot split off: nothing would stay behind.
    expect(split(layout, 'threads', 'horizontal')).toBe(layout)
    layout = close(layout, 'threads')
    expect(shape(layout.root)).toEqual(['plan', 'artifacts', 'memory'])
    expect(focusedGroup(layout).tabs).toEqual(['plan', 'artifacts', 'memory'])
  })

  test('closing the tab in front brings its neighbour forward', () => {
    let layout = open(defaultLayout(), 'artifacts')
    layout = close(layout, 'artifacts')
    expect(focusedGroup(layout).active).toBe('memory')
    layout = close(layout, 'memory')
    expect(focusedGroup(layout).active).toBe('threads')
  })

  test('closing the other tabs closes threads only, and the side never ends up empty', () => {
    let layout = open(defaultLayout(), threadKey('a'))
    layout = open(layout, threadKey('b'))
    layout = closeOthers(layout, threadKey('a'))
    expect(tabs(layout)).toEqual([['plan', 'thread:a', 'threads', 'artifacts', 'memory']])
    for (const key of ['plan', 'threads', 'artifacts', 'memory', 'thread:a'] as const) {
      layout = close(layout, key)
    }
    expect(tabs(layout)).toEqual([['plan', 'threads', 'artifacts', 'memory']])
  })

  test('moves a tab to another group', () => {
    let layout = split(defaultLayout(), 'threads', 'horizontal')
    const [left, right] = groupsOf(layout.root)
    layout = move(layout, 'plan', right!.id)
    expect(tabs(layout)).toEqual([
      ['artifacts', 'memory'],
      ['threads', 'plan'],
    ])
    expect(focusedGroup(layout).active).toBe('plan')
    layout = move(layout, 'artifacts', right!.id)
    layout = move(layout, 'memory', right!.id)
    // The left group emptied and went.
    expect(tabs(layout)).toEqual([['threads', 'plan', 'artifacts', 'memory']])
    expect(groupsOf(layout.root).some((g) => g.id === left!.id)).toBe(false)
  })

  test('lists the threads open anywhere', () => {
    let layout = open(defaultLayout(), threadKey('a'), { beside: 'horizontal' })
    layout = open(layout, threadKey('b'))
    expect(openThreads(layout)).toEqual(['a', 'b'])
  })

  test('prunes tabs of threads that are gone', () => {
    let layout = open(defaultLayout(), threadKey('a'), { beside: 'horizontal' })
    layout = prune(layout, (key) => key !== 'thread:a')
    expect(shape(layout.root)).toEqual(['plan', 'threads', 'artifacts', 'memory'])
  })

  test('reads back what it stored, and turns down what it cannot use', () => {
    const opened = open(defaultLayout(), threadKey('a'), { beside: 'vertical' })
    const layout = { ...opened, root: { ...opened.root, size: 40 } as DockNode }
    expect(parseLayout(JSON.parse(JSON.stringify(layout)))).toEqual(layout)
    expect(parseLayout(null)).toBeNull()
    expect(parseLayout({ root: { type: 'group', id: 'g', tabs: [] } })).toBeNull()
    // Unknown tabs and a tab open twice are dropped; a split missing a side is that side.
    const parsed = parseLayout({
      root: {
        type: 'split',
        direction: 'sideways',
        size: 500,
        start: { type: 'group', id: 'g1', tabs: ['plan', 'nonsense', 'plan'], active: 'x' },
        end: { type: 'group', id: 'g2', tabs: ['plan'] },
      },
      focused: 'missing',
    })
    expect(parsed).toEqual({
      root: { type: 'group', id: 'g1', tabs: ['plan'], active: 'plan' },
      focused: 'g1',
    })
  })

  test('keeps artifacts and branch changes, not what a turn changed', () => {
    const parsed = parseLayout({
      root: {
        type: 'group',
        id: 'g',
        tabs: ['plan', 'artifact:art_1', 'changes:th_1', 'turn:th_1:tu_1'],
        active: 'turn:th_1:tu_1',
      },
    })
    expect(parsed?.root).toEqual({
      type: 'group',
      id: 'g',
      tabs: ['plan', 'artifact:art_1', 'changes:th_1'],
      active: 'plan',
    })
    expect(viewOf('turn:th_1:tu_1')).toEqual({ kind: 'turn', threadId: 'th_1', turnId: 'tu_1' })
    expect(viewOf('artifact:art_1')).toEqual({ kind: 'artifact', id: 'art_1' })
    expect(viewOf('thread:th_1')).toBeNull()
  })

  test('closes the other artifacts and changes with the threads, not the lists', () => {
    let layout = open(defaultLayout(), 'artifact:art_1')
    layout = open(layout, threadKey('a'))
    layout = closeOthers(layout, 'plan')
    expect(shape(layout.root)).toEqual(['plan', 'threads', 'artifacts', 'memory'])
  })
})
