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

/** The longest wait worth retrying after; a longer `Retry-After` (a spent quota) fails at once. */
export const MAX_RETRY_AFTER_MS = 60_000

/** The `finish` part for a failed HTTP answer, marked retryable when waiting may help. */
export async function httpErrorPart(response: Response) {
  const after = retryAfterMs(response)
  const error = await describeHttpError(response)
  if (after !== undefined && after > MAX_RETRY_AFTER_MS) {
    const minutes = Math.ceil(after / 60_000)
    return {
      type: 'finish' as const,
      reason: 'error' as const,
      error: `${error} (the service asks to wait ${minutes} min before trying again)`,
    }
  }
  return {
    type: 'finish' as const,
    reason: 'error' as const,
    error,
    ...(RETRYABLE_STATUS.has(response.status) ? { retryable: true } : {}),
    ...(after !== undefined ? { retryAfterMs: after } : {}),
  }
}

/**
 * Failures of a connection that was working: reset, timed out, closed by the other end.
 * A wrong address, an unknown host or a bad certificate is a setting to fix, not retried.
 */
const PASSING_NETWORK_ERRORS = new Set([
  'ECONNRESET',
  'ECONNABORTED',
  'EPIPE',
  'ETIMEDOUT',
  'EAI_AGAIN',
  'ConnectionClosed',
  'UND_ERR_SOCKET',
  'TimeoutError',
])

/**
 * The `finish` part for a request that failed on the wire, or `null` when it was aborted
 * on purpose. Only a connection that broke is retryable; `midStream` says the answer had
 * started, so the connection worked and whatever cut it off is passing.
 */
export function transportErrorPart(error: unknown, signal?: AbortSignal, midStream = false) {
  if (signal?.aborted) return null
  const code = (error as { code?: unknown; name?: unknown } | null)?.code
  const name = (error as { name?: unknown } | null)?.name
  const passing =
    midStream ||
    (typeof code === 'string' && PASSING_NETWORK_ERRORS.has(code)) ||
    (typeof name === 'string' && PASSING_NETWORK_ERRORS.has(name))
  return {
    type: 'finish' as const,
    reason: 'error' as const,
    error: error instanceof Error ? error.message : String(error),
    ...(passing ? { retryable: true } : {}),
  }
}

/** The `finish` part for a stream that ended before the service said it was done. */
export const incompletePart = () => ({
  type: 'finish' as const,
  reason: 'error' as const,
  error: 'the response stream ended before it was complete',
  retryable: true,
})
