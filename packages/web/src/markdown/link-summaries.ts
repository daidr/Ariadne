import { ExpiringCache } from '@ariadne/core/cache'
import type { LinkPreview, LinkSummary } from '@ariadne/plugin-sdk'
import { api } from '../api'

export const SUMMARY_LIMIT = 500
export const SUMMARY_TTL_MS = 5 * 60_000
export const PREVIEW_LIMIT = 100
export const PREVIEW_TTL_MS = 60_000
const BATCH = 200
const MAX_LINK_CHARS = 64_000
interface Pending<T> {
  value: Promise<T | null>
  resolve(value: T | null): void
}
function pending<T>(): Pending<T> {
  let resolve!: Pending<T>['resolve']
  const value = new Promise<T | null>((done) => {
    resolve = done
  })
  return { value, resolve }
}
const waiting = new Map<string, Pending<LinkSummary>>()
const summaries = new ExpiringCache<string, Pending<LinkSummary>>(
  SUMMARY_LIMIT,
  SUMMARY_TTL_MS,
  (url, entry) => {
    waiting.delete(url)
    entry.resolve(null)
  },
)
let flushing: Promise<void> | null = null
let batchController: AbortController | null = null

/** Mounted links hold their own result; expired cache entries do not erase their labels. */
export function linkSummary(url: string): Promise<LinkSummary | null> {
  if (url.length > 2000) return Promise.resolve(null)
  const kept = summaries.get(url)
  if (kept) return kept.value
  const entry = pending<LinkSummary>()
  summaries.set(url, entry)
  waiting.set(url, entry)
  flushing ??= Promise.resolve().then(flush)
  return entry.value
}

async function flush() {
  try {
    while (waiting.size) {
      const batch = [...waiting.entries()].slice(0, BATCH)
      for (const [url] of batch) waiting.delete(url)
      const controller = new AbortController()
      batchController = controller
      const timer = setTimeout(() => controller.abort(), 15_000)
      try {
        const { links } = await api.summarizeLinks(
          batch.map(([url]) => url),
          controller.signal,
        )
        for (const [url, entry] of batch) {
          const value = links[url] ?? null
          entry.resolve(summaries.peek(url) === entry && small(value) ? value : null)
        }
      } catch {
        for (const [, entry] of batch) entry.resolve(null)
      } finally {
        clearTimeout(timer)
        batchController = null
      }
    }
  } finally {
    flushing = null
  }
}

type Preview = Pending<LinkPreview> & { controller: AbortController }
const previews = new ExpiringCache<string, Preview>(
  PREVIEW_LIMIT,
  PREVIEW_TTL_MS,
  (_url, entry) => {
    entry.controller.abort()
    entry.resolve(null)
  },
)

export function linkPreview(url: string): Promise<LinkPreview | null> {
  if (url.length > 2000) return Promise.resolve(null)
  const kept = previews.get(url)
  if (kept) return kept.value
  const entry = { ...pending<LinkPreview>(), controller: new AbortController() }
  previews.set(url, entry)
  void api.previewLink(url, entry.controller.signal).then(
    ({ preview }) => entry.resolve(previews.peek(url) === entry && small(preview) ? preview : null),
    () => entry.resolve(null),
  )
  return entry.value
}

function small(value: unknown): boolean {
  return JSON.stringify(value).length <= MAX_LINK_CHARS
}

export function clearLinkCaches(): void {
  summaries.clear()
  previews.clear()
  batchController?.abort()
}
