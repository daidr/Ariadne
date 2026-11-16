import type { SearchProvider } from '@ariadne/plugin-sdk'

export const SEARCH_SERVICES = {
  tavily: { title: 'Tavily', baseUrl: 'https://api.tavily.com' },
  deepseek: { title: 'DeepSeek Search API', baseUrl: 'https://api.deepseek.com/v1/tools' },
} as const
export type SearchServiceKind = keyof typeof SEARCH_SERVICES
export interface SearchServiceSettings {
  apiKey: string
  baseUrl: string
}
export interface SearchConfig {
  provider: SearchServiceKind
  services: Record<SearchServiceKind, SearchServiceSettings>
}

export class SearchHttpError extends Error {
  constructor(readonly status: number) {
    super(`HTTP ${status}`)
  }
}

/**
 * The independent search API used after plugins and model services, with its own key
 * and endpoint. `fetchPage` swaps the request for tests.
 */
export function keyedSearch(
  apiKey: string,
  fetchPage: typeof fetch = fetch,
  options: { provider?: SearchServiceKind; baseUrl?: string } = {},
): SearchProvider {
  const provider = options.provider ?? 'tavily'
  const service = SEARCH_SERVICES[provider]
  const endpoint = `${(options.baseUrl || service.baseUrl).replace(/\/+$/, '')}/search`
  return {
    kind: provider,
    title: { 'zh-CN': service.title, en: service.title },
    async search(query, { limit, signal }) {
      if (provider === 'deepseek' && Array.from(query).length > 128) {
        throw new Error('DeepSeek search query must be at most 128 characters')
      }
      const response = await fetchPage(endpoint, {
        method: 'POST',
        redirect: 'error',
        headers: { 'content-type': 'application/json', authorization: `Bearer ${apiKey}` },
        body: JSON.stringify(
          provider === 'deepseek'
            ? { query, count: Math.min(50, limit), need_content: false }
            : { query, max_results: limit },
        ),
        ...(signal ? { signal } : {}),
      })
      if (!response.ok) throw new SearchHttpError(response.status)
      const body = (await response.json()) as {
        results?: {
          title?: unknown
          url?: unknown
          content?: unknown
          snippet?: unknown
          summary?: unknown
        }[]
      }
      if (!body || !Array.isArray(body.results)) throw new Error('Invalid search response')
      return body.results.flatMap((hit) =>
        hit && typeof hit.url === 'string'
          ? [
              {
                title: typeof hit.title === 'string' ? hit.title : '',
                url: hit.url,
                snippet:
                  provider === 'deepseek'
                    ? typeof hit.summary === 'string'
                      ? hit.summary
                      : typeof hit.snippet === 'string'
                        ? hit.snippet
                        : ''
                    : typeof hit.content === 'string'
                      ? hit.content
                      : '',
              },
            ]
          : [],
      )
    },
  }
}
