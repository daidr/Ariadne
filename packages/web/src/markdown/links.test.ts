import { describe, expect, it } from 'vitest'
import { innerLink, isBare, sanitizeUrl } from './links'

describe('message links', () => {
  it('reads thread and artifact links', () => {
    expect(innerLink('thread:thr_abc-1')).toEqual({ kind: 'thread', id: 'thr_abc-1' })
    expect(innerLink('artifact:art_x')).toEqual({ kind: 'artifact', id: 'art_x' })
    expect(innerLink('thread:')).toBeNull()
    expect(innerLink('thread:a/b')).toBeNull()
    expect(innerLink(undefined)).toBeNull()
  })

  it('keeps inner links and full addresses only', () => {
    expect(sanitizeUrl('thread:thr_1', 'link')).toBe('thread:thr_1')
    expect(sanitizeUrl('https://example.com/a', 'link')).toBe('https://example.com/a')
    expect(sanitizeUrl('mailto:a@b.c', 'link')).toBe('mailto:a@b.c')
    expect(sanitizeUrl('http://example.com', 'link')).toBeUndefined()
    expect(sanitizeUrl('src/App.vue:12', 'link')).toBeUndefined()
    expect(sanitizeUrl('/api/files/x', 'link')).toBeUndefined()
    expect(sanitizeUrl('javascript:alert(1)', 'link')).toBeUndefined()
    expect(sanitizeUrl('thread:thr_1', 'source')).toBeUndefined()
  })

  it('shows only kept images', () => {
    expect(innerLink('file:fil_Ab-9')).toEqual({ kind: 'file', id: 'fil_Ab-9' })
    expect(innerLink('file:/etc/passwd')).toBeNull()
    expect(sanitizeUrl('file:fil_Ab-9', 'image')).toBe('/api/files/fil_Ab-9')
    expect(sanitizeUrl('file:fil_Ab-9', 'link')).toBe('file:fil_Ab-9')
    expect(sanitizeUrl('https://example.com/a.png', 'image')).toBeUndefined()
    expect(sanitizeUrl('shots/a.png', 'image')).toBeUndefined()
    expect(sanitizeUrl('thread:thr_1', 'image')).toBeUndefined()
  })
})

const text = (value: string) => ({ type: 'text', value })

describe('bare links', () => {
  it('tells an address written alone from a link with words', () => {
    expect(isBare({ href: 'https://a.test/x', children: [text('https://a.test/x')] })).toBe(true)
    expect(isBare({ href: 'https://a.test/x', children: [text('这个 PR')] })).toBe(false)
    expect(
      isBare({ href: 'https://a.test/x', children: [text('https://a.test/x'), text('!')] }),
    ).toBe(false)
  })
})
