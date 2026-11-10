import type { BytePipe, ExecResult } from '@ariadne/plugin-sdk'
import { RelayClient, type Channel, type RelayClientOptions } from '@ariadne/relay'
import {
  CONNECTION_FAILED,
  DEFAULT_TIMEOUT_MS,
  OutputText,
  payload,
  type RemoteShell,
  type ShellRun,
} from './shell'

/** The channel kind and streams a daemon runs commands with; see the daemon's protocol. */
const EXEC = 'exec'
const TCP = 'tcp'
const STDIN = 0
const STDOUT = 1

const encoder = new TextEncoder()

/**
 * Output kept as text, each stream decoded on its own as pieces arrive, bounded when the
 * caller clips it (a background command may print for hours), and handed on as it comes
 * when the caller asked to see it.
 */
function outputOf(options: ShellRun) {
  const streams = {
    stdout: { decoder: new TextDecoder(), kept: new OutputText(options.clip ?? false) },
    stderr: { decoder: new TextDecoder(), kept: new OutputText(options.clip ?? false) },
  }
  return {
    add(data: Uint8Array, stream: 'stdout' | 'stderr') {
      const piece = streams[stream].decoder.decode(data, { stream: true })
      streams[stream].kept.add(piece)
      if (piece) options.onOutput?.(piece, stream)
    },
    text(stream: 'stdout' | 'stderr') {
      const rest = streams[stream].decoder.decode()
      streams[stream].kept.add(rest)
      if (rest) options.onOutput?.(rest, stream)
      return streams[stream].kept.text()
    },
  }
}

/**
 * A machine whose daemon runs each call as a channel of one relay session: over the
 * network, or through one long-lived ssh process. The session opens with the first call,
 * rides out a dropped connection, and closes once no call has used it for `idleMs`.
 */
export class RelayShell implements RemoteShell {
  private readonly client: RelayClient
  private idle: ReturnType<typeof setTimeout> | null = null

  constructor(
    readonly via: string,
    readonly host: string,
    connect: RelayClientOptions['connect'],
    private readonly idleMs = 120_000,
    options: Omit<RelayClientOptions, 'connect'> = {},
  ) {
    this.client = new RelayClient({ ...options, connect })
  }

  /** Whether a connection is open now. */
  get connected(): boolean {
    return this.client.connected
  }

  async run(script: string, args: string[], options: ShellRun = {}): Promise<ExecResult> {
    if (options.signal?.aborted) return { exitCode: 130, stdout: '', stderr: 'cancelled' }
    if (this.idle) clearTimeout(this.idle)
    this.idle = null
    let channel: Channel
    try {
      channel = await this.client.open(EXEC)
    } catch (error) {
      this.scheduleIdle()
      const reason = error instanceof Error ? error.message : String(error)
      return { exitCode: CONNECTION_FAILED, stdout: '', stderr: `${this.via}: ${reason}` }
    }
    const output = outputOf(options)
    const finished = new Promise<{ code?: number; error?: string }>((resolve) => {
      channel.onClose = resolve
    })
    channel.onData = (stream, bytes) => output.add(bytes, stream === STDOUT ? 'stdout' : 'stderr')
    void channel.write(encoder.encode(payload(script, args) + (options.input ?? '')), STDIN)
    if (!options.hold) channel.control({ op: 'end' })
    let ended = false
    const end = () => {
      if (ended) return
      ended = true
      // Closing the input ends a thread command's process group; the kill follows in case
      // something holds on.
      channel.control({ op: 'end' })
      setTimeout(() => channel.control({ op: 'kill' }), 2000).unref?.()
    }
    const timer = setTimeout(end, options.timeoutMs ?? DEFAULT_TIMEOUT_MS)
    options.signal?.addEventListener('abort', end, { once: true })
    try {
      const result = await finished
      const stdout = output.text('stdout')
      let stderr = output.text('stderr')
      if (result.code === undefined) {
        stderr += `${this.via}: ${result.error ?? 'the command ended without an exit code'}`
        return { exitCode: CONNECTION_FAILED, stdout, stderr }
      }
      return { exitCode: result.code, stdout, stderr }
    } finally {
      clearTimeout(timer)
      options.signal?.removeEventListener('abort', end)
      this.scheduleIdle()
    }
  }

  /** A `tcp` channel: the daemon connects to `port` on its loopback and passes bytes. */
  async connect(port: number): Promise<BytePipe> {
    if (this.idle) clearTimeout(this.idle)
    this.idle = null
    let channel: Channel
    try {
      channel = await this.client.open(TCP, { port })
    } catch (error) {
      this.scheduleIdle()
      throw error
    }
    const pipe: BytePipe = {
      write: (bytes) => channel.write(bytes, 0),
      onData: () => {},
      onClose: () => {},
      close: () => {
        if (!channel.closed) channel.close()
        this.scheduleIdle()
      },
    }
    channel.onData = (_stream, bytes) => pipe.onData(bytes)
    channel.onClose = (end) => {
      this.scheduleIdle()
      pipe.onClose(end.error)
    }
    return pipe
  }

  async dispose(): Promise<void> {
    if (this.idle) clearTimeout(this.idle)
    this.idle = null
    this.client.close('the connection was closed')
  }

  private scheduleIdle(): void {
    if (this.client.channelCount) return
    if (this.idle) clearTimeout(this.idle)
    this.idle = setTimeout(() => {
      if (!this.client.channelCount) void this.dispose()
    }, this.idleMs)
    this.idle.unref?.()
  }
}
