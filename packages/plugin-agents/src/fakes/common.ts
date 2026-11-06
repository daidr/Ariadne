/**
 * What the fake agents in the tests share. Each turn's input is a little script, one
 * instruction per line: `CALL <tool> <json>` calls a tool (calls on lines in a row with
 * `&` in front run side by side), `FAIL <text>` fails the turn, `SLOW` waits a long time;
 * the answer is `turn <n>: <results>` and `len <length of the input>`.
 */
export interface Plan {
  calls: { name: string; args: unknown; parallel: boolean }[]
  fail: string | null
  slow: boolean
}

export function planOf(text: string): Plan {
  const plan: Plan = { calls: [], fail: null, slow: false }
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    const call = /^(&?)CALL (\S+) (.*)$/.exec(line)
    if (call) plan.calls.push({ name: call[2]!, args: JSON.parse(call[3]!), parallel: !!call[1] })
    else if (line.startsWith('FAIL ')) plan.fail = line.slice(5)
    else if (line === 'SLOW') plan.slow = true
  }
  return plan
}

/** Run the calls in order, side by side where marked; `call` runs one. */
export async function runCalls(
  plan: Plan,
  call: (name: string, args: unknown) => Promise<string>,
): Promise<string[]> {
  const results: string[] = []
  let group: Promise<string>[] = []
  for (const item of plan.calls) {
    if (!item.parallel && group.length) {
      results.push(...(await Promise.all(group)))
      group = []
    }
    group.push(call(item.name, item.args))
  }
  results.push(...(await Promise.all(group)))
  return results
}

let mcpId = 1
/** Call a tool on an MCP server over HTTP, as an agent does. */
export async function mcpCall(url: string, method: string, params: unknown): Promise<any> {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream' },
    body: JSON.stringify({ jsonrpc: '2.0', id: mcpId++, method, params }),
  })
  const text = await response.text()
  const data = response.headers.get('content-type')?.includes('event-stream')
    ? text
        .split('\n')
        .filter((l) => l.startsWith('data:'))
        .map((l) => l.slice(5))
        .join('')
    : text
  return JSON.parse(data).result
}

export const textOf = (result: { content: { type: string; text?: string }[] }) =>
  result.content
    .filter((c) => c.type === 'text')
    .map((c) => c.text)
    .join('')

/** Lines of JSON from stdin, one at a time. */
export async function* lines(): AsyncGenerator<any> {
  const decoder = new TextDecoder()
  let pending = ''
  for await (const chunk of Bun.stdin.stream()) {
    pending += decoder.decode(chunk, { stream: true })
    let newline = pending.indexOf('\n')
    while (newline !== -1) {
      const line = pending.slice(0, newline).trim()
      pending = pending.slice(newline + 1)
      if (line) yield JSON.parse(line)
      newline = pending.indexOf('\n')
    }
  }
}

export const write = (value: unknown) => process.stdout.write(`${JSON.stringify(value)}\n`)
