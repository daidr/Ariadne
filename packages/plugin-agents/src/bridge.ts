import type { ImagePart, ToolSpec } from '@ariadne/plugin-sdk'

/** The tool server's name as agents see it; their tool names start with it. */
export const SERVER_NAME = 'ariadne'
/** How often a held tool call sends a keep-alive, so no client gives up on a long one. */
const KEEP_ALIVE_MS = 20_000
const PROTOCOL_VERSION = '2025-06-18'

/** What a tool call gives back to the agent. */
export interface ToolOutput {
  content: string
  isError: boolean
  images: ImagePart[]
}

/** One agent's view of the tools: what it may call and who answers. */
export interface BridgeClient {
  tools: () => ToolSpec[]
  /** A call came in; the result goes back when the promise settles. */
  call: (name: string, args: unknown) => Promise<ToolOutput>
}

interface JsonRpc {
  jsonrpc?: string
  id?: string | number | null
  method?: string
  params?: Record<string, unknown>
}

/**
 * The tools of a conversation, served to an agent program as an MCP server over HTTP on
 * 127.0.0.1. Each agent gets its own path with a random token in it: nothing else on the
 * machine can call a conversation's tools without it. A call is held open until Ariadne
 * has run the tool, as a stream that sends a keep-alive now and then, since running it may
 * wait on the user for a long time.
 */
export class ToolBridge {
  private server: ReturnType<typeof Bun.serve> | null = null
  private readonly clients = new Map<string, BridgeClient>()

  /** The address of `client`'s tools; `remove` takes it away again. */
  add(client: BridgeClient): { url: string; remove: () => void } {
    const server = this.ensure()
    const token = Buffer.from(crypto.getRandomValues(new Uint8Array(24))).toString('base64url')
    this.clients.set(token, client)
    return {
      url: `http://127.0.0.1:${server.port}/mcp/${token}`,
      remove: () => {
        this.clients.delete(token)
      },
    }
  }

  stop(): void {
    this.server?.stop(true)
    this.server = null
    this.clients.clear()
  }

  private ensure() {
    if (this.server) return this.server
    this.server = Bun.serve({
      hostname: '127.0.0.1',
      port: 0,
      // Calls are held until the tool has run, which may take hours.
      idleTimeout: 0,
      fetch: (request) => this.handle(request),
    })
    return this.server
  }

  private async handle(request: Request): Promise<Response> {
    const url = new URL(request.url)
    const match = /^\/mcp\/([A-Za-z0-9_-]+)$/.exec(url.pathname)
    const client = match ? this.clients.get(match[1]!) : undefined
    if (!client) return new Response('not found', { status: 404 })
    if (request.method === 'DELETE') return new Response(null, { status: 200 })
    // No stream of server-sent messages: this server never sends one unasked.
    if (request.method !== 'POST') return new Response(null, { status: 405 })
    let body: unknown
    try {
      body = await request.json()
    } catch {
      return rpcError(null, -32700, 'parse error')
    }
    const messages = (Array.isArray(body) ? body : [body]) as JsonRpc[]
    // Only notifications and answers: nothing to say back.
    const requests = messages.filter((m) => m && m.method && m.id !== undefined && m.id !== null)
    if (!requests.length) return new Response(null, { status: 202 })
    const call = requests.find((m) => m.method === 'tools/call')
    if (call && requests.length === 1) return this.stream(client, call)
    const answers = await Promise.all(requests.map((m) => this.answer(client, m)))
    return Response.json(Array.isArray(body) ? answers : answers[0], {
      headers: { 'Mcp-Session-Id': match![1]!.slice(0, 16) },
    })
  }

  /** A tool call, answered as a stream that keeps the connection alive while it runs. */
  private stream(client: BridgeClient, message: JsonRpc): Response {
    const encoder = new TextEncoder()
    let timer: ReturnType<typeof setInterval> | undefined
    const body = new ReadableStream<Uint8Array>({
      start: async (controller) => {
        timer = setInterval(() => {
          try {
            controller.enqueue(encoder.encode(': keep-alive\n\n'))
          } catch {
            clearInterval(timer)
          }
        }, KEEP_ALIVE_MS)
        const answer = await this.answer(client, message)
        clearInterval(timer)
        try {
          controller.enqueue(encoder.encode(`event: message\ndata: ${JSON.stringify(answer)}\n\n`))
          controller.close()
        } catch {
          // The agent went away meanwhile.
        }
      },
      cancel: () => clearInterval(timer),
    })
    return new Response(body, {
      headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' },
    })
  }

  private async answer(client: BridgeClient, message: JsonRpc): Promise<unknown> {
    const id = message.id ?? null
    switch (message.method) {
      case 'initialize': {
        const asked = message.params?.protocolVersion
        return {
          jsonrpc: '2.0',
          id,
          result: {
            protocolVersion: typeof asked === 'string' ? asked : PROTOCOL_VERSION,
            capabilities: { tools: { listChanged: false } },
            serverInfo: { name: SERVER_NAME, version: '1.0.0' },
          },
        }
      }
      case 'ping':
        return { jsonrpc: '2.0', id, result: {} }
      case 'tools/list':
        return {
          jsonrpc: '2.0',
          id,
          result: {
            tools: client.tools().map((tool) => ({
              name: tool.name,
              description: tool.description,
              inputSchema: { type: 'object', ...tool.parameters },
            })),
          },
        }
      case 'tools/call': {
        const name = message.params?.name
        if (typeof name !== 'string')
          return { jsonrpc: '2.0', id, error: { code: -32602, message: 'name' } }
        let output: ToolOutput
        try {
          output = await client.call(name, message.params?.arguments ?? {})
        } catch (error) {
          output = {
            content: error instanceof Error ? error.message : String(error),
            isError: true,
            images: [],
          }
        }
        return {
          jsonrpc: '2.0',
          id,
          result: {
            content: [
              { type: 'text', text: output.content },
              ...output.images.map((image) => ({
                type: 'image',
                data: image.data,
                mimeType: image.mime,
              })),
            ],
            isError: output.isError,
          },
        }
      }
      default:
        return {
          jsonrpc: '2.0',
          id,
          error: { code: -32601, message: `no method ${message.method}` },
        }
    }
  }
}

function rpcError(id: null, code: number, message: string): Response {
  return Response.json({ jsonrpc: '2.0', id, error: { code, message } }, { status: 400 })
}
