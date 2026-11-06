import {
  ACK,
  CLOSE,
  CONTROL,
  CREDIT,
  DATA,
  encode,
  FrameError,
  isReliable,
  MAX_FRAME,
  OPEN,
  pack,
  PING,
  PONG,
  Reader,
  unpack,
} from './frame'
import type { Link } from './link'

/** Bytes each side may send on a channel before the other side takes them. */
export const WINDOW = 2 * 1024 * 1024
/** The largest piece of data in one frame. */
const CHUNK = 64 * 1024
/** Above this much unsent on the link, writers wait. */
const HIGH_WATER = 4 * 1024 * 1024
/** A session holding more than this unacknowledged gives up instead of growing. */
const MAX_UNACKED = 64 * 1024 * 1024
/** Acknowledge after this many frames or bytes, or after `ACK_DELAY_MS` at the latest. */
const ACK_FRAMES = 64
const ACK_BYTES = 256 * 1024
const ACK_DELAY_MS = 100

export interface ChannelEnd {
  code?: number
  error?: string
}

/**
 * One stream of work inside a session: a command, a file transfer. Both sides may send data
 * on numbered streams (0 for input, say, 1 and 2 for output), control messages, and close.
 */
export class Channel {
  onData: (stream: number, bytes: Uint8Array) => void = () => {}
  onControl: (message: unknown) => void = () => {}
  /** The other side closed the channel, or the session was lost. */
  onClose: (end: ChannelEnd) => void = () => {}

  private credit = WINDOW
  private writing: Promise<unknown> = Promise.resolve()
  private taken = 0
  private ended = false
  private closing = false

  constructor(
    private readonly session: Session,
    readonly id: number,
    readonly kind: string,
    readonly params: Record<string, unknown>,
  ) {}

  get closed(): boolean {
    return this.ended
  }

  /**
   * Send bytes, waiting for the other side's credit and for room on the link. Resolves
   * `false` once the channel is closed: the rest is dropped.
   */
  write(bytes: Uint8Array, stream = 0): Promise<boolean> {
    // One write at a time, so pieces of two writes never interleave.
    if (this.closing) return Promise.resolve(false)
    const next = this.writing.then(() => this.writeNow(bytes, stream))
    this.writing = next.catch(() => false)
    return next
  }

  private async writeNow(bytes: Uint8Array, stream: number): Promise<boolean> {
    let offset = 0
    while (offset < bytes.length) {
      while (!this.ended && (this.credit <= 0 || !this.session.writable)) {
        await this.session.change()
      }
      if (this.ended) return false
      const size = Math.min(bytes.length - offset, this.credit, CHUNK)
      this.credit -= size
      this.session.send(encode(DATA, [this.id, stream], bytes.subarray(offset, offset + size)))
      offset += size
    }
    return !this.ended
  }

  /** Send a control message, after any data already being written. */
  control(message: unknown): void {
    if (this.ended) return
    const frame = encode(CONTROL, [this.id], pack(message))
    this.writing = this.writing.then(() => {
      if (!this.ended) this.session.send(frame)
    })
  }

  /** Close the channel both ways, after any data already being written. */
  close(end: ChannelEnd = {}): void {
    if (this.ended || this.closing) return
    this.closing = true
    this.writing = this.writing.then(() => {
      if (this.ended) return
      this.ended = true
      this.session.send(encode(CLOSE, [this.id], pack(end)))
      this.session.forget(this)
    })
  }

  /** Close now, dropping data not yet sent; `onClose` hears it too. */
  abort(end: ChannelEnd = {}): void {
    if (this.ended) return
    this.ended = true
    this.closing = true
    this.session.send(encode(CLOSE, [this.id], pack(end)))
    this.session.forget(this)
    this.onClose(end)
  }

  /** @internal */
  receiveData(stream: number, bytes: Uint8Array): void {
    if (this.ended) return
    this.onData(stream, bytes)
    this.taken += bytes.length
    if (this.taken >= WINDOW / 4 && !this.ended) {
      this.session.send(encode(CREDIT, [this.id, this.taken]))
      this.taken = 0
    }
  }

  /** @internal */
  receiveCredit(bytes: number): void {
    this.credit += bytes
  }

  /** @internal */
  finish(end: ChannelEnd): void {
    if (this.ended) return
    this.ended = true
    this.onClose(end)
  }
}

export class SessionLost extends Error {
  override name = 'SessionLost'
}

/**
 * Both ends of a relay session look alike: channels, reliable delivery over whichever link
 * is current, and replay after a reconnect. Every reliable frame is counted and kept until
 * the other side acknowledges it; when a new link attaches, each side says how many it got
 * and the other sends the rest again, in order, so nothing is lost or doubled.
 */
export class Session {
  /** A channel the other side opened. */
  onChannel: (channel: Channel) => void = (channel) => channel.close({ error: 'not accepted' })

  readonly channels = new Map<number, Channel>()
  private link: Link | null = null
  private nextId: number
  private sent = 0
  private kept: Uint8Array[] = []
  private keptBytes = 0
  private received = 0
  private acknowledged = 0
  private unackedBytes = 0
  private ackTimer: ReturnType<typeof setTimeout> | null = null
  private waiters: (() => void)[] = []
  private poll: ReturnType<typeof setTimeout> | null = null
  private lostWith: string | null = null
  /** When the current link last delivered anything. */
  lastHeard = Date.now()

  constructor(
    readonly id: string,
    /** The side that opens channels with odd numbers; the other uses even ones. */
    odd: boolean,
  ) {
    this.nextId = odd ? 1 : 2
  }

  get currentLink(): Link | null {
    return this.link
  }

  get attached(): boolean {
    return this.link !== null
  }

  get lost(): boolean {
    return this.lostWith !== null
  }

  /** Whether a writer may send now. */
  get writable(): boolean {
    return this.link !== null && this.link.buffered < HIGH_WATER
  }

  /** How many reliable frames have arrived, for the other side to resume from. */
  get receivedCount(): number {
    return this.received
  }

  open(kind: string, params: Record<string, unknown> = {}): Channel {
    if (this.lostWith !== null) throw new SessionLost(this.lostWith)
    const channel = new Channel(this, this.nextId, kind, params)
    this.nextId += 2
    this.channels.set(channel.id, channel)
    this.send(encode(OPEN, [channel.id], pack({ ...params, kind })))
    return channel
  }

  /**
   * Run over `link` from now on. `peerReceived` is how many of this side's reliable frames
   * the other side has; the ones after it go again.
   */
  attach(link: Link, peerReceived: number): void {
    this.detach()
    if (this.lostWith !== null) {
      link.close()
      return
    }
    this.dropUpTo(peerReceived)
    // The other side learns this count in the handshake: no ACK owed for it.
    this.acknowledged = this.received
    this.unackedBytes = 0
    this.link = link
    this.lastHeard = Date.now()
    for (const frame of this.kept) link.send(frame)
    this.wake()
  }

  /** Stop using the current link; the session waits for the next one. */
  detach(): Link | null {
    const link = this.link
    this.link = null
    if (this.ackTimer) clearTimeout(this.ackTimer)
    this.ackTimer = null
    return link
  }

  /** The session is over: every channel ends with `reason`. */
  lose(reason: string): void {
    if (this.lostWith !== null) return
    this.lostWith = reason
    this.detach()?.close()
    const channels = [...this.channels.values()]
    this.channels.clear()
    this.kept = []
    this.keptBytes = 0
    for (const channel of channels) channel.finish({ error: reason })
    this.wake()
  }

  /** Resolves when credit, room on the link or the session's state may have changed. */
  change(): Promise<void> {
    return new Promise((resolve) => {
      this.waiters.push(resolve)
      // A link has no drain event everywhere (a browser WebSocket has none): look again soon.
      if (this.link && !this.writable) this.poll ??= setTimeout(() => this.wake(), 10)
    })
  }

  /** Send a frame, reliable ones counted and kept for a resume. */
  send(frame: Uint8Array): void {
    if (this.lostWith !== null) return
    if (isReliable(frame[0]!)) {
      this.sent++
      this.kept.push(frame)
      this.keptBytes += frame.length
      if (this.keptBytes > MAX_UNACKED) {
        this.lose('the other side stopped acknowledging')
        return
      }
    }
    this.link?.send(frame)
  }

  /** A frame from the current link. */
  receive(frame: Uint8Array): void {
    if (this.lostWith !== null) return
    this.lastHeard = Date.now()
    const kind = frame[0]
    const reader = new Reader(frame)
    reader.offset = 1
    if (kind === undefined) throw new FrameError('an empty frame')
    if (kind === ACK) {
      this.dropUpTo(reader.varint())
      this.wake()
      return
    }
    if (kind === PING) {
      this.link?.send(encode(PONG, [], reader.rest()))
      return
    }
    if (kind === PONG) return
    if (!isReliable(kind)) throw new FrameError(`a frame of kind ${kind} inside a session`)
    this.received++
    this.unackedBytes += frame.length
    this.scheduleAck()
    const id = reader.varint()
    if (kind === OPEN) {
      const { kind: channelKind, ...params } = unpack<{ kind: string }>(reader.rest())
      if (this.channels.has(id)) throw new FrameError(`channel ${id} is already open`)
      const channel = new Channel(this, id, String(channelKind), params)
      this.channels.set(id, channel)
      this.onChannel(channel)
      return
    }
    const channel = this.channels.get(id)
    if (!channel) return
    if (kind === DATA) {
      const stream = reader.varint()
      const bytes = reader.rest()
      if (bytes.length > MAX_FRAME) throw new FrameError('a data frame is too large')
      channel.receiveData(stream, bytes)
    } else if (kind === CREDIT) {
      channel.receiveCredit(reader.varint())
      this.wake()
    } else if (kind === CONTROL) {
      channel.onControl(unpack(reader.rest()))
    } else if (kind === CLOSE) {
      this.channels.delete(id)
      channel.finish(unpack<ChannelEnd>(reader.rest()) ?? {})
      this.wake()
    }
  }

  /** @internal A channel this side closed. */
  forget(channel: Channel): void {
    this.channels.delete(channel.id)
    this.wake()
  }

  private dropUpTo(peerReceived: number): void {
    if (peerReceived > this.sent) throw new FrameError('acknowledged frames that were never sent')
    const first = this.sent - this.kept.length
    const drop = Math.min(Math.max(0, peerReceived - first), this.kept.length)
    for (let i = 0; i < drop; i++) this.keptBytes -= this.kept[i]!.length
    this.kept = drop ? this.kept.slice(drop) : this.kept
  }

  private scheduleAck(): void {
    if (this.received - this.acknowledged >= ACK_FRAMES || this.unackedBytes >= ACK_BYTES) {
      this.ack()
      return
    }
    this.ackTimer ??= setTimeout(() => this.ack(), ACK_DELAY_MS)
  }

  private ack(): void {
    if (this.ackTimer) clearTimeout(this.ackTimer)
    this.ackTimer = null
    if (!this.link || this.acknowledged === this.received) return
    this.acknowledged = this.received
    this.unackedBytes = 0
    this.link.send(encode(ACK, [this.received]))
  }

  private wake(): void {
    if (this.poll) clearTimeout(this.poll)
    this.poll = null
    const waiters = this.waiters
    this.waiters = []
    for (const resolve of waiters) resolve()
  }
}
