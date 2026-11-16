import { lines, planOf, runCalls, write } from './common'
import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs'

const storeAt = process.argv.indexOf('--store')
const store = storeAt >= 0 ? process.argv[storeAt + 1] : undefined
const rpcLog = store ? `${store}.jsonl` : undefined

let next = 1
const waiting = new Map<number, (result: any) => void>()
const ask = (method: string, params: unknown) =>
  new Promise<any>((resolve) => {
    const id = next++
    waiting.set(id, resolve)
    write({ id, method, params })
  })
const notify = (method: string, params: unknown) => write({ method, params })

let tools: string[] = []
let system = ''
let environments = 'unset'
let turn = 0
let activeEffort: string | null = null
let total = { inputTokens: 0, cachedInputTokens: 0, outputTokens: 0 }

async function runTurn(id: number, params: any) {
  activeEffort = params.effort
  turn++
  const turnId = `t${turn}`
  write({ id, result: { turn: { id: turnId, status: 'inProgress' } } })
  const text: string = params.input.find((i: any) => i.type === 'text').text
  const images = params.input.filter((i: any) => i.type === 'image').length
  const plan = planOf(text)
  // A command it would run on its own is asked about, and declined.
  const approval = await ask('item/commandExecution/requestApproval', {
    threadId: 'th',
    turnId,
    itemId: 'x',
  })
  const results = await runCalls(plan, async (tool, args) => {
    if (!tools.includes(tool)) return 'unknown tool'
    const result = await ask('item/tool/call', {
      threadId: 'th',
      turnId,
      callId: `c${next}`,
      namespace: null,
      tool,
      arguments: args,
    })
    return result.contentItems.map((c: any) => c.text ?? '').join('')
  })
  total = {
    inputTokens: total.inputTokens + 100,
    cachedInputTokens: total.cachedInputTokens + 80,
    outputTokens: total.outputTokens + 5,
  }
  notify('thread/tokenUsage/updated', {
    threadId: 'th',
    turnId,
    tokenUsage: { total, last: { inputTokens: 100, outputTokens: 5, cachedInputTokens: 80 } },
  })
  if (plan.fail) {
    notify('turn/completed', {
      threadId: 'th',
      turn: { id: turnId, status: 'failed', error: { message: plan.fail } },
    })
    return
  }
  notify('item/completed', {
    threadId: 'th',
    turnId,
    item: { type: 'reasoning', id: 'r', summary: ['thinking'], content: [] },
  })
  const answer = `turn ${turn}: ${results.join(' | ')} len ${text.length} images ${images} system ${system.length} approval ${approval.decision} effort ${activeEffort ?? '-'} environments ${environments} machine context ${process.argv.includes('include_environment_context=false') ? 'off' : 'on'} place note ${system.includes('helper program')}`
  for (const delta of [answer.slice(0, 6), answer.slice(6)]) {
    notify('item/agentMessage/delta', { threadId: 'th', turnId, itemId: 'm', delta })
  }
  notify('item/completed', {
    threadId: 'th',
    turnId,
    item: { type: 'agentMessage', id: 'm', text: answer },
  })
  notify('turn/completed', {
    threadId: 'th',
    turn: { id: turnId, status: 'completed', error: null },
  })
  if (store) writeFileSync(store, JSON.stringify({ tools, system, environments, turn, total }))
}

for await (const message of lines()) {
  if (rpcLog && message.method) appendFileSync(rpcLog, `${JSON.stringify(message)}\n`)
  if (message.method === undefined) {
    waiting.get(message.id)?.(message.result ?? message.error)
    waiting.delete(message.id)
    continue
  }
  if (message.id === undefined) continue
  switch (message.method) {
    case 'initialize':
      write({ id: message.id, result: { userAgent: 'fake' } })
      break
    case 'model/list': {
      const pages = [
        [
          {
            id: 'm1',
            model: 'gpt-big',
            displayName: 'GPT Big',
            isDefault: true,
            supportedReasoningEfforts: [
              { reasoningEffort: 'minimal', description: '' },
              { reasoningEffort: 'low', description: '' },
              { reasoningEffort: 'high', description: '' },
              { reasoningEffort: 'xhigh', description: '' },
            ],
          },
        ],
        [{ id: 'm2', model: 'gpt-small', displayName: 'gpt-small' }],
      ]
      const page = message.params.cursor ? 1 : 0
      write({
        id: message.id,
        result: { data: pages[page], nextCursor: page === 0 ? 'next' : null },
      })
      break
    }
    case 'thread/start':
      tools = message.params.dynamicTools.map((t: any) => t.name)
      system = message.params.baseInstructions
      environments = JSON.stringify(message.params.environments ?? 'unset')
      write({ id: message.id, result: { thread: { id: 'th' } } })
      break
    case 'thread/resume':
      if (!store || !existsSync(store)) {
        write({ id: message.id, error: { code: -32600, message: 'thread missing' } })
        break
      }
      ;({ tools, system, environments, turn, total } = JSON.parse(readFileSync(store, 'utf8')))
      write({ id: message.id, result: { thread: { id: 'th' } } })
      break
    case 'thread/inject_items':
    case 'thread/archive':
      write({ id: message.id, result: {} })
      break
    case 'turn/settings/update':
      if (process.argv.includes('--fallback-metadata')) {
        write({
          id: message.id,
          error: { code: -32600, message: 'the active model has only fallback metadata' },
        })
        break
      }
      if (!process.argv.includes('features.step_model_switching=true')) {
        write({
          id: message.id,
          error: {
            code: -32600,
            message: 'turn settings updates require the step_model_switching feature',
          },
        })
        break
      }
      activeEffort = message.params.effort
      write({ id: message.id, result: {} })
      break
    case 'turn/start':
      void runTurn(message.id, message.params)
      break
    default:
      write({ id: message.id, error: { code: -32601, message: 'no' } })
  }
}
