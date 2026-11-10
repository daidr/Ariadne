import type { ModelPrice } from '@ariadne/agent'
import type { Effort } from '@ariadne/plugin-sdk'

/**
 * The public catalog of models: their list prices (US dollars per million tokens), how
 * much they read and write, and what they can do.
 */
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
  limit?: { context?: number; output?: number }
  modalities?: { input?: string[] }
  tool_call?: boolean
  reasoning?: boolean
  release_date?: string
}
type Catalog = Record<string, { id?: string; models?: Record<string, CatalogModel> }>

/** A model's list price as the catalog has it, and which service lists it at that price. */
export interface PriceMatch {
  price: ModelPrice
  source: string
}

/**
 * What the catalog knows of a model besides its price, each only where it says: the form
 * fills a model's settings in with it, and the user changes what is wrong.
 */
export interface ModelInfo {
  contextTokens?: number
  /** The most it writes in one response. */
  maxTokens?: number
  /** Whether it looks at images. */
  images?: boolean
  price?: ModelPrice
  /** Whether it calls tools; Ariadne works through them. */
  tools?: boolean
  /** The day it came out, `YYYY-MM-DD`. */
  released?: string
  /**
   * The efforts it accepts, least first: empty when how hard it thinks cannot be set.
   * Left out when unknown, and the common ones are offered.
   */
  efforts?: Effort[]
}

const priced = (model: CatalogModel) =>
  typeof model.cost?.input === 'number' && typeof model.cost?.output === 'number'

function priceOf(model: CatalogModel): ModelPrice {
  const cost = model.cost!
  return {
    currency: 'USD',
    input: cost.input!,
    output: cost.output!,
    ...(typeof cost.cache_read === 'number' ? { cacheRead: cost.cache_read } : {}),
    ...(typeof cost.cache_write === 'number' ? { cacheWrite: cost.cache_write } : {}),
  }
}

const whole = (value: unknown, least: number): number | undefined =>
  typeof value === 'number' && Number.isInteger(value) && value >= least ? value : undefined

/**
 * The public catalog of models, for filling in a model's settings. Asked only from the
 * settings form; what comes back is a suggestion the user saves or changes.
 */
export class ModelCatalog {
  private catalog: { at: number; data: Catalog } | null = null

  constructor(private readonly fetcher: typeof fetch = fetch) {}

  /**
   * The price of `model`. `hint` (the provider's kind and address) picks the service when
   * the name is listed by several, e.g. a DeepSeek model on its own address.
   */
  async lookup(model: string, hint = ''): Promise<PriceMatch | null> {
    const best = this.matches(await this.load(), model, hint).find((m) => priced(m.model))
    return best ? { source: best.provider, price: priceOf(best.model) } : null
  }

  /**
   * What the catalog knows of each of `models`, by name; a model it does not list is left
   * out. Each fact comes from the best service that states it.
   */
  async describe(models: string[], hint = ''): Promise<Map<string, ModelInfo>> {
    const catalog = await this.load()
    const out = new Map<string, ModelInfo>()
    for (const model of models) {
      const found = this.matches(catalog, model, hint).map((m) => m.model)
      if (!found.length) continue
      const first = <T>(read: (model: CatalogModel) => T | undefined): T | undefined => {
        for (const entry of found) {
          const value = read(entry)
          if (value !== undefined) return value
        }
        return undefined
      }
      const contextTokens = first((m) => whole(m.limit?.context, 4096))
      const maxTokens = first((m) => whole(m.limit?.output, 256))
      const inputs = first((m) =>
        Array.isArray(m.modalities?.input) ? m.modalities.input : undefined,
      )
      const price = first((m) => (priced(m) ? priceOf(m) : undefined))
      const tools = first((m) => (typeof m.tool_call === 'boolean' ? m.tool_call : undefined))
      const reasoning = first((m) => (typeof m.reasoning === 'boolean' ? m.reasoning : undefined))
      const released = first((m) =>
        typeof m.release_date === 'string' && /^\d{4}-\d{2}/.test(m.release_date)
          ? m.release_date
          : undefined,
      )
      out.set(model, {
        ...(contextTokens ? { contextTokens } : {}),
        // More than it reads at once is a mistake in the catalog.
        ...(maxTokens && maxTokens <= 1_000_000 ? { maxTokens } : {}),
        ...(inputs ? { images: inputs.includes('image') } : {}),
        ...(price ? { price } : {}),
        ...(tools !== undefined ? { tools } : {}),
        ...(released ? { released } : {}),
        // A model that does not reason has no effort to set; the catalog says no more.
        ...(reasoning === false ? { efforts: [] } : {}),
      })
    }
    return out
  }

  /**
   * The catalog's entries for `model`, best first: the service `hint` (the provider's
   * kind and address) names, then those that make their own models.
   */
  private matches(
    catalog: Catalog,
    model: string,
    hint: string,
  ): { provider: string; model: CatalogModel }[] {
    const name = model.trim().toLowerCase()
    if (!name) return []
    const matches: { provider: string; model: CatalogModel }[] = []
    for (const [provider, entry] of Object.entries(catalog)) {
      for (const [id, found] of Object.entries(entry.models ?? {})) {
        const key = id.toLowerCase()
        if (key === name || key.endsWith(`/${name}`)) matches.push({ provider, model: found })
      }
    }
    const lowerHint = hint.toLowerCase()
    const rank = (provider: string) => {
      if (lowerHint && lowerHint.includes(provider.toLowerCase())) return -1
      const maker = MAKERS.indexOf(provider)
      return maker === -1 ? MAKERS.length : maker
    }
    return matches.toSorted((a, b) => rank(a.provider) - rank(b.provider))
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
