import { describe, expect, it } from 'vitest'
import { splitCode } from './markdown-code'

/** The code parts of a split, after checking the parts give the text back. */
function code(markdown: string): string[] {
  const parts = splitCode(markdown)
  expect(parts.join('')).toBe(markdown)
  expect(parts.length % 2).toBe(1)
  return parts.filter((_, index) => index % 2 === 1)
}

describe('splitCode', () => {
  it('finds fenced blocks, closed by a long enough fence of the same kind', () => {
    expect(code('a\n```ts\nx ``` y\n~~~\n```\nb')).toEqual(['```ts\nx ``` y\n~~~\n```\n'])
    expect(code('````\n```\n![a](a.png)\n````\nb')).toEqual(['````\n```\n![a](a.png)\n````\n'])
    expect(code('text\n~~~\nopen to the end')).toEqual(['~~~\nopen to the end'])
    // A fence inside a line is not one.
    expect(code('a ```b``` c')).toEqual(['```b```'])
  })

  it('finds indented blocks after a blank line', () => {
    expect(code('para\n\n    ![a](a.png)\n\n    more\nafter')).toEqual([
      '    ![a](a.png)\n\n    more\n',
    ])
    expect(code('\t![a](a.png)')).toEqual(['\t![a](a.png)'])
    // A paragraph's continuation line is not code.
    expect(code('para\n    ![a](a.png)')).toEqual([])
  })

  it('counts indentation from the list item', () => {
    expect(code('- item\n\n    ![a](a.png)')).toEqual([])
    expect(code('1. item\n\n       ![a](a.png)')).toEqual(['       ![a](a.png)'])
    expect(code('- item\n\nend of list\n\n    ![a](a.png)')).toEqual(['    ![a](a.png)'])
  })

  it('finds inline spans of matching backtick runs', () => {
    expect(code('a `b` c ``d ` e`` f')).toEqual(['`b`', '``d ` e``'])
    expect(code('a `b\nc` d')).toEqual(['`b\nc`'])
    // Not across a blank line, and not without a closing run.
    expect(code('a `b\n\nc` d')).toEqual([])
    expect(code('a ``b` c')).toEqual([])
  })
})
