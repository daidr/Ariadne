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

/** Statuses that mean "try again later": timeout, rate limit, the service failing or overloaded. */
const RETRYABLE_STATUS = new Set([408, 409, 429, 500, 502, 503, 504, 529])

/** How long a `Retry-After` header asks to wait, in milliseconds. */
export function retryAfterMs(response: Response): number | undefined {
  const header = response.headers.get('retry-after')
  if (!header) return undefined
  const seconds = Number(header)
  if (Number.isFinite(seconds)) return Math.max(0, seconds * 1000)
  const at = Date.parse(header)
  return Number.isNaN(at) ? undefined : Math.max(0, at - Date.now())
}

/** The `finish` part for a failed HTTP answer, marked retryable when waiting may help. */
export async function httpErrorPart(response: Response) {
  const after = retryAfterMs(response)
  return {
    type: 'finish' as const,
    reason: 'error' as const,
    error: await describeHttpError(response),
    ...(RETRYABLE_STATUS.has(response.status) ? { retryable: true } : {}),
    ...(after !== undefined ? { retryAfterMs: after } : {}),
  }
}

/**
 * The `finish` part for a request that failed on the wire (refused, reset, cut off), or
 * `null` when it was aborted on purpose.
 */
export function transportErrorPart(error: unknown, signal?: AbortSignal) {
  if (signal?.aborted) return null
  return {
    type: 'finish' as const,
    reason: 'error' as const,
    error: error instanceof Error ? error.message : String(error),
    retryable: true,
  }
}

/** The `finish` part for a stream that ended before the service said it was done. */
export const incompletePart = () => ({
  type: 'finish' as const,
  reason: 'error' as const,
  error: 'the response stream ended before it was complete',
  retryable: true,
})
