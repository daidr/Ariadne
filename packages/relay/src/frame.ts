import { Packr } from 'msgpackr'

/**
 * Frames, the unit every link carries. A frame is one byte of kind, then fields that depend
 * on the kind, then its payload:
 *
 * - `HELLO`, `WELCOME`: msgpack. The opening handshake of each link, see `Session`.
 * - `ACK`: varint count. How many reliable frames the sender has received in this session.
 * - `PING`, `PONG`: the payload comes back as it went.
 * - `OPEN`: varint channel, msgpack `{ kind, ...params }`.
 * - `DATA`: varint channel, varint stream, the bytes as they are.
 * - `CREDIT`: varint channel, varint bytes the receiver takes more of.
 * - `CONTROL`: varint channel, msgpack.
 * - `CLOSE`: varint channel, msgpack `{ code?, error? }`; the channel is over both ways.
 *
 * Kinds from `OPEN` on are reliable: they are counted, kept until acknowledged and sent again
 * after a reconnect. The others belong to one link only.
 */
export const HELLO = 1
export const WELCOME = 2
export const ACK = 3
export const PING = 4
export const PONG = 5
export const OPEN = 16
export const DATA = 17
export const CREDIT = 18
export const CONTROL = 19
export const CLOSE = 20

export const isReliable = (kind: number) => kind >= OPEN

/** No frame is larger; writers split data below it. */
export const MAX_FRAME = 1024 * 1024

// Plain maps, no records: records keep state on the connection, which a resume would lose.
const packr = new Packr({ useRecords: false, moreTypes: false })

export function pack(value: unknown): Uint8Array {
  return packr.pack(value)
}

export function unpack<T>(bytes: Uint8Array): T {
  return packr.unpack(bytes) as T
}

export function varintLength(value: number): number {
  let length = 1
  while (value >= 0x80) {
    value = Math.floor(value / 0x80)
    length++
  }
  return length
}

/** Write `value` (a safe non-negative integer) at `offset`; returns the offset after it. */
export function writeVarint(out: Uint8Array, offset: number, value: number): number {
  while (value >= 0x80) {
    out[offset++] = (value % 0x80) | 0x80
    value = Math.floor(value / 0x80)
  }
  out[offset++] = value
  return offset
}

export class FrameError extends Error {
  override name = 'FrameError'
}

/** Reads fields from the front of a frame. */
export class Reader {
  offset = 0
  constructor(readonly bytes: Uint8Array) {}

  varint(): number {
    let value = 0
    let scale = 1
    for (let i = 0; i < 8; i++) {
      const byte = this.bytes[this.offset++]
      if (byte === undefined) throw new FrameError('the frame ends inside a number')
      value += (byte & 0x7f) * scale
      if (byte < 0x80) return value
      scale *= 0x80
    }
    throw new FrameError('a number in the frame is too long')
  }

  rest(): Uint8Array {
    return this.bytes.subarray(this.offset)
  }
}

/** A frame of `kind` with the given numbers, then `payload`. */
export function encode(kind: number, numbers: number[], payload?: Uint8Array): Uint8Array {
  let length = 1 + (payload?.length ?? 0)
  for (const n of numbers) length += varintLength(n)
  const out = new Uint8Array(length)
  out[0] = kind
  let offset = 1
  for (const n of numbers) offset = writeVarint(out, offset, n)
  if (payload) out.set(payload, offset)
  return out
}

/**
 * Frames on a plain byte stream (a pipe, a TCP socket), each after its length as a varint.
 * Feed it bytes as they arrive; it hands on whole frames.
 */
export class StreamFramer {
  private buffer = new Uint8Array(0)

  constructor(private readonly onFrame: (frame: Uint8Array) => void) {}

  static wrap(frame: Uint8Array): Uint8Array {
    const out = new Uint8Array(varintLength(frame.length) + frame.length)
    out.set(frame, writeVarint(out, 0, frame.length))
    return out
  }

  push(chunk: Uint8Array): void {
    let bytes = chunk
    if (this.buffer.length) {
      bytes = new Uint8Array(this.buffer.length + chunk.length)
      bytes.set(this.buffer)
      bytes.set(chunk, this.buffer.length)
    }
    let offset = 0
    while (offset < bytes.length) {
      const reader = new Reader(bytes)
      reader.offset = offset
      let length: number
      try {
        length = reader.varint()
      } catch (error) {
        // An unfinished length waits for more; a malformed one is fatal.
        if (bytes.length - offset < 8) break
        throw error
      }
      if (length > MAX_FRAME + 64) throw new FrameError(`a frame of ${length} bytes is too large`)
      if (reader.offset + length > bytes.length) break
      this.onFrame(bytes.slice(reader.offset, reader.offset + length))
      offset = reader.offset + length
    }
    this.buffer = offset === bytes.length ? new Uint8Array(0) : bytes.slice(offset)
  }
}
