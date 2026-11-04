import type { LinkPreview, LinkSummary } from '@ariadne/plugin-sdk'
import { reactive } from 'vue'
import { api } from '../api'

/**
 * What plugins say the links shown name, by address: `null` for a plain link, missing
 * while it is being asked. Addresses shown together are asked in one request.
 */
const summaries = reactive(new Map<string, LinkSummary | null>())
let waiting = new Set<string>()
const BATCH = 200
let flush: Promise<void> | null = null

export function linkSummary(url: string): LinkSummary | null | undefined {
  return summaries.get(url)
}

export function askLinkSummary(url: string): void {
  if (summaries.has(url) || waiting.has(url)) return
  waiting.add(url)
  flush ??= Promise.resolve().then(async () => {
    const urls = [...waiting]
    waiting = new Set()
    flush = null
    // The server answers at most this many at once.
    for (let at = 0; at < urls.length; at += BATCH) {
      const batch = urls.slice(at, at + BATCH)
      try {
        const { links } = await api.summarizeLinks(batch)
        for (const each of batch) summaries.set(each, links[each] ?? null)
      } catch {
        // Shown as plain links; asked again on the next page load.
        for (const each of batch) summaries.set(each, null)
      }
    }
  })
}

const PREVIEW_TTL_MS = 60_000
const previews = new Map<string, { at: number; value: Promise<LinkPreview | null> }>()

/** The card over a link, asked when the user points at it and kept a minute. */
export function linkPreview(url: string): Promise<LinkPreview | null> {
  const cached = previews.get(url)
  if (cached && Date.now() - cached.at < PREVIEW_TTL_MS) return cached.value
  const value = api.previewLink(url).then(
    ({ preview }) => preview,
    () => null,
  )
  previews.set(url, { at: Date.now(), value })
  return value
}
