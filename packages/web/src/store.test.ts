import type { ConversationView, MessageView, NoticeView, ThreadView, TodoView } from '@ariadne/core'
import { describe, expect, test } from 'vitest'
import {
  backMain,
  closeFull,
  closeView,
  fullScreenPane,
  merge,
  openedViewOf,
  openMain,
  openThread,
  openView,
  partsOf,
  patchThreads,
  setSide,
  state,
  type ConversationState,
} from './store'
import * as side from './side/layout'

const conversation = { id: 'crd_1' } as ConversationView
const todo = { items: [] } as unknown as TodoView
const message = (id: string, content = id, createdAt = Number(id.slice(4))): MessageView =>
  ({ id, content, createdAt }) as MessageView
const notice = (id: string, createdAt: number, extra: object = {}): NoticeView =>
  ({ id, createdAt, kind: 'memory', ...extra }) as NoticeView

const shown = (over: Partial<ConversationState> = {}): ConversationState => ({
  conversation,
  messages: [message('msg_10'), message('msg_20')],
  notices: [notice('ntc_1', 15)],
  todo,
  model: null,
  more: true,
  from: 'msg_10',
  loadingEarlier: false,
  seq: 5,
  ...over,
})

const page = (over: { messages?: MessageView[]; notices?: NoticeView[]; lastSeq?: number }) => ({
  conversation,
  todo,
  model: null,
  more: false,
  messages: over.messages ?? [],
  notices: over.notices ?? [],
  lastSeq: over.lastSeq ?? 9,
})

describe('a conversation brought up to date', () => {
  test('adds new messages, replaces changed ones and keeps the rest', () => {
    const next = merge(
      shown(),
      page({ messages: [message('msg_20', '改过了'), message('msg_30')], lastSeq: 9 }),
    )
    expect(next.messages.map((m) => [m.id, m.content])).toEqual([
      ['msg_10', 'msg_10'],
      ['msg_20', '改过了'],
      ['msg_30', 'msg_30'],
    ])
    expect(next.seq).toBe(9)
    expect(next.more).toBe(true)
  })

  test('leaves out what lies before the first message shown; an earlier page brings it', () => {
    const next = merge(
      shown(),
      page({ messages: [message('msg_05')], notices: [notice('ntc_0', 5), notice('ntc_2', 25)] }),
    )
    expect(next.messages.map((m) => m.id)).toEqual(['msg_10', 'msg_20'])
    expect(next.notices.map((n) => n.id)).toEqual(['ntc_1', 'ntc_2'])
  })

  test('replaces a notice that changed, e.g. one undone', () => {
    const next = merge(shown(), page({ notices: [notice('ntc_1', 15, { undone: true })] }))
    expect(next.notices).toEqual([notice('ntc_1', 15, { undone: true })])
  })

  test('nothing new leaves the lists the ones they were', () => {
    const current = shown()
    const next = merge(current, page({}))
    expect(next.messages).toBe(current.messages)
    expect(next.notices).toBe(current.notices)
  })

  test('never goes back to an older event', () => {
    expect(merge(shown({ seq: 12 }), page({ lastSeq: 9 })).seq).toBe(12)
  })
})

describe('events', () => {
  test('refresh only the parts they change', () => {
    expect(partsOf('prompt.recorded')).toEqual([])
    expect(partsOf('turn.step')).toEqual(['detail', 'conversations'])
    expect(partsOf('tool.finished')).not.toContain('plan')
    expect(partsOf('turn.finished')).toContain('plan')
    expect(partsOf('memory.saved')).toEqual(['memories', 'conversations'])
    expect(partsOf('artifact.created')).toContain('artifacts')
    expect(partsOf('message.posted')).not.toContain('artifacts')
    expect(partsOf('message.read')).toEqual(['conversations'])
  })
})

describe('back in the middle column', () => {
  test('returns to where a view was opened from, then to the coordinator', () => {
    openMain({ kind: 'conversation' })
    openMain({ kind: 'artifacts' })
    openMain({ kind: 'trace', conversationId: 'c_1' })
    backMain()
    expect(state.main).toEqual({ kind: 'artifacts' })
    backMain()
    expect(state.main).toEqual({ kind: 'conversation' })
    backMain()
    expect(state.main).toEqual({ kind: 'conversation' })
  })

  test('a view opened again is not visited twice on the way back', () => {
    openMain({ kind: 'conversation' })
    openMain({ kind: 'artifacts' })
    openMain({ kind: 'trace', conversationId: 'c_1' })
    openMain({ kind: 'artifacts' })
    backMain()
    expect(state.main).toEqual({ kind: 'trace', conversationId: 'c_1' })
    backMain()
    expect(state.main).toEqual({ kind: 'conversation' })
  })
})

const tabs = () => side.groupsOf(state.side.root).flatMap(side.tabsOf)

describe('artifacts and changes', () => {
  test('open in a tab on the right side, beside the coordinator', async () => {
    setSide(side.defaultLayout())
    await openView({ kind: 'artifact', id: 'art_1' })
    expect(tabs()).toContain('artifact:art_1')
    expect(state.main).toEqual({ kind: 'conversation' })
    expect(state.full).toBeNull()
  })

  test('opened from a thread, show full screen with the thread beside them', async () => {
    setSide(side.defaultLayout())
    await openView({ kind: 'artifact', id: 'art_1' }, { from: 'th_1' })
    expect(state.full).toEqual({
      view: { kind: 'artifact', id: 'art_1' },
      threadId: 'th_1',
      fromSide: false,
    })
    expect(tabs()).not.toContain('artifact:art_1')
    closeFull()
    expect(state.full).toBeNull()
    expect(tabs()).not.toContain('artifact:art_1')
  })

  test('a tab taken full screen goes back to the side when closed', async () => {
    state.currentId = 'p_1'
    setSide(side.defaultLayout())
    const turn = {
      turnId: 'tu_1',
      from: 'a',
      to: 'b',
      files: [],
      total: { files: 0, added: 0, removed: 0 },
    }
    await openView({ kind: 'changes', threadId: 'th_1', turn })
    fullScreenPane('turn:th_1:tu_1')
    expect(tabs()).not.toContain('turn:th_1:tu_1')
    // The changes come with their thread beside them.
    expect(state.full?.threadId).toBe('th_1')
    closeFull()
    expect(tabs()).toContain('turn:th_1:tu_1')
    expect(openedViewOf('turn:th_1:tu_1')).toEqual({ kind: 'changes', threadId: 'th_1', turn })
    state.currentId = null
  })

  test('a merged branch closes, wherever it is shown', async () => {
    setSide(side.defaultLayout())
    await openView({ kind: 'changes', threadId: 'th_1' })
    closeView({ kind: 'changes', threadId: 'th_1' })
    expect(tabs()).not.toContain('changes:th_1')
    await openView({ kind: 'changes', threadId: 'th_1' }, { full: true })
    closeView({ kind: 'changes', threadId: 'th_1' })
    expect(state.full).toBeNull()
  })

  test('a tab taken full screen goes back to the side when something else takes its place', async () => {
    setSide(side.defaultLayout())
    await openView({ kind: 'changes', threadId: 'th_1' })
    fullScreenPane('changes:th_1')
    await openView({ kind: 'artifact', id: 'art_1' }, { from: 'th_1' })
    expect(state.full?.view).toEqual({ kind: 'artifact', id: 'art_1' })
    expect(tabs()).toContain('changes:th_1')
    closeFull()
  })

  test('a thread opened over a full-screen view shows beside it, not behind it', async () => {
    setSide(side.defaultLayout())
    await openView({ kind: 'changes', threadId: 'th_1' }, { from: 'th_2' })
    await openThread('th_1')
    expect(state.full).toEqual({
      view: { kind: 'changes', threadId: 'th_1' },
      threadId: 'th_1',
      fromSide: false,
    })
    expect(tabs()).not.toContain(side.threadKey('th_1'))
    closeFull()
    await openThread('th_1')
    expect(tabs()).toContain(side.threadKey('th_1'))
  })
})

const row = (id: string, title = id) => ({ id, title }) as ThreadView

describe('patchThreads', () => {
  const had = [row('a'), row('b')]

  test('puts the changed threads in place of the ones had, keeping the rest', () => {
    const changed = row('b', 'renamed')
    const next = patchThreads(had, [changed], undefined)
    expect(next).toEqual([had[0], changed])
    expect(next![0]).toBe(had[0])
    expect(patchThreads(had, [], undefined)).toEqual(had)
  })

  test('follows the ids when threads came or went', () => {
    const added = row('c')
    expect(patchThreads(had, [added], ['a', 'b', 'c'])).toEqual([...had, added])
    expect(patchThreads(had, [], ['b'])).toEqual([had[1]])
  })

  test('gives up on a thread it was never sent', () => {
    expect(patchThreads(had, [row('c')], undefined)).toBeNull()
    expect(patchThreads(had, [], ['a', 'b', 'c'])).toBeNull()
  })
})
