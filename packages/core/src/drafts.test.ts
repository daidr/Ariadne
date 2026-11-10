import { afterEach, describe, expect, test, vi } from 'vitest'
import { ReplyDrafts, type DraftUpdate } from './live'

afterEach(() => vi.useRealTimers())

function watch(drafts: ReplyDrafts) {
  const seen: (DraftUpdate['draft'] | null)[] = []
  drafts.subscribe((update) => seen.push(update.draft && { ...update.draft }))
  return seen
}

describe('reply drafts', () => {
  test('show as they grow, a few times a second, and hand over to the message', () => {
    vi.useFakeTimers()
    const drafts = new ReplyDrafts(() => 7)
    const seen = watch(drafts)
    const draft = drafts.start({ projectId: 'p', conversationId: 'c', messageId: 'msg_1' })
    draft.write('')
    expect(seen).toEqual([])
    draft.write('改好了')
    expect(seen).toEqual([{ messageId: 'msg_1', text: '改好了', startedAt: 7 }])
    draft.write('改好了。\n\n- 第一处')
    draft.write('改好了。\n\n- 第一处\n- 第二处')
    expect(seen).toHaveLength(1)
    vi.advanceTimersByTime(150)
    expect(seen.at(-1)?.text).toBe('改好了。\n\n- 第一处\n- 第二处')
    expect(drafts.list('p')).toHaveLength(1)
    expect(drafts.list('other')).toHaveLength(0)
    draft.write('改好了。\n\n- 第一处\n- 第二处\n- 第三处')
    draft.posted()
    vi.advanceTimersByTime(150)
    expect(seen.at(-1)).toEqual({
      messageId: 'msg_1',
      text: '改好了。\n\n- 第一处\n- 第二处\n- 第三处',
      startedAt: 7,
      posted: true,
    })
    expect(drafts.list()).toEqual([])
    draft.withdraw()
    drafts.drop('c')
    expect(seen).toHaveLength(3)
  })

  test('a draft never shown is posted and withdrawn without a word', () => {
    const drafts = new ReplyDrafts()
    const seen = watch(drafts)
    drafts.start({ projectId: 'p', conversationId: 'c', messageId: 'msg_1' }).posted()
    drafts.start({ projectId: 'p', conversationId: 'c', messageId: 'msg_2' }).withdraw()
    expect(seen).toEqual([])
  })

  test('a withdrawn draft goes, and may start over', () => {
    const drafts = new ReplyDrafts(() => 1)
    const seen = watch(drafts)
    const draft = drafts.start({ projectId: 'p', conversationId: 'c', messageId: 'msg_1' })
    draft.write('第一行')
    draft.withdraw()
    expect(seen).toEqual([{ messageId: 'msg_1', text: '第一行', startedAt: 1 }, null])
    draft.write('重来')
    expect(seen.at(-1)?.text).toBe('重来')
    // The next message's draft takes its place.
    drafts.start({ projectId: 'p', conversationId: 'c', messageId: 'msg_2' })
    expect(seen.at(-1)).toBeNull()
    expect(drafts.list()).toEqual([])
  })
})
