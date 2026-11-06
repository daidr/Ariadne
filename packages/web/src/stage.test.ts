import type { ThreadStage, ThreadView } from '@ariadne/core'
import { describe, expect, test } from 'vitest'
import { groupOf, sortThreads, threadLine } from './stage'

const thread = (over: Partial<ThreadView>): ThreadView =>
  ({
    id: 'thr_1',
    title: 't',
    stage: 'idle',
    lastHeadline: null,
    createdAt: 0,
    activeAt: 0,
    ...over,
  }) as ThreadView

const ids = (sorted: ThreadView[]) => sorted.map((item) => item.id)

describe('thread list', () => {
  test('shows the headline only while it waits on the user or once done', () => {
    const stages: ThreadStage[] = ['review', 'blocked', 'done', 'working', 'idle', 'answer']
    expect(stages.map((stage) => threadLine(thread({ stage, lastHeadline: '看一下' })))).toEqual([
      '看一下',
      '看一下',
      '看一下',
      null,
      null,
      null,
    ])
  })

  test('groups every stage', () => {
    expect(groupOf.answer).toBe('waiting')
    expect(groupOf.blocked).toBe('waiting')
    expect(groupOf.queued).toBe('working')
    expect(groupOf.stopped).toBe('paused')
    expect(groupOf.halted).toBe('waiting')
    expect(groupOf.silent).toBe('waiting')
  })

  test('sorts by activity, title or creation without touching the list', () => {
    const list = [
      thread({ id: 'a', title: '评审 #10', createdAt: 3, activeAt: 1 }),
      thread({ id: 'b', title: '评审 #9', createdAt: 1, activeAt: 3 }),
      thread({ id: 'c', title: '修复', createdAt: 2, activeAt: 2 }),
    ]
    expect(ids(sortThreads(list, 'active', 'zh-CN'))).toEqual(['b', 'c', 'a'])
    expect(ids(sortThreads(list, 'created', 'zh-CN'))).toEqual(['a', 'c', 'b'])
    expect(ids(sortThreads(list, 'title', 'zh-CN'))).toEqual(['b', 'a', 'c'])
    expect(ids(list)).toEqual(['a', 'b', 'c'])
  })
})
