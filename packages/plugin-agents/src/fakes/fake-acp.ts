import { lines, mcpCall, planOf, runCalls, textOf, write } from './common'

let next = 1
const waiting = new Map<number, (result: any) => void>()
const ask = (method: string, params: unknown) =>
  new Promise<any>((resolve) => {
    const id = next++
    waiting.set(id, resolve)
    write({ jsonrpc: '2.0', id, method, params })
  })
const update = (change: unknown) =>
  write({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: 's1', update: change } })

let url = ''
let turn = 0
let model = 'default'
const permission = (title: string) =>
  ask('session/request_permission', {
    sessionId: 's1',
    toolCall: { toolCallId: 'x', title },
    options: [
      { optionId: 'yes', name: 'Allow', kind: 'allow_once' },
      { optionId: 'no', name: 'Reject', kind: 'reject_once' },
    ],
  }).then((r) => r.outcome.optionId)

async function prompt(id: number, params: any) {
  turn++
  const text: string = params.prompt.find((p: any) => p.type === 'text').text
  const images = params.prompt.filter((p: any) => p.type === 'image').length
  const plan = planOf(text)
  if (plan.slow) await Bun.sleep(60_000)
  const own = await permission('Run shell command: ls')
  update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'thin' } })
  update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'king' } })
  const results = await runCalls(plan, async (name, args) => {
    const allowed = await permission(`${name} (ariadne MCP Server)`)
    if (allowed !== 'yes') return 'denied'
    update({ sessionUpdate: 'tool_call', toolCallId: name, title: name, status: 'pending' })
    return textOf(await mcpCall(url, 'tools/call', { name, arguments: args }))
  })
  if (plan.fail) {
    write({ jsonrpc: '2.0', id, error: { code: -32603, message: plan.fail } })
    return
  }
  const answer = `turn ${turn}: ${results.join(' | ')} len ${text.length} images ${images} own ${own} model ${model}`
  update({
    sessionUpdate: 'agent_message_chunk',
    content: { type: 'text', text: answer.slice(0, 5) },
  })
  update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: answer.slice(5) } })
  write({
    jsonrpc: '2.0',
    id,
    result: {
      stopReason: 'end_turn',
      usage: { inputTokens: 50, outputTokens: 4, cachedReadTokens: 40 },
    },
  })
}

for await (const message of lines()) {
  if (message.method === undefined) {
    waiting.get(message.id)?.(message.result)
    waiting.delete(message.id)
    continue
  }
  if (message.id === undefined) continue
  switch (message.method) {
    case 'initialize':
      write({
        jsonrpc: '2.0',
        id: message.id,
        result: {
          protocolVersion: 1,
          agentCapabilities: {
            promptCapabilities: { image: true },
            mcpCapabilities: { http: process.env.FAKE_ACP_HTTP !== '0' },
          },
        },
      })
      break
    case 'session/new': {
      const server = message.params.mcpServers[0]
      if (server?.url) url = server.url
      else if (server) url = `stdio:${server.args[1]}`
      write({
        jsonrpc: '2.0',
        id: message.id,
        result: {
          sessionId: 's1',
          models: {
            currentModelId: 'default',
            availableModels: [{ modelId: 'default' }, { modelId: 'fast' }],
          },
        },
      })
      break
    }
    case 'session/set_model':
      model = message.params.modelId
      write({ jsonrpc: '2.0', id: message.id, result: {} })
      break
    case 'session/prompt':
      void prompt(message.id, message.params)
      break
    default:
      write({ jsonrpc: '2.0', id: message.id, error: { code: -32601, message: 'no' } })
  }
}
