import type { AttentionView, NeedsMeItem, ThreadStage, ThreadView } from '@ariadne/core'
import { describe, expect, test } from 'vitest'
import {
  groupOf,
  groupOfThread,
  sortThreads,
  statusLine,
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

  test('keeps active groups in stable creation order even as concurrent activity updates activeAt', () => {
    const initial = [
      thread({ id: 'thr_1', title: '任务 1', stage: 'working', createdAt: 100, activeAt: 100 }),
      thread({ id: 'thr_2', title: '任务 2', stage: 'working', createdAt: 200, activeAt: 150 }),
      thread({ id: 'thr_3', title: '任务 3', stage: 'queued', createdAt: 300, activeAt: 120 }),
    ]
    expect(ids(sortThreads(initial, 'working'))).toEqual(['thr_3', 'thr_2', 'thr_1'])
    expect(ids(initial)).toEqual(['thr_1', 'thr_2', 'thr_3'])

    // Subsequent turns / tool calls bump activeAt in arbitrary order; visual order stays fixed.
    const updated = [
      thread({ id: 'thr_1', title: '任务 1', stage: 'working', createdAt: 100, activeAt: 999 }),
      thread({ id: 'thr_2', title: '任务 2', stage: 'working', createdAt: 200, activeAt: 500 }),
      thread({ id: 'thr_3', title: '任务 3', stage: 'working', createdAt: 300, activeAt: 350 }),
    ]
    expect(ids(sortThreads(updated, 'working'))).toEqual(['thr_3', 'thr_2', 'thr_1'])
    expect(ids(sortThreads(updated, 'review'))).toEqual(['thr_3', 'thr_2', 'thr_1'])
    expect(ids(sortThreads(updated, 'waiting'))).toEqual(['thr_3', 'thr_2', 'thr_1'])
  })

  test('breaks equal createdAt ties deterministically by id', () => {
    const list = [
      thread({ id: 'thr_a', createdAt: 100, activeAt: 300 }),
      thread({ id: 'thr_c', createdAt: 100, activeAt: 100 }),
      thread({ id: 'thr_b', createdAt: 100, activeAt: 200 }),
    ]
    expect(ids(sortThreads(list, 'working'))).toEqual(['thr_c', 'thr_b', 'thr_a'])
  })

  test('sorts done and archived groups by latest activity first', () => {
    const list = [
      thread({ id: 'a', stage: 'done', createdAt: 3, activeAt: 1 }),
      thread({ id: 'b', stage: 'done', createdAt: 1, activeAt: 3 }),
      thread({ id: 'c', stage: 'done', createdAt: 2, activeAt: 2 }),
    ]
    expect(ids(sortThreads(list, 'done'))).toEqual(['b', 'c', 'a'])
    expect(ids(sortThreads(list, 'archived'))).toEqual(['b', 'c', 'a'])
    expect(ids(list)).toEqual(['a', 'b', 'c'])
  })

  test('keeps a stable status line across queued and working transitions', () => {
    const queued = statusLine(thread({ stage: 'queued', doing: null }), null)
    const workingBlank = statusLine(thread({ stage: 'working', doing: null }), null)
    const workingDoing = statusLine(thread({ stage: 'working', doing: '检查测试' }), null)
    const answerFallback = statusLine(thread({ stage: 'answer' }), null)

    expect(queued).toEqual({ text: expect.any(String), live: false })
    expect(queued?.text.length).toBeGreaterThan(0)
    expect(workingBlank).toEqual({ text: expect.any(String), live: true })
    expect(workingBlank?.text.length).toBeGreaterThan(0)
    expect(workingDoing).toEqual({ text: '检查测试', live: true })
    expect(answerFallback).toEqual({ text: expect.any(String), live: false })
    expect(answerFallback?.text.length).toBeGreaterThan(0)
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
