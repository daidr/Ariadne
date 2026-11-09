import {
  HttpTransport,
  McpClient,
  McpConnectionError,
  McpSessionEndedError,
  SETUP_TIMEOUT_MS,
  StdioTransport,
  type McpToolInfo,
  type ServerInfo,
  type Transport,
} from '@ariadne/mcp'
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

/** A turn waits this long for a server that is not ready yet; it starts without it after. */
export const TURN_WAIT_MS = 5_000
/** After a failure a server is not tried again for this long, doubling with each failure in a row. */
export const RETRY_AFTER_MS = 60_000
export const MAX_RETRY_AFTER_MS = 15 * 60_000

/** How long a server that failed `count` times in a row is left alone. */
export const retryAfter = (count: number) =>
  Math.min(MAX_RETRY_AFTER_MS, RETRY_AFTER_MS * 2 ** Math.max(0, count - 1))

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
  /** The environment local servers start from: the server's own, without its credentials. */
  env: () => Record<string, string>
  /** Swaps the fetch of HTTP servers, for tests. */
  fetch?: typeof fetch
  /** Swaps `TURN_WAIT_MS` and `SETUP_TIMEOUT_MS`, for tests. */
  turnWaitMs?: number
  setupTimeoutMs?: number
}

/**
 * The connections to the user's external tool servers. A server is started on first use
 * and kept running; it is started again after it stops or its settings change.
 */
export class McpHub {
  private readonly connections = new Map<string, Connection>()
  private readonly failures = new Map<
    string,
    { at: number; count: number; error: string; log: string | null }
  >()
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
   * The server's tools for a turn that is starting, connecting first if needed. It waits
   * only a few seconds: a server that is slower brings no tools to this turn and goes on
   * connecting for the next. `null` when it is not ready, failed or is left alone after
   * failing.
   */
  async tools(config: McpServerConfig, signal?: AbortSignal): Promise<McpToolInfo[] | null> {
    let connection = this.connections.get(config.id)
    if (connection?.key !== wireKey(config)) {
      const failure = this.failures.get(config.id)
      if (failure && Date.now() - failure.at < retryAfter(failure.count)) return null
      void this.connect(config).catch(() => {})
      connection = this.connections.get(config.id)
    }
    if (!connection) return null
    const wait = this.options.turnWaitMs ?? TURN_WAIT_MS
    let timer: ReturnType<typeof setTimeout> | undefined
    const late = new Promise<null>((resolve) => {
      timer = setTimeout(() => resolve(null), wait)
    })
    return abortable(Promise.race([connection.ready, late]), signal)
      .catch(() => null)
      .finally(() => clearTimeout(timer))
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
    return new StdioTransport({
      argv,
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
      // An HTTP session the server forgot is no failure: the next use opens a new one.
      if (error instanceof McpSessionEndedError) return
      const log = transport instanceof StdioTransport ? transport.stderrTail() : ''
      this.failures.set(config.id, {
        at: Date.now(),
        count: (this.failures.get(config.id)?.count ?? 0) + 1,
        error: redact(error instanceof Error ? error.message : String(error), secrets),
        log: log ? redact(log, secrets) : null,
      })
    }
    // Starting up and every page of tools share one deadline.
    const setupMs = this.options.setupTimeoutMs ?? SETUP_TIMEOUT_MS
    const setup = () => {
      const deadline = new AbortController()
      const timer = setTimeout(
        () =>
          deadline.abort(
            new McpConnectionError(`not ready within ${Math.round(setupMs / 1000)} seconds`),
          ),
        setupMs,
      )
      return { signal: deadline.signal, done: () => clearTimeout(timer) }
    }
    const list = () => {
      const { signal, done } = setup()
      return client.listTools(signal).finally(done)
    }
    const connection: Connection = {
      key,
      client,
      transport,
      tools: null,
      ready: Promise.resolve([]),
    }
    connection.ready = (async () => {
      const { signal, done } = setup()
      try {
        await client.connect(signal)
        const tools = await client.listTools(signal)
        connection.tools = tools
        this.lastTools.set(config.id, { key, tools, info: client.info })
        this.failures.delete(config.id)
        return tools
      } finally {
        done()
      }
    })()
    client.onClose((reason) => {
      // Whatever the process left running in its group goes too.
      void client.close().catch(() => {})
      if (this.connections.get(config.id) !== connection) return
      this.connections.delete(config.id)
      fail(
        transport instanceof HttpTransport && transport.sessionEnded
          ? new McpSessionEndedError()
          : new Error(reason),
      )
    })
    // A list that changed is read again on next use.
    client.onToolsChanged(() => {
      if (this.connections.get(config.id) !== connection) return
      connection.ready = list().then((tools) => {
        connection.tools = tools
        this.lastTools.set(config.id, { key, tools, info: client.info })
        return tools
      })
      connection.ready.catch(() => {})
    })
    this.connections.set(config.id, connection)
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
