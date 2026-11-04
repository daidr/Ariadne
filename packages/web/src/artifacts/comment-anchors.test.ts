// @vitest-environment happy-dom
import { editorKit, type PartialBlock } from '@dou-ui/editor'
import { blocksToMarkdown, markdownToBlocks } from '@dou-ui/editor/markdown'
import { describe, expect, test } from 'vitest'
import { anchoredIds, anchorQuotes } from './comment-anchors'

const extensions = [editorKit()]
const read = (markdown: string) => markdownToBlocks(markdown, extensions) as PartialBlock[]

describe('anchorQuotes', () => {
  test('marks a quote across formatting, and leaves the Markdown as it was', () => {
    const markdown = '# 方案\n\n开业当天**全场八折**，送帆布袋。\n'
    const before = blocksToMarkdown(read(markdown) as never, extensions)
    const { blocks, found } = anchorQuotes(read(markdown), [
      { id: 'a', quote: '当天全场' },
      { id: 'b', quote: '八折，送' },
      { id: 'gone', quote: '九折' },
    ])
    expect([...found]).toEqual(['a', 'b'])
    expect(blocks[1]?.content).toMatchObject([
      { text: '开业' },
      { text: '当天', comments: ['a'] },
      { text: '全场', comments: ['a'], marks: ['bold'] },
      { text: '八折', comments: ['b'], marks: ['bold'] },
      { text: '，送', comments: ['b'] },
      { text: '帆布袋。' },
    ])
    expect(blocksToMarkdown(blocks as never, extensions)).toBe(before)
    expect(anchoredIds(blocks)).toEqual(new Set(['a', 'b']))
  })

  test('a quote may run over two blocks, as the editor reads it', () => {
    const { blocks, found } = anchorQuotes(read('第一段结尾\n\n第二段开头\n'), [
      { id: 'x', quote: '结尾第二段' },
    ])
    expect(found.has('x')).toBe(true)
    expect(anchoredIds(blocks)).toEqual(new Set(['x']))
  })

  test('code blocks are not commented', () => {
    expect(
      anchorQuotes(read('```\nconst a = 1\n```\n'), [{ id: 'x', quote: 'const' }]).found.size,
    ).toBe(0)
  })
})
