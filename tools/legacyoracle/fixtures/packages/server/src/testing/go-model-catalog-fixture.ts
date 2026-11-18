import { ModelCatalog } from '../prices'

const input = (await Bun.stdin.json()) as {
  catalog: unknown
  asks: { model: string; hint: string }[]
}
const prices = new ModelCatalog((async () =>
  Response.json(input.catalog)) as unknown as typeof fetch)
const output = []
for (const ask of input.asks)
  output.push({
    lookup: await prices.lookup(ask.model, ask.hint),
    info: Object.fromEntries(await prices.describe([ask.model], ask.hint)),
  })
console.info(JSON.stringify(output))
