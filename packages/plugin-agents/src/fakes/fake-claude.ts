import { readFileSync } from 'node:fs'
import { lines, mcpCall, planOf, runCalls, textOf, write } from './common'

const args = process.argv.slice(2)
const flag = (name: string) => {
  const i = args.indexOf(name)
  return i >= 0 ? args[i + 1] : undefined
}
const mcpFile = flag('--mcp-config')
const url: string | undefined = mcpFile
  ? JSON.parse(readFileSync(mcpFile, 'utf8')).mcpServers.ariadne.url
  : undefined
const system = readFileSync(flag('--system-prompt-file')!, 'utf8')
if (url) {
  await mcpCall(url, 'initialize', { protocolVersion: '2025-06-18', capabilities: {} })
  await mcpCall(url, 'tools/list', {})
}
let turn = 0
let cost = 0
for await (const message of lines()) {
  turn++
  const text: string = message.message.content.find((c: any) => c.type === 'text').text
  const images = message.message.content.filter((c: any) => c.type === 'image').length
  const plan = planOf(text)
  if (plan.slow) await Bun.sleep(60_000)
  const id = `msg_${turn}`
  write({ type: 'system', subtype: 'status', status: 'requesting' })
  write({
    type: 'assistant',
    message: {
      id,
      content: [{ type: 'thinking', thinking: 'hmm' }],
      usage: { input_tokens: 10, cache_read_input_tokens: 90, output_tokens: 1 },
    },
  })
  const results = await runCalls(plan, async (name, a) =>
    textOf(await mcpCall(url!, 'tools/call', { name, arguments: a })),
  )
  if (plan.fail) {
    write({ type: 'result', subtype: 'success', is_error: true, result: plan.fail })
    continue
  }
  const answer = `turn ${turn}: ${results.join(' | ')} len ${text.length} images ${images} system ${system.length}`
  write({
    type: 'assistant',
    message: {
      id,
      content: [{ type: 'text', text: answer }],
      usage: { input_tokens: 10, cache_read_input_tokens: 90, output_tokens: 3 },
    },
  })
  cost += 0.01
  write({
    type: 'result',
    subtype: 'success',
    is_error: false,
    result: answer,
    total_cost_usd: cost,
    usage: { input_tokens: 10, cache_read_input_tokens: 90, output_tokens: 7 },
  })
}
