export interface ServerSentEvent {
  event: string | null
  data: string
}

/** Split a `text/event-stream` body into events. Comments and `id`/`retry` fields are skipped. */
export async function* readServerSentEvents(
  body: ReadableStream<Uint8Array>,
): AsyncGenerator<ServerSentEvent> {
  const decoder = new TextDecoder()
  let buffer = ''
  let event: string | null = null
  let data: string[] = []
  const flush = function* () {
    if (data.length) yield { event, data: data.join('\n') }
    event = null
    data = []
  }
  for await (const chunk of body) {
    buffer += decoder.decode(chunk, { stream: true })
    let newline = buffer.search(/\r?\n/)
    while (newline !== -1) {
      const line = buffer.slice(0, newline)
      buffer = buffer.slice(buffer[newline] === '\r' ? newline + 2 : newline + 1)
      if (line === '') yield* flush()
      else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''))
      else if (line.startsWith('event:')) event = line.slice(6).trim()
      newline = buffer.search(/\r?\n/)
    }
  }
  buffer += decoder.decode()
  if (buffer.startsWith('data:')) data.push(buffer.slice(5).replace(/^ /, ''))
  yield* flush()
}

/** Describe a failed HTTP answer: the `error.message` most model services send, else raw text. */
export async function describeHttpError(response: Response): Promise<string> {
  const text = await response.text().catch(() => '')
  try {
    const message = JSON.parse(text)?.error?.message
    if (typeof message === 'string') return `HTTP ${response.status}: ${message}`
  } catch {
    // Not JSON; fall through.
  }
  return `HTTP ${response.status}${text ? `: ${text.slice(0, 300)}` : ''}`
}
