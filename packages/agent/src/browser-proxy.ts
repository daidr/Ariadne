import type { BytePipe } from '@ariadne/plugin-sdk'

/** Opens a connection to a port on the workspace machine's loopback. */
export type Tunnel = (port: number) => Promise<BytePipe>

/** Names that mean the machine itself: to the browser of a remote workspace, that machine. */
export function isLoopback(host: string): boolean {
  const name = host.toLowerCase().replace(/^\[|\]$/g, '')
  return (
    name === 'localhost' ||
    name.endsWith('.localhost') ||
    name === '::1' ||
    /^127(\.\d{1,3}){3}$/.test(name)
  )
}

/** A connection to wherever the browser asked, as a pipe: through the tunnel, or straight. */
async function direct(host: string, port: number): Promise<BytePipe> {
  const pipe: BytePipe = {
    write: async () => false,
    onData: () => {},
    onClose: () => {},
    close: () => {},
  }
  let queue: Uint8Array | null = null
  let closed = false
  const socket = await Bun.connect({
    hostname: host,
    port,
    socket: {
      data: (_socket, data) => pipe.onData(data.slice()),
      drain(open) {
        if (!queue) return
        const sent = open.write(queue)
        queue = sent < queue.length ? queue.subarray(sent) : null
      },
      close: () => {
        closed = true
        pipe.onClose()
      },
      error: (_socket, error) => {
        closed = true
        pipe.onClose(error.message)
      },
    },
  })
  pipe.write = async (bytes) => {
    if (closed) return false
    if (queue) {
      const joined = new Uint8Array(queue.length + bytes.length)
      joined.set(queue)
      joined.set(bytes, queue.length)
      queue = joined
      return true
    }
    const sent = socket.write(bytes)
    if (sent < bytes.length) queue = bytes.slice(sent)
    return true
  }
  pipe.close = () => socket.end()
  return pipe
}

interface Client {
  /** Bytes read before the connection to the target was made. */
  buffer: Uint8Array
  stage: 'greeting' | 'request' | 'connecting' | 'open' | 'closed'
  target: BytePipe | null
  /** Bytes for the browser that its socket could not take yet. */
  out: Uint8Array | null
}

const concat = (a: Uint8Array, b: Uint8Array) => {
  if (!a.length) return b.slice()
  const joined = new Uint8Array(a.length + b.length)
  joined.set(a)
  joined.set(b, a.length)
  return joined
}

/**
 * A SOCKS5 proxy on this machine's loopback for a headless browser whose workspace is on
 * another machine: what the browser asks of `localhost` goes through `tunnel` to that
 * machine's loopback, so its addresses work as they are; everything else goes straight.
 */
export class LoopbackProxy {
  private constructor(private readonly server: Bun.TCPSocketListener<Client>) {}

  get port(): number {
    return this.server.port
  }

  static start(tunnel: Tunnel): LoopbackProxy {
    const send = (socket: Bun.Socket<Client>, bytes: Uint8Array) => {
      const client = socket.data
      if (client.out) {
        client.out = concat(client.out, bytes)
        return
      }
      const sent = socket.write(bytes)
      if (sent < bytes.length) client.out = bytes.slice(sent)
    }
    const fail = (socket: Bun.Socket<Client>, code: number) => {
      // Refused, with an empty IPv4 address.
      socket.write(new Uint8Array([5, code, 0, 1, 0, 0, 0, 0, 0, 0]))
      socket.data.stage = 'closed'
      socket.end()
    }
    const connect = async (socket: Bun.Socket<Client>, host: string, port: number) => {
      const client = socket.data
      client.stage = 'connecting'
      let target: BytePipe
      try {
        target = isLoopback(host) ? await tunnel(port) : await direct(host, port)
      } catch {
        fail(socket, 5)
        return
      }
      // The browser may have gone while the connection was made.
      if ((client.stage as Client['stage']) === 'closed') {
        target.close()
        return
      }
      client.target = target
      target.onData = (bytes) => send(socket, bytes)
      target.onClose = () => {
        if (client.stage === 'closed') return
        client.stage = 'closed'
        socket.end()
      }
      client.stage = 'open'
      socket.write(new Uint8Array([5, 0, 0, 1, 0, 0, 0, 0, 0, 0]))
      if (client.buffer.length) {
        void target.write(client.buffer)
        client.buffer = new Uint8Array()
      }
    }
    const server = Bun.listen<Client>({
      hostname: '127.0.0.1',
      port: 0,
      socket: {
        open(socket) {
          socket.data = { buffer: new Uint8Array(), stage: 'greeting', target: null, out: null }
        },
        data(socket, data) {
          const client = socket.data
          if (client.stage === 'open') {
            void client.target?.write(data.slice())
            return
          }
          client.buffer = concat(client.buffer, data)
          const bytes = client.buffer
          if (client.stage === 'greeting') {
            if (bytes.length < 2 || bytes.length < 2 + bytes[1]!) return
            if (bytes[0] !== 5 || !bytes.subarray(2, 2 + bytes[1]!).includes(0)) {
              socket.write(new Uint8Array([5, 0xff]))
              client.stage = 'closed'
              socket.end()
              return
            }
            client.buffer = bytes.slice(2 + bytes[1]!)
            client.stage = 'request'
            socket.write(new Uint8Array([5, 0]))
            if (!client.buffer.length) return
          }
          if (client.stage !== 'request') return
          const request = client.buffer
          if (request.length < 5) return
          if (request[0] !== 5 || request[1] !== 1) {
            fail(socket, 7)
            return
          }
          let host: string
          let at: number
          if (request[3] === 1) {
            if (request.length < 10) return
            host = [...request.subarray(4, 8)].join('.')
            at = 8
          } else if (request[3] === 3) {
            const length = request[4]!
            if (request.length < 5 + length + 2) return
            host = new TextDecoder().decode(request.subarray(5, 5 + length))
            at = 5 + length
          } else if (request[3] === 4) {
            if (request.length < 22) return
            const words: string[] = []
            for (let i = 4; i < 20; i += 2) {
              words.push(((request[i]! << 8) | request[i + 1]!).toString(16))
            }
            host = words.join(':').replace(/(^|:)0(:0)+(:|$)/, '::')
            at = 20
          } else {
            fail(socket, 8)
            return
          }
          const port = (request[at]! << 8) | request[at + 1]!
          client.buffer = request.slice(at + 2)
          void connect(socket, host, port)
        },
        drain(socket) {
          const client = socket.data
          if (!client.out) return
          const sent = socket.write(client.out)
          client.out = sent < client.out.length ? client.out.slice(sent) : null
        },
        close(socket) {
          const client = socket.data
          client.stage = 'closed'
          client.target?.close()
        },
        error(socket) {
          socket.data.target?.close()
        },
      },
    })
    return new LoopbackProxy(server)
  }

  stop(): void {
    this.server.stop(true)
  }
}
