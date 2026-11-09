import type { SearchProvider } from '@ariadne/plugin-sdk'

const ENDPOINT = 'https://api.tavily.com/search'

/**
 * The search service agents use when no model service searches itself: Tavily, with the
 * key the user saved in the settings. `fetchPage` swaps the request, for tests.
 */
export function keyedSearch(apiKey: string, fetchPage: typeof fetch = fetch): SearchProvider {
  return {
    kind: 'tavily',
    title: { 'zh-CN': 'Tavily', en: 'Tavily' },
    async search(query, { limit, signal }) {
      const response = await fetchPage(ENDPOINT, {
        method: 'POST',
        headers: { 'content-type': 'application/json', authorization: `Bearer ${apiKey}` },
        body: JSON.stringify({ query, max_results: limit }),
        ...(signal ? { signal } : {}),
      })
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      const body = (await response.json()) as {
        results?: { title?: unknown; url?: unknown; content?: unknown }[]
      }
      return (body.results ?? []).flatMap((hit) =>
        typeof hit.url === 'string'
          ? [
              {
                title: typeof hit.title === 'string' ? hit.title : '',
                url: hit.url,
                snippet: typeof hit.content === 'string' ? hit.content : '',
              },
            ]
          : [],
      )
    },
  }
}
