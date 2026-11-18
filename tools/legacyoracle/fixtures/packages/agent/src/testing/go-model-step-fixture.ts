import type { ModelStreamPart, ModelProvider } from '@ariadne/plugin-sdk'
import { DEFAULT_LIMITS } from '../limits'
import { requestStep, TurnError } from '../model-step'
import { usageOf } from '../compaction'
import { withCost } from '../model'

const input = (await Bun.stdin.json()) as {
  scripts: ModelStreamPart[][]
  retries: number
  images: boolean
  usage: Extract<ModelStreamPart, { type: 'usage' }>
}
Date.now = () => 100
let index = 0
const retries: unknown[] = []
const inputs: unknown[] = []
let resets = 0
const provider = {
  async *stream() {
    yield* input.scripts[Math.min(index++, input.scripts.length - 1)]!
  },
} as unknown as ModelProvider<never>
let result: unknown
try {
  result = await requestStep(
    { provider, settings: {}, model: 'fixture', label: 'Fixture' },
    {
      model: 'fixture',
      messages: input.images
        ? [{ role: 'user', content: '', images: [{ mime: 'image/png', data: '' }] }]
        : [],
      tools: [],
      signal: new AbortController().signal,
    },
    (retry) => retries.push(retry),
    0,
    { ...DEFAULT_LIMITS, modelRetries: input.retries },
    undefined,
    {
      input: (call: unknown, name: string, args: string) => inputs.push([call, name, args]),
      reset: () => resets++,
    } as never,
  )
} catch (error) {
  if (!(error instanceof TurnError)) throw error
  result = { error: error.localized, exhausted: error.exhausted }
}
console.info(
  JSON.stringify({
    result,
    retries,
    inputs,
    resets,
    usage: usageOf(input.usage),
    priced: withCost(usageOf(input.usage), {
      provider,
      settings: {},
      model: 'fixture',
      label: 'Fixture',
      price: { currency: 'CNY', input: 2, output: 4, cacheRead: 0.5, cacheWrite: 3 },
    }),
  }),
)
