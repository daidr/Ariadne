import { describe, expect, test } from 'vitest'
import { applyDraftAction, sendable, type DraftComment } from './changes/line-comments'
import { handleOf, mentionQuery } from './components/mentions'

const at = { path: 'src/a.ts', line: 3, quote: 'const a = 1' }
const draft = (id: string, extra: Partial<DraftComment> = {}): DraftComment => ({
  ...at,
  body: id,
  id,
  createdAt: '2026-11-05T00:00:00.000Z',
  ...extra,
})
const author = { id: 'user', name: '我' }

describe('line comment drafts', () => {
  test('a first comment and a reply add, an edit and a delete change one', () => {
    let drafts = applyDraftAction([], at, {
      type: 'create',
      thread: { id: 'x', comments: [{ id: 'c', author, body: ' 改 ', createdAt: '' }] },
    })
    expect(drafts.map((d) => [d.line, d.body, d.quote])).toEqual([[3, '改', 'const a = 1']])
    drafts = applyDraftAction(drafts, at, {
      type: 'reply',
      threadId: 'x',
      comment: { id: 'r', author, body: '还有这里', createdAt: '' },
    })
    expect(drafts).toHaveLength(2)
    const [first] = drafts
    drafts = applyDraftAction(drafts, at, {
      type: 'edit',
      threadId: 'x',
      commentId: first!.id,
      body: '改成 2',
      editedAt: '',
    })
    expect(drafts[0]?.body).toBe('改成 2')
    drafts = applyDraftAction(drafts, at, { type: 'delete', threadId: 'x', commentId: first!.id })
    expect(drafts.map((d) => d.body)).toEqual(['还有这里'])
  })

  test('resolving keeps the comments, removing the card drops only its line', () => {
    const drafts = [draft('a'), draft('b', { line: 9 }), draft('c', { path: 'b.ts' })]
    const resolved = applyDraftAction(drafts, at, {
      type: 'resolve',
      threadId: 'x',
      by: author,
      at: '',
    })
    expect(resolved.map((d) => d.id)).toEqual(['a', 'b', 'c'])
    const left = applyDraftAction(drafts, at, { type: 'remove', threadId: 'x' })
    expect(left.map((d) => d.id)).toEqual(['b', 'c'])
  })

  test('a line of the old version is its own card, apart from the same line number now', () => {
    const before = { ...at, side: 'original' as const }
    const drafts = [draft('a'), draft('b', { side: 'original' })]
    const left = applyDraftAction(drafts, before, { type: 'remove', threadId: 'x' })
    expect(left.map((d) => d.id)).toEqual(['a'])
    expect(sendable(drafts)).toEqual([
      { path: 'src/a.ts', line: 3, quote: 'const a = 1', body: 'b', side: 'original' },
      { path: 'src/a.ts', line: 3, quote: 'const a = 1', body: 'a' },
    ])
  })

  test('go out in file and line order, without their draft ids', () => {
    const drafts = [draft('a', { line: 9 }), draft('b', { path: 'a.ts', line: 1 }), draft('c')]
    expect(sendable(drafts)).toEqual([
      { path: 'a.ts', line: 1, quote: 'const a = 1', body: 'b' },
      { path: 'src/a.ts', line: 3, quote: 'const a = 1', body: 'c' },
      { path: 'src/a.ts', line: 9, quote: 'const a = 1', body: 'a' },
    ])
  })
})

describe('mentions in the input', () => {
  test('the @ word just before the caret', () => {
    expect(mentionQuery('看看 @修复', 6)).toBe('修复')
    expect(mentionQuery('@', 1)).toBe('')
    expect(mentionQuery('mail a@b.c', 10)).toBeNull()
    expect(mentionQuery('@done and more', 14)).toBeNull()
  })

  test('a title is one word after @', () => {
    expect(handleOf('Fix Safari  export')).toBe('FixSafariexport')
  })
})
