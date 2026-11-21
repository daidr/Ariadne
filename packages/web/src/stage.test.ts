import type { AttentionView, NeedsMeItem, ThreadStage, ThreadView } from '@ariadne/core'
import { describe, expect, test } from 'vitest'
import {
  groupOf,
  groupOfThread,
  sortThreads,
  threadActionIds,
  threadGroups,
  threadLine,
} from './stage'

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
  test('shows the headline for a review, blocker or completed delivery', () => {
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
    expect(groupOf.blocked).toBe('idle')
    expect(groupOf.queued).toBe('working')
    expect(groupOf.stopped).toBe('paused')
    expect(groupOf.halted).toBe('waiting')
    expect(groupOf.silent).toBe('waiting')
  })

  test('archived threads are listed last, whatever they ended as', () => {
    expect(threadGroups.at(-1)).toBe('archived')
    const actions = new Set<string>()
    expect(groupOfThread(thread({ stage: 'done', archived: true }), actions)).toBe('archived')
    expect(groupOfThread(thread({ stage: 'done', archived: false }), actions)).toBe('done')
  })

  test('groups a blocked thread with user actions only when the server includes it', () => {
    const blocked = thread({ stage: 'blocked' })
    expect(groupOfThread(blocked, new Set())).toBe('idle')
    expect(groupOfThread(blocked, new Set([blocked.id]))).toBe('waiting')
  })

  test('sorts by activity, title or creation without touching the list', () => {
    const list = [
      thread({ id: 'a', title: '评审 #10', createdAt: 3, activeAt: 1 }),
      thread({ id: 'b', title: '评审 #9', createdAt: 1, activeAt: 3 }),
      thread({ id: 'c', title: '修复', createdAt: 2, activeAt: 2 }),
    ]
    expect(ids(sortThreads(list))).toEqual(['b', 'c', 'a'])
    expect(ids(list)).toEqual(['a', 'b', 'c'])
  })

  test('extracts thread action IDs from ID-referenced needsMe items', () => {
    const items: NeedsMeItem[] = [
      { kind: 'thread', threadId: 'thr_1' },
      {
        kind: 'attention',
        attention: { id: 'att_1' } as AttentionView,
        threadId: 'thr_2',
        joinedIds: ['thr_3'],
      },
    ]
    expect([...threadActionIds(items)]).toEqual(['thr_1'])
  })
})
