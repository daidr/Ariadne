import { StreamFramer } from './frame'

/** What a link reports to whoever reads it. */
export interface LinkHandlers {
  frame(frame: Uint8Array): void
  /** The link is gone, by either side or by the network. Called once. */
  close(reason: string): void
}

/**
 * One connection that carries frames in order and whole: a WebSocket, a process's pipes.
 * A session runs over one link at a time and moves to a new one after a reconnect.
 */
export interface Link {
  send(frame: Uint8Array): void
  /** Bytes handed to `send` that have not left yet, when the link can tell. */
  readonly buffered: number
  /** Starts delivery; frames that arrived before are delivered first. */
  listen(handlers: LinkHandlers): void
  close(): void
}

/** Holds frames and the close until someone listens. */
abstract class BaseLink implements Link {
  private handlers: LinkHandlers | null = null
  private early: Uint8Array[] = []
  private closedWith: string | null = null

  abstract send(frame: Uint8Array): void
  abstract get buffered(): number
  abstract close(): void

  listen(handlers: LinkHandlers): void {
    this.handlers = handlers
    const early = this.early
    this.early = []
    for (const frame of early) handlers.frame(frame)
    if (this.closedWith !== null) handlers.close(this.closedWith)
  }

  protected deliver(frame: Uint8Array): void {
    if (this.closedWith !== null) return
    if (this.handlers) this.handlers.frame(frame)
    else this.early.push(frame)
  }

  protected closed(reason: string): void {
    if (this.closedWith !== null) return
    this.closedWith = reason
    this.handlers?.close(reason)
  }

  get isClosed(): boolean {
    return this.closedWith !== null
  }
}

/** A WebSocket as the browser and Bun's client have it, already open. */
export class WebSocketLink extends BaseLink {
  constructor(private readonly ws: WebSocket) {
    super()
    ws.binaryType = 'arraybuffer'
    ws.addEventListener('message', (event) => {
      if (typeof event.data !== 'string') this.deliver(new Uint8Array(event.data as ArrayBuffer))
    })
    ws.addEventListener('close', (event) =>
      this.closed(event.reason || `the connection closed (${event.code})`),
    )
    ws.addEventListener('error', () => this.closed('the connection failed'))
  }

  send(frame: Uint8Array): void {
    if (this.ws.readyState === WebSocket.OPEN) this.ws.send(frame as Uint8Array<ArrayBuffer>)
  }

  get buffered(): number {
    return this.ws.bufferedAmount
  }

  close(): void {
    this.ws.close()
    this.closed('closed')
  }
}

/** The side of a Bun server WebSocket; the server passes on what it receives. */
export class ServerSocketLink extends BaseLink {
  constructor(private readonly ws: Bun.ServerWebSocket<unknown>) {
    super()
  }

  /** From the server's `message` handler. */
  received(message: string | Buffer): void {
    if (typeof message !== 'string') this.deliver(new Uint8Array(message))
  }

  /** From the server's `close` handler. */
  ended(reason = 'the connection closed'): void {
    this.closed(reason)
  }

  send(frame: Uint8Array): void {
    if (this.ws.readyState === 1) this.ws.send(frame)
  }

  get buffered(): number {
    return this.ws.getBufferedAmount()
  }

  close(): void {
    this.ws.close()
    this.closed('closed')
  }
}

/** Something that takes bytes, like a process's input or Bun's standard output. */
export interface ByteSink {
  write(bytes: Uint8Array): unknown
  flush?(): unknown
  end?(): unknown
}

/**
 * Frames over a pair of byte streams, each frame after its length: a child process's pipes,
 * or this process's own standard input and output.
 */
export class StreamLink extends BaseLink {
  constructor(
    input: ReadableStream<Uint8Array>,
    private readonly output: ByteSink,
    /** Says why the input ended, say from the process's error output. */
    why?: () => Promise<string>,
  ) {
    super()
    const framer = new StreamFramer((frame) => this.deliver(frame))
    void (async () => {
      try {
        for await (const chunk of input) framer.push(chunk)
        this.closed((await why?.().catch(() => '')) || 'the stream ended')
      } catch (error) {
        this.closed(error instanceof Error ? error.message : String(error))
      }
    })()
  }

  send(frame: Uint8Array): void {
    if (this.isClosed) return
    try {
      this.output.write(StreamFramer.wrap(frame))
      void Promise.resolve(this.output.flush?.()).catch(() => {})
    } catch (error) {
      this.closed(error instanceof Error ? error.message : String(error))
    }
  }

  get buffered(): number {
    return 0
  }

  close(): void {
    void Promise.resolve(this.output.end?.()).catch(() => {})
    this.closed('closed')
  }
}
