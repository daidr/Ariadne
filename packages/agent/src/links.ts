import type { LinkPreview, LinkProvider, LinkSummary } from '@ariadne/plugin-sdk'

/** How long a preview is reused: a page of messages points at the same pull requests often. */
const PREVIEW_TTL_MS = 60_000
/** Previews kept at once; the oldest go first. */
const MAX_CACHED = 500
const PREVIEW_TIMEOUT_MS = 10_000
/** Addresses one request may ask about. */
export const MAX_LINKS = 200

function parse(url: string): URL | null {
  if (url.length > 2000) return null
  try {
    const parsed = new URL(url)
    return parsed.protocol === 'https:' ? parsed : null
  } catch {
    return null
  }
}

/**
 * Links in messages that a plugin knows: what each names (asked for every link shown) and
 * the card over it (asked when the user points at one, kept a minute).
 */
export class LinkResolver {
  private readonly previews = new Map<string, { at: number; value: Promise<LinkPreview | null> }>()

  constructor(
    private readonly providers: ReadonlyMap<string, LinkProvider>,
    private readonly now: () => number = Date.now,
  ) {}

  private find(url: URL): { provider: LinkProvider; summary: LinkSummary } | null {
    for (const provider of this.providers.values()) {
      try {
        const summary = provider.match(url)
        if (summary) return { provider, summary }
      } catch {
        // A plugin that cannot read an address leaves it a plain link.
      }
    }
    return null
  }

  /** What each address names; `null` for one no plugin knows. */
  summarize(urls: string[]): Record<string, LinkSummary | null> {
    const out: Record<string, LinkSummary | null> = {}
    for (const url of urls.slice(0, MAX_LINKS)) {
      const parsed = parse(url)
      out[url] = parsed ? (this.find(parsed)?.summary ?? null) : null
    }
    return out
  }

  /** The card for one address; `null` when no plugin knows it or it cannot be read now. */
  async preview(url: string): Promise<LinkPreview | null> {
    const parsed = parse(url)
    const found = parsed && this.find(parsed)
    if (!parsed || !found) return null
    const key = parsed.href
    const cached = this.previews.get(key)
    if (cached && this.now() - cached.at < PREVIEW_TTL_MS) return cached.value
    const entry = { at: this.now(), value: Promise.resolve<LinkPreview | null>(null) }
    entry.value = (async () => {
      const signal = AbortSignal.timeout(PREVIEW_TIMEOUT_MS)
      try {
        const preview = await found.provider.preview(parsed, { signal })
        return preview?.kind === found.summary.kind ? preview : null
      } catch {
        // Not kept: the next look tries again.
        if (this.previews.get(key) === entry) this.previews.delete(key)
        return null
      }
    })()
    this.previews.delete(key)
    this.previews.set(key, entry)
    while (this.previews.size > MAX_CACHED) {
      this.previews.delete(this.previews.keys().next().value!)
    }
    return entry.value
  }
}
