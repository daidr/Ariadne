import { describe, expect, it } from 'vitest'
import { innerLink, sanitizeUrl } from './links'

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

  it('drops images', () => {
    expect(sanitizeUrl('https://example.com/a.png', 'image')).toBeUndefined()
  })
})
