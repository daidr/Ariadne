/**
 * For agents that start their tool servers only as local programs: this one passes each
 * line of its stdin to the tool bridge at the address it is given and prints the answers,
 * one per line. Run as `bun mcp-stdio.ts <address>`.
 */
const url = process.argv[2]
if (!url) {
  console.error('usage: mcp-stdio <address>')
  process.exit(2)
}

async function forward(line: string) {
  let parsed: { id?: unknown } | null = null
  try {
    parsed = JSON.parse(line)
  } catch {
    return
  }
  try {
    const response = await fetch(url!, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'application/json, text/event-stream',
      },
      body: line,
    })
    if (response.status === 202 || !response.body) return
    const type = response.headers.get('content-type') ?? ''
    if (!type.includes('text/event-stream')) {
      const text = (await response.text()).trim()
      if (text) process.stdout.write(`${text}\n`)
      return
    }
    // A stream of events: the answer is in a `data:` line; the rest keeps it alive.
    const decoder = new TextDecoder()
    let buffer = ''
    for await (const chunk of response.body) {
      buffer += decoder.decode(chunk, { stream: true })
      let end = buffer.indexOf('\n\n')
      while (end !== -1) {
        const event = buffer.slice(0, end)
        buffer = buffer.slice(end + 2)
        const data = event
          .split('\n')
          .filter((l) => l.startsWith('data:'))
          .map((l) => l.slice(5).trimStart())
          .join('\n')
        if (data) process.stdout.write(`${data}\n`)
        end = buffer.indexOf('\n\n')
      }
    }
  } catch (error) {
    if (parsed && parsed.id !== undefined && parsed.id !== null) {
      const message = error instanceof Error ? error.message : String(error)
      process.stdout.write(
        `${JSON.stringify({ jsonrpc: '2.0', id: parsed.id, error: { code: -32000, message } })}\n`,
      )
    }
  }
}

async function main() {
  const decoder = new TextDecoder()
  let pending = ''
  for await (const chunk of Bun.stdin.stream()) {
    pending += decoder.decode(chunk, { stream: true })
    let newline = pending.indexOf('\n')
    while (newline !== -1) {
      const line = pending.slice(0, newline).trim()
      pending = pending.slice(newline + 1)
      // Calls run side by side: one held open does not hold up a ping.
      if (line) void forward(line)
      newline = pending.indexOf('\n')
    }
  }
}

void main()
