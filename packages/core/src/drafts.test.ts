import { afterEach, describe, expect, test, vi } from 'vitest'
import { draftShows, ReplyDrafts, type DraftUpdate } from './live'

afterEach(() => vi.useRealTimers())

function watch(drafts: ReplyDrafts) {
  const seen: (DraftUpdate['draft'] | null)[] = []
  drafts.subscribe((update) => seen.push(update.draft && { ...update.draft }))
  return seen
}

describe('reply drafts', () => {
  test('hold back a short first line, which may be what the model does next', () => {
    expect(draftShows('先跑一下测试')).toBe(false)
    expect(draftShows('先跑一下测试\n')).toBe(false)
    expect(draftShows('改好了。\n\n- 第一处')).toBe(true)
    expect(draftShows('长'.repeat(120))).toBe(true)
  })

  test('show as they grow, a few times a second, and hand over to the message', () => {
    vi.useFakeTimers()
    const drafts = new ReplyDrafts(() => 7)
    const seen = watch(drafts)
    const draft = drafts.start({ projectId: 'p', conversationId: 'c', messageId: 'msg_1' })
    draft.text('改好了')
    expect(seen).toEqual([])
    draft.text('。\n\n- 第一处')
    expect(seen).toEqual([{ messageId: 'msg_1', text: '改好了。\n\n- 第一处', startedAt: 7 }])
    draft.text('\n- 第二处')
    draft.text('\n- 第三处')
    expect(seen).toHaveLength(1)
    vi.advanceTimersByTime(150)
    expect(seen.at(-1)?.text).toBe('改好了。\n\n- 第一处\n- 第二处\n- 第三处')
    expect(drafts.list('p')).toHaveLength(1)
    expect(drafts.list('other')).toHaveLength(0)
    draft.text('\n- 第四处')
    draft.posted()
    vi.advanceTimersByTime(150)
    expect(seen.at(-1)).toEqual({
      messageId: 'msg_1',
      text: '改好了。\n\n- 第一处\n- 第二处\n- 第三处\n- 第四处',
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
    const short = drafts.start({ projectId: 'p', conversationId: 'c', messageId: 'msg_1' })
    short.text('好的')
    short.posted()
    const line = drafts.start({ projectId: 'p', conversationId: 'c', messageId: 'msg_2' })
    line.text('先看看文件')
    line.withdraw()
    expect(seen).toEqual([])
  })

  test('a withdrawn draft goes, and may start over', () => {
    const drafts = new ReplyDrafts(() => 1)
    const seen = watch(drafts)
    const draft = drafts.start({ projectId: 'p', conversationId: 'c', messageId: 'msg_1' })
    draft.text('第一行\n第二行')
    draft.withdraw()
    expect(seen).toEqual([{ messageId: 'msg_1', text: '第一行\n第二行', startedAt: 1 }, null])
    draft.text('重来\n一次')
    expect(seen.at(-1)?.text).toBe('重来\n一次')
    // The next step's draft takes its place.
    drafts.start({ projectId: 'p', conversationId: 'c', messageId: 'msg_2' })
    expect(seen.at(-1)).toBeNull()
    expect(drafts.list()).toEqual([])
  })

  test('a shown draft is held when a tool call begins, until later text shows', () => {
    vi.useFakeTimers()
    const drafts = new ReplyDrafts(() => 1)
    const seen = watch(drafts)
    const first = drafts.start({ projectId: 'p', conversationId: 'c', messageId: 'msg_1' })
    first.text('答案是这样。\n\n')
    first.text('- 第一点')
    first.hold()
    first.text('不再变长')
    expect(seen.at(-1)).toEqual({
      messageId: 'msg_1',
      text: '答案是这样。\n\n- 第一点',
      startedAt: 1,
      held: true,
    })
    expect(first.shown).toBe('答案是这样。\n\n- 第一点')
    // The next step starts: what the user read stays while it writes one short line.
    const second = drafts.start({ projectId: 'p', conversationId: 'c', messageId: 'msg_2' })
    second.text('再看看')
    second.hold()
    expect(second.shown).toBeNull()
    expect(first.shown).not.toBeNull()
    const third = drafts.start({ projectId: 'p', conversationId: 'c', messageId: 'msg_3' })
    third.text('新的回答\n第二行')
    expect(seen.at(-1)).toEqual({ messageId: 'msg_3', text: '新的回答\n第二行', startedAt: 1 })
    expect(first.shown).toBeNull()
    // The one it took the place of changes nothing any more.
    first.posted()
    first.withdraw()
    expect(drafts.list().map((update) => update.draft?.messageId)).toEqual(['msg_3'])
  })

  test('a held draft can be posted, and a stopped turn drops it', () => {
    const drafts = new ReplyDrafts(() => 1)
    const seen = watch(drafts)
    const held = drafts.start({ projectId: 'p', conversationId: 'c', messageId: 'msg_1' })
    held.text('第一行\n第二行')
    held.hold()
    held.posted()
    expect(seen.at(-1)).toMatchObject({ messageId: 'msg_1', posted: true })
    const again = drafts.start({ projectId: 'p', conversationId: 'c', messageId: 'msg_2' })
    again.text('第一行\n第二行')
    again.hold()
    drafts.drop('c')
    expect(seen.at(-1)).toBeNull()
    expect(drafts.list()).toEqual([])
  })
})
