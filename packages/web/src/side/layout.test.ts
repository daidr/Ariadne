import { describe, expect, test } from 'vitest'
import { moveDockTab, splitDockTab, type DockNode } from 'dou-ui'
import {
  type SideLayout,
  activate,
  close,
  closeOthers,
  compactGroup,
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
  updateDock,
} from './layout'

const tabs = (layout: SideLayout) => groupsOf(layout.root).map((g) => g.tabs)
const shape = (node: DockNode): unknown =>
  node.type === 'group' ? node.tabs : [node.direction, shape(node.start), shape(node.end)]

describe('side layout', () => {
  test('starts with the lists in one group, the plan in front', () => {
    const layout = defaultLayout()
    expect(tabs(layout)).toEqual([['plan', 'threads', 'artifacts']])
    expect(focusedGroup(layout).active).toBe('plan')
  })

  test('opens a thread after the tab in front, and brings an open one forward', () => {
    let layout = open(defaultLayout(), threadKey('a'))
    expect(tabs(layout)).toEqual([['plan', 'threads', 'artifacts', 'thread:a']])
    expect(focusedThread(layout)).toBe('a')
    layout = open(layout, 'artifacts')
    layout = open(layout, threadKey('a'))
    expect(tabs(layout)[0]).toHaveLength(4)
    expect(focusedThread(layout)).toBe('a')
  })

  test('puts all split tabs in one narrow row and keeps each desktop group intact', () => {
    let layout = open(defaultLayout(), threadKey('a'))
    layout = open(layout, threadKey('b'), { beside: 'horizontal' })
    expect(compactGroup(layout)).toMatchObject({
      tabs: ['plan', 'threads', 'artifacts', 'thread:a', 'thread:b'],
      active: 'thread:b',
    })
    layout = move(layout, 'plan', focusedGroup(layout).id)
    expect(compactGroup(layout).active).toBe('plan')
    expect(tabs(layout)).toEqual([
      ['plan', 'threads', 'artifacts', 'thread:a'],
      ['thread:b', 'list:plan:1'],
    ])
  })

  test('replace takes the place of the thread in front, not of a list', () => {
    let layout = open(defaultLayout(), threadKey('a'))
    layout = open(layout, threadKey('b'), { replace: true })
    expect(tabs(layout)).toEqual([['plan', 'threads', 'artifacts', 'thread:b']])
    layout = open(layout, 'threads')
    layout = open(layout, threadKey('c'), { replace: true })
    expect(tabs(layout)[0]).toContain('thread:b')
    expect(tabs(layout)[0]).toContain('thread:c')
  })

  test('opens beside in a new group, moving a tab that was open', () => {
    let layout = open(defaultLayout(), threadKey('a'), { beside: 'horizontal' })
    expect(shape(layout.root)).toEqual([
      'horizontal',
      ['plan', 'threads', 'artifacts'],
      ['thread:a'],
    ])
    expect(focusedThread(layout)).toBe('a')
    // Alone in its group already: nothing moves.
    expect(open(layout, threadKey('a'), { beside: 'vertical' }).root).toEqual(layout.root)
    layout = open(layout, 'plan', { beside: 'vertical' })
    expect(shape(layout.root)).toEqual([
      'horizontal',
      ['plan', 'threads', 'artifacts'],
      ['vertical', ['thread:a'], ['list:plan:1']],
    ])
  })

  test('splits a tab out, and an emptied group gives its room back', () => {
    let layout = split(defaultLayout(), 'threads', 'vertical')
    expect(shape(layout.root)).toEqual([
      'vertical',
      ['plan', 'threads', 'artifacts'],
      ['list:threads:1'],
    ])
    expect(focusedGroup(layout).tabs).toEqual(['list:threads:1'])
    // The last tab of a group cannot split off: nothing would stay behind.
    expect(split(layout, 'list:threads:1', 'horizontal')).toBe(layout)
    layout = close(layout, 'list:threads:1')
    expect(shape(layout.root)).toEqual(['plan', 'threads', 'artifacts'])
  })

  test('closing the first opened tab goes back to the previously active list', () => {
    let layout = open(defaultLayout(), threadKey('a'))
    layout = close(layout, threadKey('a'))
    expect(focusedGroup(layout).active).toBe('plan')
    expect(close(layout, 'artifacts')).toBe(layout)
  })

  test('closing the active tab follows selection history, even after reloading the layout', () => {
    let layout = open(defaultLayout(), threadKey('a'))
    layout = open(layout, threadKey('b'))
    layout = open(layout, threadKey('c'))
    layout = activate(layout, threadKey('a'))
    layout = activate(layout, threadKey('c'))
    const restored = parseLayout(JSON.parse(JSON.stringify(layout)))!
    expect(focusedThread(close(restored, threadKey('b')))).toBe('c')
    expect(focusedThread(close(restored, threadKey('c')))).toBe('a')
  })

  test('the compact row goes back across desktop groups, and closing an empty group also returns focus', () => {
    let layout = open(defaultLayout(), threadKey('a'))
    layout = open(layout, threadKey('b'), { beside: 'horizontal' })
    layout = open(layout, threadKey('c'))
    layout = activate(layout, threadKey('a'))
    layout = activate(layout, threadKey('c'))
    expect(focusedThread(close(layout, threadKey('c')))).toBe('b')
    expect(focusedThread(close(layout, threadKey('c'), true))).toBe('a')
    expect(focusedThread(close(close(layout, threadKey('c')), threadKey('b')))).toBe('a')
  })

  test('closing the other tabs closes threads only, and the side never ends up empty', () => {
    let layout = open(defaultLayout(), threadKey('a'))
    layout = open(layout, threadKey('b'))
    layout = closeOthers(layout, threadKey('a'))
    expect(tabs(layout)).toEqual([['plan', 'threads', 'artifacts', 'thread:a']])
    for (const key of ['plan', 'threads', 'artifacts', 'thread:a'] as const) {
      layout = close(layout, key)
    }
    expect(tabs(layout)).toEqual([['plan', 'threads', 'artifacts']])
  })

  test('moves a tab to another group', () => {
    let layout = split(defaultLayout(), 'threads', 'horizontal')
    const [left, right] = groupsOf(layout.root)
    layout = move(layout, 'plan', right!.id)
    expect(tabs(layout)).toEqual([
      ['plan', 'threads', 'artifacts'],
      ['list:threads:1', 'list:plan:1'],
    ])
    expect(focusedGroup(layout).active).toBe('list:plan:1')
    layout = move(layout, 'artifacts', right!.id)
    layout = move(layout, 'list:plan:1', left!.id)
    expect(tabs(layout)).toEqual([
      ['plan', 'threads', 'artifacts'],
      ['list:threads:1', 'list:artifacts:1'],
    ])
    expect(groupsOf(layout.root)[0]?.active).toBe('plan')
  })

  test('dragging a fixed tab copies it, and a copy returned to the first panel leaves its selection alone', () => {
    let layout = open(defaultLayout(), threadKey('a'))
    const first = focusedGroup(layout).id
    layout = updateDock(layout, splitDockTab(layout.root, 'plan', first, 'bottom'))
    const second = groupsOf(layout.root)[1]!.id
    expect(tabs(layout)).toEqual([['plan', 'threads', 'artifacts', 'thread:a'], ['list:plan:1']])
    layout = updateDock(layout, splitDockTab(layout.root, 'plan', first, 'top'))
    expect(groupsOf(layout.root)[0]?.id).toBe(first)
    expect(tabs(layout)).toEqual([
      ['plan', 'threads', 'artifacts', 'thread:a'],
      ['list:plan:2'],
      ['list:plan:1'],
    ])
    layout = updateDock(layout, moveDockTab(layout.root, 'list:plan:1', first))
    expect(groupsOf(layout.root).find((group) => group.id === second)).toBeUndefined()
    expect(tabs(layout)).toEqual([['plan', 'threads', 'artifacts', 'thread:a'], ['list:plan:2']])
    expect(groupsOf(layout.root)[0]?.active).toBe('thread:a')
    expect(parseLayout(JSON.parse(JSON.stringify(layout)))).toEqual(layout)
  })

  test('older layouts keep their list in its panel and restore the fixed entry in the first panel', () => {
    const layout = parseLayout({
      root: {
        type: 'split',
        start: { type: 'group', id: 'first', tabs: ['threads', 'thread:a'] },
        end: { type: 'group', id: 'second', tabs: ['plan', 'artifacts'], active: 'artifacts' },
      },
    })!
    expect(tabs(layout)).toEqual([
      ['plan', 'threads', 'artifacts', 'thread:a'],
      ['list:plan:1', 'list:artifacts:1'],
    ])
    expect(groupsOf(layout.root)[1]?.active).toBe('list:artifacts:1')
  })

  test('lists the threads open anywhere', () => {
    let layout = open(defaultLayout(), threadKey('a'), { beside: 'horizontal' })
    layout = open(layout, threadKey('b'))
    expect(openThreads(layout)).toEqual(['a', 'b'])
  })

  test('prunes tabs of threads that are gone', () => {
    let layout = open(defaultLayout(), threadKey('a'), { beside: 'horizontal' })
    layout = prune(layout, (key) => key !== 'thread:a')
    expect(shape(layout.root)).toEqual(['plan', 'threads', 'artifacts'])
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
      root: { type: 'group', id: 'g1', tabs: ['plan', 'threads', 'artifacts'], active: 'plan' },
      focused: 'g1',
      recent: ['plan'],
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
      tabs: ['plan', 'threads', 'artifacts', 'artifact:art_1', 'changes:th_1'],
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
    expect(shape(layout.root)).toEqual(['plan', 'threads', 'artifacts'])
  })
})
