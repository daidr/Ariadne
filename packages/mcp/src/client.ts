import {
  isRequest,
  isResponse,
  McpConnectionError,
  McpRemoteError,
  PROTOCOL_VERSIONS,
  toolResultOf,
  toolsOf,
  type JsonRpcId,
  type JsonRpcMessage,
  type McpToolInfo,
  type McpToolResult,
} from './protocol'
import type { Transport } from './transport'

/** One request while starting up may take this long. */
export const SETUP_TIMEOUT_MS = 60_000
/** A tool call may run this long before it is cancelled. */
export const CALL_TIMEOUT_MS = 300_000
/** At most this many pages of `tools/list`, so a server cannot keep the list going forever. */
const MAX_TOOL_PAGES = 20

const CLIENT_INFO = { name: 'ariadne', version: '0.0.0' }

interface Pending {
  resolve(result: unknown): void
  reject(error: Error): void
}

export interface ServerInfo {
  name: string
  version: string
  protocolVersion: string
}

/**
 * One connection to an MCP server. It asks for tools and calls them; it offers the server
 * nothing in return (no sampling, roots or elicitation), and answers only its pings.
 */
export class McpClient {
  private nextId = 1
  private readonly pending = new Map<JsonRpcId, Pending>()
  private closedBecause: string | null = null
  private readonly closeListeners = new Set<(reason: string) => void>()
  private readonly changeListeners = new Set<() => void>()
  info: ServerInfo | null = null

  constructor(private readonly transport: Transport) {}

  get closed(): boolean {
    return this.closedBecause !== null
  }

  /** Called once when the connection ends, whoever ended it. */
  onClose(listener: (reason: string) => void): void {
    this.closeListeners.add(listener)
  }

  /** Called when the server says its tools changed. */
  onToolsChanged(listener: () => void): void {
    this.changeListeners.add(listener)
  }

  async connect(signal?: AbortSignal): Promise<ServerInfo> {
    await this.transport.start(
      (message) => this.receive(message),
      (reason) => this.end(reason),
    )
    const result = (await this.request(
      'initialize',
      {
        protocolVersion: PROTOCOL_VERSIONS[0],
        capabilities: {},
        clientInfo: CLIENT_INFO,
      },
      { signal, timeoutMs: SETUP_TIMEOUT_MS },
    )) as {
      protocolVersion?: unknown
      serverInfo?: { name?: unknown; version?: unknown }
    } | null
    const version = String(result?.protocolVersion ?? '')
    if (!(PROTOCOL_VERSIONS as readonly string[]).includes(version)) {
      await this.close()
      // The server's own string is not repeated: it would reach the model unchecked.
      throw new McpConnectionError('the server speaks no protocol version this client knows')
    }
    this.transport.setProtocolVersion?.(version)
    await this.notify('notifications/initialized')
    this.info = {
      name: typeof result?.serverInfo?.name === 'string' ? result.serverInfo.name : '',
      version: typeof result?.serverInfo?.version === 'string' ? result.serverInfo.version : '',
      protocolVersion: version,
    }
    return this.info
  }

  async listTools(signal?: AbortSignal): Promise<McpToolInfo[]> {
    const tools: McpToolInfo[] = []
    let cursor: string | undefined
    for (let page = 0; page < MAX_TOOL_PAGES; page++) {
      const result = toolsOf(
        await this.request('tools/list', cursor ? { cursor } : {}, {
          signal,
          timeoutMs: SETUP_TIMEOUT_MS,
        }),
      )
      tools.push(...result.tools)
      cursor = result.nextCursor
      if (!cursor) break
    }
    return tools
  }

  async callTool(
    name: string,
    args: Record<string, unknown>,
    options: { signal?: AbortSignal; timeoutMs?: number } = {},
  ): Promise<McpToolResult> {
    return toolResultOf(
      await this.request(
        'tools/call',
        { name, arguments: args },
        {
          signal: options.signal,
          timeoutMs: options.timeoutMs ?? CALL_TIMEOUT_MS,
        },
      ),
    )
  }

  /**
   * Send a request and wait for its answer. Given up on when `signal` aborts or the time
   * runs out; the server is then told to stop working on it.
   */
  request(
    method: string,
    params: Record<string, unknown>,
    options: { signal?: AbortSignal; timeoutMs: number },
  ): Promise<unknown> {
    if (this.closedBecause !== null) {
      return Promise.reject(new McpConnectionError(this.closedBecause))
    }
    const id = this.nextId++
    return new Promise((resolve, reject) => {
      const timer = setTimeout(
        () =>
          giveUp(
            new McpConnectionError(
              `no answer to ${method} within ${options.timeoutMs / 1000} seconds`,
            ),
            'timeout',
          ),
        options.timeoutMs,
      )
      // A signal aborted with a connection error (a deadline) fails with that error.
      const abort = () =>
        options.signal?.reason instanceof McpConnectionError
          ? giveUp(options.signal.reason, 'timeout')
          : giveUp(new DOMException('aborted', 'AbortError'), 'cancelled by the user')
      const finish = () => {
        clearTimeout(timer)
        options.signal?.removeEventListener('abort', abort)
        this.pending.delete(id)
      }
      const giveUp = (error: Error, reason: string) => {
        if (!this.pending.has(id)) return
        finish()
        void this.notify('notifications/cancelled', {
          requestId: id,
          reason,
        }).catch(() => {})
        reject(error)
      }
      this.pending.set(id, {
        resolve: (result) => {
          finish()
          resolve(result)
        },
        reject: (error) => {
          finish()
          reject(error)
        },
      })
      if (options.signal?.aborted) return abort()
      options.signal?.addEventListener('abort', abort, { once: true })
      this.transport.send({ jsonrpc: '2.0', id, method, params }).catch((error: unknown) => {
        const failure = error instanceof Error ? error : new McpConnectionError(String(error))
        this.pending.get(id)?.reject(failure)
        // A transport that cannot send any more is a connection that is over.
        if (failure instanceof McpConnectionError) this.end(failure.message)
      })
    })
  }

  private notify(method: string, params?: Record<string, unknown>): Promise<void> {
    if (this.closedBecause !== null) return Promise.resolve()
    return this.transport.send({
      jsonrpc: '2.0',
      method,
      ...(params ? { params } : {}),
    })
  }

  private receive(message: JsonRpcMessage) {
    if (isResponse(message)) {
      const pending = message.id === null ? undefined : this.pending.get(message.id)
      if (!pending) return
      if (message.error) {
        pending.reject(
          new McpRemoteError(
            Number(message.error.code) || 0,
            String(message.error.message ?? 'error'),
          ),
        )
      } else pending.resolve(message.result)
      return
    }
    if (isRequest(message)) {
      const reply =
        message.method === 'ping'
          ? { jsonrpc: '2.0' as const, id: message.id, result: {} }
          : {
              jsonrpc: '2.0' as const,
              id: message.id,
              error: {
                code: -32601,
                message: `${message.method} is not supported`,
              },
            }
      void this.transport.send(reply).catch(() => {})
      return
    }
    if (message.method === 'notifications/tools/list_changed') {
      for (const listener of this.changeListeners) listener()
    }
  }

  private end(reason: string) {
    if (this.closedBecause !== null) return
    this.closedBecause = reason
    // Each one leaves the map as it is rejected, which iterating a map allows.
    for (const pending of this.pending.values()) {
      pending.reject(new McpConnectionError(reason))
    }
    this.pending.clear()
    for (const listener of this.closeListeners) listener(reason)
  }

  async close(): Promise<void> {
    this.end('the connection was closed')
    await this.transport.close()
  }
}
