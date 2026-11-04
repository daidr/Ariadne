import type { MarkdownUrlKind } from 'dou-ui'

/** Something in Ariadne a message points at: `[title](thread:thr_…)`, `[title](artifact:art_…)`. */
export type InnerLink = { kind: 'thread' | 'artifact'; id: string }

const INNER = /^(thread|artifact):([\w-]+)$/

export function innerLink(href: string | undefined): InnerLink | null {
  const match = href ? INNER.exec(href.trim()) : null
  return match ? { kind: match[1] as InnerLink['kind'], id: match[2]! } : null
}

/**
 * Which addresses in an agent's Markdown are kept. Threads and artifacts of Ariadne, and
 * full https and mailto addresses the user opens by clicking. Relative addresses would
 * open inside Ariadne, so only their text stays. Images are dropped: loading one would
 * send a request the user never chose to whatever address the text names.
 */
export function sanitizeUrl(url: string, kind: MarkdownUrlKind): string | undefined {
  if (kind === 'image') return undefined
  if (kind === 'link' && innerLink(url)) return url.trim()
  return /^(?:https:\/\/|mailto:)/i.test(url.trim()) ? url.trim() : undefined
}
