import type { LinkPreview, LinkProvider } from '@ariadne/plugin-sdk'
import { describe, expect, it } from 'vitest'
import { LinkResolver } from './links'

const pr: LinkPreview = {
  kind: 'pull-request',
  repository: 'acme/app',
  number: 3,
  url: 'https://example.com/acme/app/pull/3',
  title: 'Fix it',
  state: 'open',
  draft: false,
  author: 'someone',
  additions: 1,
  deletions: 2,
  changedFiles: 3,
  at: '2026-11-04T00:00:00Z',
}

function provider(overrides: Partial<LinkProvider> = {}): LinkProvider & { asked: number } {
  const out = {
    asked: 0,
    kind: 'demo',
    title: { 'zh-CN': '演示' },
    match: (url: URL) =>
      url.host === 'example.com' && url.pathname.includes('/pull/')
        ? ({ kind: 'pull-request', label: 'acme/app#3' } as const)
        : null,
    async preview() {
      out.asked++
      return pr
    },
    ...overrides,
  }
  return out
}

describe('link resolver', () => {
  it('names the links a plugin knows, from the address alone', () => {
    const links = new LinkResolver(new Map([['demo', provider()]]))
    expect(
      links.summarize([
        'https://example.com/acme/app/pull/3',
        'https://example.com/acme/app',
        'http://example.com/acme/app/pull/3',
        'not a url',
      ]),
    ).toEqual({
      'https://example.com/acme/app/pull/3': { kind: 'pull-request', label: 'acme/app#3' },
      'https://example.com/acme/app': null,
      'http://example.com/acme/app/pull/3': null,
      'not a url': null,
    })
  })

  it('keeps a plugin that throws from breaking the others', () => {
    const broken = provider({
      kind: 'broken',
      match: () => {
        throw new Error('boom')
      },
    })
    const links = new LinkResolver(
      new Map<string, LinkProvider>([
        ['broken', broken],
        ['demo', provider()],
      ]),
    )
    expect(links.summarize(['https://example.com/acme/app/pull/3'])).toEqual({
      'https://example.com/acme/app/pull/3': { kind: 'pull-request', label: 'acme/app#3' },
    })
  })

  it('reuses a preview for a minute, and asks again after a failure', async () => {
    let now = 0
    const demo = provider()
    const links = new LinkResolver(new Map([['demo', demo]]), () => now)
    const url = 'https://example.com/acme/app/pull/3'
    expect(await links.preview(url)).toEqual(pr)
    expect(await links.preview(url)).toEqual(pr)
    expect(demo.asked).toBe(1)
    now = 61_000
    await links.preview(url)
    expect(demo.asked).toBe(2)

    let fail = true
    const flaky = provider({
      async preview() {
        if (fail) throw new Error('rate limited')
        return pr
      },
    })
    const again = new LinkResolver(new Map([['demo', flaky]]), () => now)
    expect(await again.preview(url)).toBeNull()
    fail = false
    expect(await again.preview(url)).toEqual(pr)
    expect(await again.preview('https://example.com/elsewhere')).toBeNull()
  })

  it('drops a preview of another kind than its link', async () => {
    const odd = provider({
      async preview() {
        return { ...pr, kind: 'issue' } as unknown as LinkPreview
      },
    })
    const links = new LinkResolver(new Map([['demo', odd]]))
    expect(await links.preview('https://example.com/acme/app/pull/3')).toBeNull()
  })
})
