import type { MarkdownUrlKind } from 'dou-ui'
import type { ProjectSource } from '@ariadne/core'

/**
 * Something in Ariadne a message points at: `[title](thread:thr_…)`, `[title](artifact:art_…)`,
 * or an image kept from a thread's workspace, `![alt](file:fil_…)`.
 */
export type InnerLink = { kind: 'thread' | 'artifact' | 'file'; id: string }

const INNER = /^(thread|artifact):([\w-]+)$|^file:(fil_[\w-]+)$/

export function innerLink(href: string | undefined): InnerLink | null {
  const match = href ? INNER.exec(href.trim()) : null
  if (!match) return null
  return match[3]
    ? { kind: 'file', id: match[3] }
    : { kind: match[1] as InnerLink['kind'], id: match[2]! }
}

/** Where the page loads a kept file from. */
export const fileSrc = (id: string) => `/api/files/${id}`

/**
 * Which addresses in an agent's Markdown are kept. Threads, artifacts and kept images of
 * Ariadne, and full https and mailto addresses the user opens by clicking. Relative
 * addresses would open inside Ariadne, so only their text stays. Images show only when
 * Ariadne keeps them: loading any other would send a request the user never chose to
 * whatever address the text names.
 */
export function sanitizeUrl(url: string, kind: MarkdownUrlKind): string | undefined {
  const inner = innerLink(url)
  if (kind === 'image') return inner?.kind === 'file' ? fileSrc(inner.id) : undefined
  if (kind === 'link' && inner) return url.trim()
  return /^(?:https:\/\/|mailto:)/i.test(url.trim()) ? url.trim() : undefined
}

/** A link written as its address alone, such as a bare `https://…` in the text. */
export function isBare(node: {
  href?: string | undefined
  children: readonly { type: string; value?: string }[]
}): boolean {
  const [only, ...rest] = node.children
  return !rest.length && only?.type === 'text' && !!node.href && only.value === node.href
}

const repositoryPath = (url: URL) => url.pathname.replace(/\/+$/, '').replace(/\.git$/i, '')

export function sameHostedRepository(
  source: ProjectSource | null | undefined,
  repositoryUrl: string,
) {
  const host = source?.hosting ?? source
  if (host?.kind !== 'github' && host?.kind !== 'gitlab') return false
  const repository = host.settings.repository
  if (typeof repository !== 'string') return false
  const instance = host.settings.instance
  const base =
    host.kind === 'github'
      ? 'https://github.com'
      : typeof instance === 'string' && instance.trim()
        ? instance.trim().replace(/\/+$/, '')
        : 'https://gitlab.com'
  const address = repository.trim().replace(/^(?:ssh:\/\/)?git@([^:/]+)[:/]/, 'https://$1/')
  try {
    const local = new URL(/^https?:\/\//i.test(address) ? address : `${base}/${address}`)
    const link = new URL(repositoryUrl)
    const path = repositoryPath(local)
    const target = repositoryPath(link)
    return (
      local.host === link.host &&
      (host.kind === 'github' ? path.toLowerCase() === target.toLowerCase() : path === target)
    )
  } catch {
    return false
  }
}
