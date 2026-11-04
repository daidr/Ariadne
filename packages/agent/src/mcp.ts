import {
  HttpTransport,
  McpClient,
  McpConnectionError,
  StdioTransport,
  type McpToolInfo,
  type ServerInfo,
  type Transport,
} from '@ariadne/mcp'
import type { CommandSandbox } from '@ariadne/plugin-sdk'
import { homedir } from 'node:os'

/**
 * How a tool of an external server may be used: `read` is called without asking (and the
 * coordinator may use it), `ask` waits for the user's yes to each call, `off` is not
 * offered at all. A tool the user has not marked is `ask`.
 */
export type McpToolMark = 'read' | 'ask' | 'off'

export const MCP_TOOL_MARKS: readonly McpToolMark[] = ['read', 'ask', 'off']

/** One external tool server the user set up. Its environment and headers are secrets. */
export interface McpServerConfig {
  /** Short and stable, part of the tool names the model sees. */
  id: string
  name: string
  transport: 'stdio' | 'http'
  /** For stdio: the program, its arguments and where it runs (the home directory if left out). */
  command?: string
  args?: string[]
  cwd?: string
  env?: Record<string, string>
  /** For HTTP: where it answers, and the headers sent along (e.g. a token). */
  url?: string
  headers?: Record<string, string>
  tools: Record<string, McpToolMark>
}

export type McpServerState = 'idle' | 'connecting' | 'ready' | 'failed'

/** How a server stands, for the settings page. */
export interface McpServerStatus {
  id: string
  state: McpServerState
  /** Why it failed, secrets taken out. */
  error: string | null
  /** The last lines the process wrote to stderr when it failed, secrets taken out. */
  log: string | null
  info: ServerInfo | null
  /** Its tools as last listed; `null` until it was reached once. */
  tools: McpToolInfo[] | null
}

/** After a failure, a server is not tried again for this long, so turns do not wait on it. */
export const RETRY_AFTER_MS = 30_000

/** What identifies a connection: everything but the tool marks, which change nothing on the wire. */
const wireKey = (config: McpServerConfig) => {
  const { tools: _, name: __, ...wire } = config
  return JSON.stringify(wire)
}

/** Values of the environment and headers: never shown to a model, nor in an error. */
export function secretsOf(config: McpServerConfig): string[] {
  return [...Object.values(config.env ?? {}), ...Object.values(config.headers ?? {})].filter(
    (value) => value.length >= 4,
  )
}

/** `text` with every secret of `config` replaced. */
export function redact(text: string, secrets: string[]): string {
  let out = text
  // Longest first, so a secret that holds another is replaced whole.
  for (const secret of secrets.toSorted((a, b) => b.length - a.length)) {
    out = out.split(secret).join('[secret]')
  }
  return out
}

interface Connection {
  key: string
  client: McpClient
  transport: Transport
  ready: Promise<McpToolInfo[]>
  tools: McpToolInfo[] | null
}

export interface McpHubOptions {
  /** The servers as the settings hold them now. */
  servers: () => McpServerConfig[]
  /** Local servers run in it, like thread commands: Ariadne's home stays out of sight. */
  sandbox: CommandSandbox
  /** The environment local servers start from: the server's own, without its credentials. */
  env: () => Record<string, string>
  /** Swaps the fetch of HTTP servers, for tests. */
  fetch?: typeof fetch
}

/**
 * The connections to the user's external tool servers. A server is started on first use
 * and kept running; it is started again after it stops or its settings change.
 */
export class McpHub {
  private readonly connections = new Map<string, Connection>()
  private readonly failures = new Map<string, { at: number; error: string; log: string | null }>()
  private readonly lastTools = new Map<
    string,
    { key: string; tools: McpToolInfo[]; info: ServerInfo | null }
  >()

  constructor(private readonly options: McpHubOptions) {}

  config(id: string): McpServerConfig | undefined {
    return this.options.servers().find((server) => server.id === id)
  }

  status(): McpServerStatus[] {
    return this.options.servers().map((config) => {
      const connection = this.connections.get(config.id)
      const current = connection?.key === wireKey(config) ? connection : undefined
      const failure = this.failures.get(config.id)
      const last = this.lastTools.get(config.id)
      const known = last?.key === wireKey(config) ? last : undefined
      const state: McpServerState = current
        ? current.tools
          ? 'ready'
          : 'connecting'
        : failure
          ? 'failed'
          : 'idle'
      return {
        id: config.id,
        state,
        error: state === 'failed' ? (failure?.error ?? null) : null,
        log: state === 'failed' ? (failure?.log ?? null) : null,
        info: known?.info ?? null,
        tools: known?.tools ?? null,
      }
    })
  }

  /** Close connections whose server was removed or set up differently. */
  async reconfigure(): Promise<void> {
    const servers = new Map(this.options.servers().map((s) => [s.id, s]))
    const stale = [...this.connections.entries()].filter(([id, connection]) => {
      const config = servers.get(id)
      return !config || wireKey(config) !== connection.key
    })
    for (const [id] of stale) {
      this.connections.delete(id)
      this.failures.delete(id)
    }
    for (const id of this.failures.keys()) if (!servers.has(id)) this.failures.delete(id)
    await Promise.all(stale.map(([, connection]) => connection.client.close().catch(() => {})))
  }

  /** Connect again now (even right after a failure) and list the tools, for the settings page. */
  async check(id: string): Promise<McpServerStatus | undefined> {
    const config = this.config(id)
    if (!config) return undefined
    const connection = this.connections.get(id)
    if (connection) {
      this.connections.delete(id)
      await connection.client.close().catch(() => {})
    }
    this.failures.delete(id)
    await this.connect(config).catch(() => {})
    return this.status().find((s) => s.id === id)
  }

  /**
   * The server's tools, connecting first if needed; `null` when it cannot be reached (it
   * failed a moment ago, or fails now). Waiting ends when `signal` aborts.
   */
  async tools(config: McpServerConfig, signal?: AbortSignal): Promise<McpToolInfo[] | null> {
    const connection = this.connections.get(config.id)
    if (connection?.key === wireKey(config)) {
      return abortable(connection.ready, signal).catch(() => null)
    }
    const failure = this.failures.get(config.id)
    if (failure && Date.now() - failure.at < RETRY_AFTER_MS) return null
    return abortable(this.connect(config), signal).catch(() => null)
  }

  /** The live client of a server, connecting first if needed. */
  async client(config: McpServerConfig, signal?: AbortSignal): Promise<McpClient> {
    let connection = this.connections.get(config.id)
    if (connection?.key !== wireKey(config)) {
      await abortable(this.connect(config), signal)
      connection = this.connections.get(config.id)
    } else await abortable(connection.ready, signal)
    if (!connection || connection.client.closed) {
      throw new McpConnectionError('the server stopped')
    }
    return connection.client
  }

  private transportFor(config: McpServerConfig): Transport {
    if (config.transport === 'http') {
      return new HttpTransport({
        url: config.url ?? '',
        headers: { ...config.headers },
        ...(this.options.fetch ? { fetch: this.options.fetch } : {}),
      })
    }
    const argv = [config.command ?? '', ...(config.args ?? [])]
    // Nothing is kept visible for it: a directory under Ariadne's home stays hidden even
    // when it is asked to run there.
    return new StdioTransport({
      argv: this.options.sandbox.wrap(argv, { keep: [] }),
      cwd: config.cwd || homedir(),
      env: { ...this.options.env(), ...config.env },
    })
  }

  private connect(config: McpServerConfig): Promise<McpToolInfo[]> {
    const key = wireKey(config)
    const transport = this.transportFor(config)
    const client = new McpClient(transport)
    const secrets = secretsOf(config)
    const fail = (error: unknown) => {
      const log = transport instanceof StdioTransport ? transport.stderrTail() : ''
      this.failures.set(config.id, {
        at: Date.now(),
        error: redact(error instanceof Error ? error.message : String(error), secrets),
        log: log ? redact(log, secrets) : null,
      })
    }
    const connection: Connection = {
      key,
      client,
      transport,
      tools: null,
      ready: Promise.resolve([]),
    }
    connection.ready = (async () => {
      await client.connect()
      const tools = await client.listTools()
      connection.tools = tools
      this.lastTools.set(config.id, { key, tools, info: client.info })
      return tools
    })()
    client.onClose((reason) => {
      if (this.connections.get(config.id) !== connection) return
      this.connections.delete(config.id)
      fail(new Error(reason))
    })
    // A list that changed is read again on next use.
    client.onToolsChanged(() => {
      if (this.connections.get(config.id) !== connection) return
      connection.ready = client.listTools().then((tools) => {
        connection.tools = tools
        this.lastTools.set(config.id, { key, tools, info: client.info })
        return tools
      })
      connection.ready.catch(() => {})
    })
    this.connections.set(config.id, connection)
    this.failures.delete(config.id)
    connection.ready.catch((error: unknown) => {
      if (this.connections.get(config.id) !== connection) return
      this.connections.delete(config.id)
      fail(error)
      void client.close().catch(() => {})
    })
    return connection.ready
  }

  async dispose(): Promise<void> {
    const all = [...this.connections.values()]
    this.connections.clear()
    await Promise.all(all.map((connection) => connection.client.close().catch(() => {})))
  }
}

/** `work`, or a rejection as soon as `signal` aborts; the work itself goes on. */
function abortable<T>(work: Promise<T>, signal?: AbortSignal): Promise<T> {
  if (!signal) return work
  if (signal.aborted) return Promise.reject(new DOMException('aborted', 'AbortError'))
  return new Promise((resolve, reject) => {
    const abort = () => reject(new DOMException('aborted', 'AbortError'))
    signal.addEventListener('abort', abort, { once: true })
    work.then(
      (value) => {
        signal.removeEventListener('abort', abort)
        resolve(value)
      },
      (error: unknown) => {
        signal.removeEventListener('abort', abort)
        reject(error)
      },
    )
  })
}
