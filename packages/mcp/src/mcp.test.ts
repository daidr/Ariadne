import { afterEach, describe, expect, it } from 'vitest'
import { join } from 'node:path'
import { McpClient } from './client'
import { HttpTransport } from './http'
import { McpConnectionError, McpRemoteError, type JsonRpcMessage } from './protocol'
import { StdioTransport } from './stdio'
import { createServer } from './test-server'

const SERVER = join(import.meta.dirname, 'test-server.ts')
const open: McpClient[] = []

afterEach(async () => {
  await Promise.all(open.splice(0).map((client) => client.close()))
})

function stdio(env: Record<string, string> = {}) {
  const transport = new StdioTransport({
    argv: [process.execPath, SERVER],
    cwd: import.meta.dirname,
    env: { PATH: process.env.PATH ?? '', ...env },
  })
  const client = new McpClient(transport)
  open.push(client)
  return { client, transport }
}

describe('stdio', () => {
  it('starts, lists every page of tools and calls them', async () => {
    const { client } = stdio({ SECRET_THING: 'shh' })
    const info = await client.connect()
    expect(info).toEqual({ name: 'test-server', version: '1.0.0', protocolVersion: '2025-06-18' })
    const tools = await client.listTools()
    expect(tools.map((t) => t.name)).toEqual(['echo', 'env', 'picture', 'fail', 'slow', 'grow'])
    expect(tools[0]?.annotations?.readOnlyHint).toBe(true)
    expect(await client.callTool('echo', { text: 'hi' })).toEqual({
      content: [{ type: 'text', text: 'hi' }],
    })
    // Only the environment it was given.
    expect((await client.callTool('env', { name: 'SECRET_THING' })).content).toEqual([
      { type: 'text', text: 'shh' },
    ])
    expect((await client.callTool('env', { name: 'HOME' })).content).toEqual([
      { type: 'text', text: '(unset)' },
    ])
    expect((await client.callTool('fail', {})).isError).toBe(true)
    await expect(client.callTool('nope', {})).rejects.toBeInstanceOf(McpRemoteError)
  })

  it('cancels a call when its signal aborts, and hears that tools changed', async () => {
    const { client } = stdio()
    await client.connect()
    const controller = new AbortController()
    const call = client.callTool('slow', {}, { signal: controller.signal })
    controller.abort()
    await expect(call).rejects.toThrow('aborted')
    let changed = 0
    client.onToolsChanged(() => changed++)
    await client.callTool('grow', {})
    expect(changed).toBe(1)
    expect((await client.listTools()).map((t) => t.name)).toContain('extra.tool')
  })

  it('gives up on a call that takes too long', async () => {
    const { client } = stdio()
    await client.connect()
    await expect(client.callTool('slow', {}, { timeoutMs: 100 })).rejects.toBeInstanceOf(
      McpConnectionError,
    )
    // The connection is still good.
    expect((await client.callTool('echo', { text: 'still' })).content).toHaveLength(1)
  })

  it('refuses a protocol version it does not speak', async () => {
    const { client } = stdio({ TEST_MCP_VERSION: '1999-01-01' })
    await expect(client.connect()).rejects.toThrow('1999-01-01')
  })

  it('reports a process that exits, with what it wrote to stderr', async () => {
    const transport = new StdioTransport({
      argv: [process.execPath, '-e', 'console.error("no token given"); process.exit(3)'],
      cwd: import.meta.dirname,
      env: { PATH: process.env.PATH ?? '' },
    })
    const client = new McpClient(transport)
    open.push(client)
    await expect(client.connect()).rejects.toThrow('exited with code 3')
    expect(transport.stderrTail()).toContain('no token given')
  })

  it('fails to start a command that does not exist', async () => {
    const transport = new StdioTransport({
      argv: ['/definitely/not/here'],
      cwd: import.meta.dirname,
      env: {},
    })
    await expect(new McpClient(transport).connect()).rejects.toBeInstanceOf(McpConnectionError)
  })
})

describe('http', () => {
  const servers: { stop(force?: boolean): void }[] = []
  afterEach(() => {
    for (const server of servers.splice(0)) server.stop(true)
  })

  function serve() {
    const seen: { headers: Headers; body: JsonRpcMessage | null; method: string }[] = []
    const server = Bun.serve({
      port: 0,
      async fetch(request) {
        const body = request.method === 'POST' ? ((await request.json()) as JsonRpcMessage) : null
        seen.push({ headers: request.headers, body, method: request.method })
        if (request.method === 'DELETE') return new Response(null, { status: 204 })
        if (request.headers.get('authorization') !== 'Bearer key') {
          return new Response('no', { status: 401 })
        }
        const out: JsonRpcMessage[] = []
        createServer((message) => out.push(message))(body as JsonRpcMessage)
        if (!body || !('id' in body)) return new Response(null, { status: 202 })
        const headers: Record<string, string> =
          'method' in body && body.method === 'initialize' ? { 'mcp-session-id': 'session-1' } : {}
        // Calls answer as an event stream, the rest as JSON.
        if ('method' in body && body.method === 'tools/call') {
          const text = out.map((m) => `event: message\ndata: ${JSON.stringify(m)}\n\n`).join('')
          return new Response(text, {
            headers: { ...headers, 'content-type': 'text/event-stream' },
          })
        }
        return Response.json(out[0], { headers })
      },
    })
    servers.push(server)
    return { url: `http://127.0.0.1:${server.port}/mcp`, seen }
  }

  it('speaks JSON and event streams, keeping the session and the headers', async () => {
    const { url, seen } = serve()
    const client = new McpClient(
      new HttpTransport({ url, headers: { authorization: 'Bearer key' } }),
    )
    open.push(client)
    await client.connect()
    expect((await client.listTools()).length).toBe(6)
    const result = await client.callTool('picture', {})
    expect(result.content.map((c) => c.type)).toEqual(['text', 'image'])
    await client.close()
    const later = seen.filter(
      (s) => !(s.body && 'method' in s.body && s.body.method === 'initialize'),
    )
    expect(later.every((s) => s.headers.get('mcp-session-id') === 'session-1')).toBe(true)
    expect(later.every((s) => s.headers.get('mcp-protocol-version') === '2025-06-18')).toBe(true)
    expect(seen.at(-1)?.method).toBe('DELETE')
  })

  it('reports a refused request without the server text', async () => {
    const { url } = serve()
    const client = new McpClient(new HttpTransport({ url, headers: {} }))
    open.push(client)
    await expect(client.connect()).rejects.toThrow('HTTP 401')
  })

  it('does not follow redirects', async () => {
    const target = serve()
    const server = Bun.serve({
      port: 0,
      fetch: () => Response.redirect(target.url, 307),
    })
    servers.push(server)
    const client = new McpClient(
      new HttpTransport({
        url: `http://127.0.0.1:${server.port}/mcp`,
        headers: { authorization: 'Bearer key' },
      }),
    )
    open.push(client)
    await expect(client.connect()).rejects.toBeInstanceOf(McpConnectionError)
    expect(target.seen).toHaveLength(0)
  })
})
