import type { ModelPrice } from '@ariadne/agent'

/** The public catalog of models and their list prices (US dollars per million tokens). */
export const CATALOG_URL = 'https://models.dev/api.json'
/** The catalog is fetched again after this long. */
const CATALOG_MS = 12 * 60 * 60 * 1000
const FETCH_TIMEOUT_MS = 15_000

/** Services that sell their own models, tried first when several list the same one. */
const MAKERS = [
  'anthropic',
  'openai',
  'deepseek',
  'google',
  'xai',
  'mistral',
  'moonshotai',
  'alibaba',
  'zhipuai',
  'minimax',
]

interface CatalogModel {
  id?: string
  cost?: { input?: number; output?: number; cache_read?: number; cache_write?: number }
}
type Catalog = Record<string, { id?: string; models?: Record<string, CatalogModel> }>

/** A model's list price as the catalog has it, and which service lists it at that price. */
export interface PriceMatch {
  price: ModelPrice
  source: string
}

/**
 * List prices from the public catalog, for filling in a model's price. Asked only when
 * the user clicks for it; what comes back is only a suggestion they save or not.
 */
export class PriceCatalog {
  private catalog: { at: number; data: Catalog } | null = null

  constructor(private readonly fetcher: typeof fetch = fetch) {}

  /**
   * The price of `model`. `hint` (the provider's kind and address) picks the service when
   * the name is listed by several, e.g. a DeepSeek model on its own address.
   */
  async lookup(model: string, hint = ''): Promise<PriceMatch | null> {
    const catalog = await this.load()
    const name = model.trim().toLowerCase()
    const matches: { provider: string; model: CatalogModel }[] = []
    for (const [provider, entry] of Object.entries(catalog)) {
      for (const [id, found] of Object.entries(entry.models ?? {})) {
        const key = id.toLowerCase()
        if (key === name || key.endsWith(`/${name}`)) matches.push({ provider, model: found })
      }
    }
    const priced = matches.filter(
      (m) => typeof m.model.cost?.input === 'number' && typeof m.model.cost?.output === 'number',
    )
    if (!priced.length) return null
    const lowerHint = hint.toLowerCase()
    const rank = (provider: string) => {
      if (lowerHint && lowerHint.includes(provider.toLowerCase())) return -1
      const maker = MAKERS.indexOf(provider)
      return maker === -1 ? MAKERS.length : maker
    }
    const best = priced.toSorted((a, b) => rank(a.provider) - rank(b.provider))[0]!
    const cost = best.model.cost!
    return {
      source: best.provider,
      price: {
        currency: 'USD',
        input: cost.input!,
        output: cost.output!,
        ...(typeof cost.cache_read === 'number' ? { cacheRead: cost.cache_read } : {}),
        ...(typeof cost.cache_write === 'number' ? { cacheWrite: cost.cache_write } : {}),
      },
    }
  }

  private async load(): Promise<Catalog> {
    if (this.catalog && Date.now() - this.catalog.at < CATALOG_MS) return this.catalog.data
    const response = await this.fetcher(CATALOG_URL, {
      signal: AbortSignal.timeout(FETCH_TIMEOUT_MS),
      headers: { Accept: 'application/json' },
    })
    if (!response.ok) throw new Error(`the price catalog answered ${response.status}`)
    const data = (await response.json()) as Catalog
    if (!data || typeof data !== 'object') throw new Error('the price catalog is not a catalog')
    this.catalog = { at: Date.now(), data }
    return data
  }
}
