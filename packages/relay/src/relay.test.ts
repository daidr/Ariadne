import { describe, expect, test } from 'vitest'
import { encode, HELLO, pack, StreamFramer, unpack, WELCOME } from './frame'
import {
  RelayClient,
  RelayServer,
  StreamLink,
  type Channel,
  type ChannelEnd,
  type Link,
} from './index'

function sink(stream: WritableStream<Uint8Array>) {
  const writer = stream.getWriter()
  return {
    write: (bytes: Uint8Array) => void writer.write(bytes).catch(() => {}),
    end: () => writer.close().catch(() => {}),
  }
}

/** Two links joined back to back, as a pipe pair would join them. */
function pair(): [Link, Link] {
  const ab = new TransformStream<Uint8Array, Uint8Array>()
  const ba = new TransformStream<Uint8Array, Uint8Array>()
  return [
    new StreamLink(ba.readable, sink(ab.writable)),
    new StreamLink(ab.readable, sink(ba.writable)),
  ]
}

/** An echo server: every channel sends back what it gets, on stream + 1. */
function setup(options: { keepMs?: number; resumeMs?: number } = {}) {
  const ends: ChannelEnd[] = []
  const server = new RelayServer({
    keepMs: options.keepMs,
    onChannel(channel) {
      channel.onData = (stream, bytes) => void channel.write(bytes.slice(), stream + 1)
      channel.onControl = (message) => {
        if ((message as { op?: string }).op === 'bye') channel.close({ code: 3 })
        else channel.control({ echo: message })
      }
      channel.onClose = (end) => ends.push(end)
    },
  })
  const serverLinks: Link[] = []
  let connects = 0
  let refuse = false
  const client = new RelayClient({
    resumeMs: options.resumeMs ?? 5000,
    async connect() {
      connects++
      if (refuse) throw new Error('no route')
      const [mine, theirs] = pair()
      serverLinks.push(theirs)
      void server.accept(theirs)
      return mine
    },
  })
  return {
    server,
    client,
    ends,
    serverLinks,
    connects: () => connects,
    refuse: (value: boolean) => (refuse = value),
  }
}

function collect(channel: Channel) {
  const chunks: Uint8Array[] = []
  const controls: unknown[] = []
  let length = 0
  const closed = new Promise<ChannelEnd>((resolve) => (channel.onClose = resolve))
  channel.onData = (_stream, bytes) => {
    chunks.push(bytes.slice())
    length += bytes.length
  }
  channel.onControl = (message) => controls.push(message)
  const bytes = () => {
    const out = new Uint8Array(chunks.reduce((n, c) => n + c.length, 0))
    let offset = 0
    for (const c of chunks) {
      out.set(c, offset)
      offset += c.length
    }
    return out
  }
  return { bytes, controls, closed, length: () => length }
}

const until = async (check: () => boolean, ms = 3000) => {
  const deadline = Date.now() + ms
  while (!check()) {
    if (Date.now() > deadline) throw new Error('timed out')
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
}

describe('frames', () => {
  test('a byte stream cut anywhere gives back the same frames', () => {
    const frames = [
      new Uint8Array([1]),
      new Uint8Array(300).fill(7),
      new Uint8Array(70_000).fill(9),
    ]
    const wire = frames.map((f) => StreamFramer.wrap(f))
    const joined = new Uint8Array(wire.reduce((n, w) => n + w.length, 0))
    let offset = 0
    for (const w of wire) {
      joined.set(w, offset)
      offset += w.length
    }
    const got: Uint8Array[] = []
    const framer = new StreamFramer((frame) => got.push(frame))
    for (let i = 0; i < joined.length; i += 333) framer.push(joined.subarray(i, i + 333))
    expect(got).toEqual(frames)
  })

  test('control messages round-trip through msgpack', () => {
    const value = { kind: 'exec', n: 2 ** 40, text: '中文', nested: { list: [1, null, true] } }
    expect(unpack(pack(value))).toEqual(value)
    expect(encode(HELLO, [300], new Uint8Array([5]))).toEqual(new Uint8Array([HELLO, 0xac, 2, 5]))
  })
})

describe('relay', () => {
  test('channels carry data in order, many at once, and close with a reason', async () => {
    const { client, server } = setup()
    const big = new Uint8Array(5 * 1024 * 1024)
    for (let i = 0; i < big.length; i++) big[i] = i % 251
    const a = await client.open('echo')
    const b = await client.open('echo', { label: 'small' })
    const fromA = collect(a)
    const fromB = collect(b)
    const writing = a.write(big)
    b.control({ hi: 1 })
    await until(() => fromB.controls.length === 1)
    expect(fromB.controls).toEqual([{ echo: { hi: 1 } }])
    await writing
    await until(() => fromA.length() === big.length)
    // Compared as buffers: element-wise equality on megabytes is very slow.
    expect(Buffer.from(fromA.bytes()).equals(big)).toBe(true)
    b.control({ op: 'bye' })
    expect(await fromB.closed).toEqual({ code: 3 })
    expect(client.channelCount).toBe(1)
    client.close()
    await until(() => server.sessionCount === 0)
  })

  test('a dropped link is resumed: nothing is lost or doubled', async () => {
    const { client, serverLinks, connects, ends } = setup()
    const channel = await client.open('echo')
    const got = collect(channel)
    const sent: number[] = []
    for (let i = 0; i < 200; i++) {
      sent.push(i)
      await channel.write(new Uint8Array([i]))
      // Cut the link a few times while data is on the way.
      if (i % 60 === 30) serverLinks.at(-1)!.close()
    }
    await until(() => got.length() === sent.length)
    expect([...got.bytes()]).toEqual(sent)
    expect(connects()).toBeGreaterThan(1)
    expect(ends).toEqual([])
    client.close()
  })

  test('when the client cannot come back, both sides end the channel', async () => {
    const { client, serverLinks, ends, refuse, server } = setup({ keepMs: 100, resumeMs: 300 })
    const channel = await client.open('echo')
    const got = collect(channel)
    channel.control({ ready: true })
    await until(() => got.controls.length === 1)
    refuse(true)
    serverLinks.at(-1)!.close()
    expect((await got.closed).error).toContain('connection was lost')
    await until(() => ends.length === 1)
    expect(ends[0]!.error).toContain('did not come back')
    expect(server.sessionCount).toBe(0)
    // A new channel starts a new session.
    refuse(false)
    const again = collect(await client.open('echo'))
    void again
    expect(server.sessionCount).toBe(1)
    client.close()
  })

  test('a session the server forgot is not resumed by mistake', async () => {
    const { client, serverLinks, server } = setup({ keepMs: 20 })
    const channel = await client.open('echo')
    const got = collect(channel)
    server.close()
    serverLinks.at(-1)!.close()
    expect((await got.closed).error).toBeTruthy()
  })

  test('another relay version is refused with a reason', async () => {
    const server = new RelayServer({ onChannel: () => {} })
    const [mine, theirs] = pair()
    void server.accept(theirs)
    const answer = new Promise<Uint8Array>((resolve) =>
      mine.listen({ frame: resolve, close: () => {} }),
    )
    mine.send(encode(HELLO, [], pack({ version: 99 })))
    const frame = await answer
    expect(frame[0]).toBe(WELCOME)
    expect(unpack<{ error: string }>(frame.subarray(1)).error).toContain('relay version 99')
  })
})
