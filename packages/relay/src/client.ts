import { encode, HELLO, pack, PING, unpack, WELCOME } from './frame'
import type { Link } from './link'
import { Session, type Channel } from './session'

/** Changes whenever the two sides would no longer understand each other. */
export const RELAY_VERSION = 1

export interface Hello {
  version: number
  /** The session to resume, if any. */
  session?: string
  /** Reliable frames received in that session. */
  received?: number
}

export interface Welcome {
  session?: string
  received?: number
  resumed?: boolean
  /** Why the other side refuses: the link closes after it. */
  error?: string
}

/** The other side refused the link: retrying will not help. */
export class RelayRefused extends Error {
  override name = 'RelayRefused'
}

export interface RelayClientOptions {
  /** Opens a fresh link to the other side. Throwing `RelayRefused` stops retries. */
  connect: () => Promise<Link>
  /** How long a lost link is retried while channels are open. */
  resumeMs?: number
  /** How often to ping an idle link; three pings unanswered and it counts as lost. */
  pingMs?: number
  handshakeMs?: number
}

const RETRY_DELAYS = [100, 250, 500, 1000, 2000, 4000]

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

/**
 * The first frame on `link`, or a failure after `ms`. Frames that come right behind it,
 * before the caller listens again, are kept in `later`.
 */
export function firstFrame(
  link: Link,
  ms: number,
): Promise<{ first: Uint8Array; later: Uint8Array[] }> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      link.close()
      reject(new Error('the other side did not answer'))
    }, ms)
    const later: Uint8Array[] = []
    let got = false
    link.listen({
      frame: (frame) => {
        if (got) {
          later.push(frame)
          return
        }
        got = true
        clearTimeout(timer)
        resolve({ first: frame, later })
      },
      close: (reason) => {
        clearTimeout(timer)
        reject(new Error(reason))
      },
    })
  })
}

/**
 * The side that connects. It opens the link when a channel is wanted, and when the link
 * drops while channels are open it connects again and resumes the session, so the channels
 * go on as if nothing happened. If the session cannot be resumed in time, its channels end
 * with an error.
 */
export class RelayClient {
  private session: Session | null = null
  private ready: Promise<Session> | null = null
  private pinger: ReturnType<typeof setInterval> | null = null
  private closed = false

  constructor(private readonly options: RelayClientOptions) {}

  /** Whether a link is up now. */
  get connected(): boolean {
    return this.session?.attached ?? false
  }

  /** Channels open now. */
  get channelCount(): number {
    return this.session?.channels.size ?? 0
  }

  async open(kind: string, params: Record<string, unknown> = {}): Promise<Channel> {
    this.closed = false
    const session = await this.connection()
    return session.open(kind, params)
  }

  /** End the session and its channels. */
  close(reason = 'closed'): void {
    this.closed = true
    this.stopPinging()
    const session = this.session
    this.session = null
    this.ready = null
    if (!session) return
    // Tell the other side, so it ends its channels now instead of waiting for a resume.
    for (const channel of Array.from(session.channels.values())) channel.abort({ error: reason })
    session.lose(reason)
  }

  private connection(): Promise<Session> {
    if (this.session?.attached) return Promise.resolve(this.session)
    this.ready ??= this.handshake(this.session).finally(() => (this.ready = null))
    return this.ready
  }

  /** Open a link and greet; resume `previous` when the other side still has it. */
  private async handshake(previous: Session | null): Promise<Session> {
    const handshakeMs = this.options.handshakeMs ?? 15_000
    const link = await this.options.connect()
    const hello: Hello = { version: RELAY_VERSION }
    if (previous && !previous.lost) {
      hello.session = previous.id
      hello.received = previous.receivedCount
    }
    link.send(encode(HELLO, [], pack(hello)))
    const { first: frame, later } = await firstFrame(link, handshakeMs)
    if (frame[0] !== WELCOME) {
      link.close()
      throw new Error('the other side does not speak the relay protocol')
    }
    const welcome = unpack<Welcome>(frame.subarray(1))
    if (welcome.error) {
      link.close()
      throw new RelayRefused(welcome.error)
    }
    if (this.closed) {
      link.close()
      throw new Error('closed')
    }
    let session = previous
    if (!session || session.lost || !welcome.resumed || welcome.session !== session.id) {
      session?.lose('the connection was lost and the other side no longer had the session')
      session = new Session(String(welcome.session), true)
      this.session = session
    }
    const current = session
    current.attach(link, welcome.received ?? 0)
    const receive = (next: Uint8Array) => {
      try {
        current.receive(next)
      } catch (error) {
        current.lose(error instanceof Error ? error.message : String(error))
      }
    }
    later.forEach(receive)
    link.listen({ frame: receive, close: (reason) => this.dropped(current, reason) })
    this.startPinging()
    return current
  }

  /** The link under `session` went away. */
  private dropped(session: Session, reason: string): void {
    if (this.session !== session) return
    session.detach()
    this.stopPinging()
    if (this.closed || session.lost) return
    if (session.channels.size === 0) {
      // Nothing to resume: the next channel starts a new session.
      this.session = null
      session.lose(reason)
      return
    }
    void this.resume(session, reason)
  }

  private async resume(session: Session, reason: string): Promise<void> {
    const deadline = Date.now() + (this.options.resumeMs ?? 120_000)
    let lastError = reason
    for (let attempt = 0; Date.now() < deadline; attempt++) {
      await sleep(RETRY_DELAYS[Math.min(attempt, RETRY_DELAYS.length - 1)]!)
      if (this.closed || this.session !== session || session.lost) return
      if (session.attached) return
      try {
        this.ready ??= this.handshake(session).finally(() => (this.ready = null))
        await this.ready
        return
      } catch (error) {
        lastError = error instanceof Error ? error.message : String(error)
        if (error instanceof RelayRefused) break
      }
    }
    if (this.session === session) {
      this.session = null
      session.lose(`the connection was lost: ${lastError}`)
    }
  }

  private startPinging(): void {
    const pingMs = this.options.pingMs ?? 15_000
    this.stopPinging()
    this.pinger = setInterval(() => {
      const session = this.session
      if (!session?.attached) return
      if (Date.now() - session.lastHeard > pingMs * 3) {
        // Silent too long: the link is dead even if nobody said so.
        session.detach()?.close()
        return
      }
      session.send(encode(PING, []))
    }, pingMs)
    this.pinger.unref?.()
  }

  private stopPinging(): void {
    if (this.pinger) clearInterval(this.pinger)
    this.pinger = null
  }
}
