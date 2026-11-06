import { firstFrame, RELAY_VERSION, type Hello, type Welcome } from './client'
import { encode, HELLO, pack, unpack, WELCOME } from './frame'
import type { Link } from './link'
import { Session, type Channel } from './session'

export interface RelayServerOptions {
  /** A channel a client opened. Set its handlers before returning. */
  onChannel: (channel: Channel, session: Session) => void
  /** How long a session with open channels waits for its client to come back; 0 for not at all. */
  keepMs?: number
  /** A session ended, with its channels. */
  onSessionEnd?: (session: Session, reason: string) => void
  handshakeMs?: number
}

/**
 * The side that accepts links. Sessions outlive their links for `keepMs`, so a client that
 * reconnects finds its channels still running; after that their channels end.
 */
export class RelayServer {
  private readonly sessions = new Map<
    string,
    { session: Session; timer?: ReturnType<typeof setTimeout> }
  >()

  constructor(private readonly options: RelayServerOptions) {}

  get sessionCount(): number {
    return this.sessions.size
  }

  /** Greet a new link and attach it to its session, new or resumed. */
  async accept(link: Link): Promise<void> {
    let greeting: Awaited<ReturnType<typeof firstFrame>>
    try {
      greeting = await firstFrame(link, this.options.handshakeMs ?? 15_000)
    } catch {
      return
    }
    const { first, later } = greeting
    const refuse = (error: string) => {
      link.send(encode(WELCOME, [], pack({ error } satisfies Welcome)))
      link.close()
    }
    if (first[0] !== HELLO) return refuse('expected a greeting')
    let hello: Hello
    try {
      hello = unpack<Hello>(first.subarray(1))
    } catch {
      return refuse('the greeting is not readable')
    }
    if (hello.version !== RELAY_VERSION) {
      return refuse(`relay version ${hello.version} is not ${RELAY_VERSION}: update both sides`)
    }
    let entry = hello.session ? this.sessions.get(hello.session) : undefined
    const resumed = entry !== undefined && !entry.session.lost
    if (!entry || !resumed) {
      const session = new Session(crypto.randomUUID(), false)
      session.onChannel = (channel) => this.options.onChannel(channel, session)
      entry = { session }
      this.sessions.set(session.id, entry)
    }
    if (entry.timer) clearTimeout(entry.timer)
    entry.timer = undefined
    const { session } = entry
    // A link the client gave up on may still be open: the new one replaces it.
    session.detach()?.close()
    link.send(
      encode(
        WELCOME,
        [],
        pack({ session: session.id, received: session.receivedCount, resumed } satisfies Welcome),
      ),
    )
    session.attach(link, resumed ? (hello.received ?? 0) : 0)
    const receive = (frame: Uint8Array) => {
      try {
        session.receive(frame)
      } catch (error) {
        this.end(session, error instanceof Error ? error.message : String(error))
      }
    }
    later.forEach(receive)
    link.listen({ frame: receive, close: () => this.dropped(session, link) })
  }

  /** End every session and its channels. */
  close(): void {
    for (const { session } of Array.from(this.sessions.values()))
      this.end(session, 'the server stopped')
  }

  private dropped(session: Session, link: Link): void {
    const entry = this.sessions.get(session.id)
    if (!entry) return
    // Only the current link counts: an old one closing after a resume changes nothing.
    if (session.currentLink !== link) return
    session.detach()
    if (session.channels.size === 0) {
      this.end(session, 'the client left')
      return
    }
    if (this.options.keepMs === 0) {
      this.end(session, 'the client left')
      return
    }
    entry.timer = setTimeout(
      () => this.end(session, 'the client did not come back'),
      this.options.keepMs ?? 120_000,
    )
    entry.timer.unref?.()
  }

  private end(session: Session, reason: string): void {
    const entry = this.sessions.get(session.id)
    if (entry?.timer) clearTimeout(entry.timer)
    if (!this.sessions.delete(session.id)) return
    session.lose(reason)
    this.options.onSessionEnd?.(session, reason)
  }
}
