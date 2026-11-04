import type { ConversationView, MessageView, NoticeView, TodoView } from '@ariadne/core'
import { describe, expect, test } from 'vitest'
import { merge, partsOf, type ConversationState } from './store'

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
  })
})
