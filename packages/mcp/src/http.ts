import { readServerSentEvents } from '@ariadne/plugin-sdk'
import { McpConnectionError, parseMessage, type JsonRpcMessage } from './protocol'
import { MAX_MESSAGE_BYTES, type Transport } from './transport'

export interface HttpOptions {
  url: string
  /** Sent with every request, e.g. `Authorization`; the user's, never shown to a model. */
  headers: Record<string, string>
  /** Swaps the fetch, for tests. */
  fetch?: typeof fetch
}

/** Read a body up to `max` bytes, or fail. */
async function readLimited(body: ReadableStream<Uint8Array> | null, max: number): Promise<string> {
  if (!body) return ''
  const decoder = new TextDecoder()
  let size = 0
  let text = ''
  for await (const chunk of body) {
    size += chunk.byteLength
    if (size > max) throw new McpConnectionError('the server answered with too much data')
    text += decoder.decode(chunk, { stream: true })
  }
  return text + decoder.decode()
}

/**
 * A server spoken to over HTTP: every message is a POST, and the answer comes back as JSON
 * or as an event stream. The session the server hands out on `initialize` is sent back
 * with every later request. Redirects are refused, so the headers go only where the user
 * pointed them.
 */
export class HttpTransport implements Transport {
  private session: string | null = null
  private version: string | null = null
  private onMessage: ((message: JsonRpcMessage) => void) | null = null
  private readonly abort = new AbortController()
  private readonly fetch: typeof fetch

  constructor(private readonly options: HttpOptions) {
    this.fetch = options.fetch ?? fetch
  }

  async start(onMessage: (message: JsonRpcMessage) => void): Promise<void> {
    this.onMessage = onMessage
  }

  setProtocolVersion(version: string): void {
    this.version = version
  }

  private headers(): Record<string, string> {
    return {
      ...this.options.headers,
      'content-type': 'application/json',
      accept: 'application/json, text/event-stream',
      ...(this.session ? { 'mcp-session-id': this.session } : {}),
      ...(this.version ? { 'mcp-protocol-version': this.version } : {}),
    }
  }

  async send(message: JsonRpcMessage): Promise<void> {
    if (this.abort.signal.aborted) throw new McpConnectionError('the connection is closed')
    let response: Response
    try {
      response = await this.fetch(this.options.url, {
        method: 'POST',
        headers: this.headers(),
        body: JSON.stringify(message),
        redirect: 'error',
        signal: this.abort.signal,
      })
    } catch (error) {
      throw new McpConnectionError(
        `could not reach ${new URL(this.options.url).host}: ${error instanceof Error ? error.message : String(error)}`,
      )
    }
    const session = response.headers.get('mcp-session-id')
    if (session && !this.session) this.session = session
    if (response.status === 202) {
      await response.body?.cancel()
      return
    }
    if (!response.ok) {
      await response.body?.cancel()
      throw new McpConnectionError(
        response.status === 404 && this.session
          ? 'the server ended the session'
          : `the server answered HTTP ${response.status}`,
      )
    }
    const type = response.headers.get('content-type') ?? ''
    if (type.includes('text/event-stream')) {
      // The stream may stay open after the answer; it is read on its own.
      void this.readStream(response.body)
      return
    }
    const text = await readLimited(response.body, MAX_MESSAGE_BYTES)
    if (!text.trim()) return
    let value: unknown
    try {
      value = JSON.parse(text)
    } catch {
      throw new McpConnectionError('the server answered with something that is not JSON')
    }
    for (const item of Array.isArray(value) ? value : [value]) {
      const parsed = parseMessage(JSON.stringify(item))
      if (parsed) this.onMessage?.(parsed)
    }
  }

  private async readStream(body: ReadableStream<Uint8Array> | null) {
    if (!body) return
    try {
      for await (const event of readServerSentEvents(body)) {
        if (event.data.length > MAX_MESSAGE_BYTES) break
        const message = parseMessage(event.data)
        if (message) this.onMessage?.(message)
      }
    } catch {
      // A broken stream leaves its request unanswered; the request's own timeout ends it.
    }
  }

  async close(): Promise<void> {
    if (this.abort.signal.aborted) return
    this.abort.abort()
    if (!this.session) return
    // Tell the server the session is over; it may not support it, which is fine.
    await this.fetch(this.options.url, {
      method: 'DELETE',
      headers: this.headers(),
      redirect: 'error',
      signal: AbortSignal.timeout(5000),
    })
      .then((response) => response.body?.cancel())
      .catch(() => {})
  }
}
