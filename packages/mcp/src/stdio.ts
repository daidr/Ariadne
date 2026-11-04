import { McpConnectionError, parseMessage, type JsonRpcMessage } from './protocol'
import { MAX_MESSAGE_BYTES, type Transport } from './transport'

/** What the server last wrote to stderr is kept this long, for the user to read. */
const STDERR_KEPT = 4000
/** A server that does not exit this long after its input closed is killed. */
const EXIT_GRACE_MS = 2000

const windows = process.platform === 'win32'

export interface StdioOptions {
  /** The whole command line, already wrapped in a sandbox if it runs in one. */
  argv: string[]
  cwd: string
  env: Record<string, string>
}

/**
 * A server run as a local process, messages one per line on its stdin and stdout. It gets
 * a process group of its own, so what it starts ends with it.
 */
export class StdioTransport implements Transport {
  private proc: ReturnType<typeof Bun.spawn<'pipe', 'pipe', 'pipe'>> | null = null
  private stderr = ''
  private closing = false

  constructor(private readonly options: StdioOptions) {}

  /** The end of what the server wrote to stderr: why it failed, for the user. */
  stderrTail(): string {
    return this.stderr.trim()
  }

  async start(
    onMessage: (message: JsonRpcMessage) => void,
    onClose: (reason: string) => void,
  ): Promise<void> {
    let proc
    try {
      proc = Bun.spawn(this.options.argv, {
        cwd: this.options.cwd,
        env: this.options.env,
        stdin: 'pipe',
        stdout: 'pipe',
        stderr: 'pipe',
        detached: !windows,
      })
    } catch (error) {
      throw new McpConnectionError(
        `could not start ${this.options.argv[0] ?? ''}: ${error instanceof Error ? error.message : String(error)}`,
      )
    }
    this.proc = proc
    void this.readStderr(proc.stderr)
    void this.readLines(proc.stdout, onMessage)
    void proc.exited.then((code) => {
      if (this.closing) return
      onClose(`the process exited with code ${code}`)
    })
  }

  private async readLines(
    stream: ReadableStream<Uint8Array>,
    onMessage: (message: JsonRpcMessage) => void,
  ) {
    const decoder = new TextDecoder()
    let buffer = ''
    try {
      for await (const chunk of stream) {
        buffer += decoder.decode(chunk, { stream: true })
        let newline = buffer.indexOf('\n')
        while (newline !== -1) {
          const line = buffer.slice(0, newline).trim()
          buffer = buffer.slice(newline + 1)
          // Lines that are not JSON-RPC (a stray log line) are skipped.
          const message = line ? parseMessage(line) : null
          if (message) onMessage(message)
          newline = buffer.indexOf('\n')
        }
        if (buffer.length > MAX_MESSAGE_BYTES) {
          buffer = ''
          void this.close()
          return
        }
      }
    } catch {
      // The process ended; `exited` reports it.
    }
  }

  private async readStderr(stream: ReadableStream<Uint8Array>) {
    const decoder = new TextDecoder()
    try {
      for await (const chunk of stream) {
        this.stderr = (this.stderr + decoder.decode(chunk, { stream: true })).slice(-STDERR_KEPT)
      }
    } catch {
      // Gone with the process.
    }
  }

  async send(message: JsonRpcMessage): Promise<void> {
    const proc = this.proc
    if (!proc || this.closing) throw new McpConnectionError('the server is not running')
    try {
      proc.stdin.write(`${JSON.stringify(message)}\n`)
      await proc.stdin.flush()
    } catch (error) {
      throw new McpConnectionError(
        `could not write to the server: ${error instanceof Error ? error.message : String(error)}`,
      )
    }
  }

  async close(): Promise<void> {
    const proc = this.proc
    if (!proc || this.closing) return
    this.closing = true
    try {
      await proc.stdin.end()
    } catch {
      // Already closed.
    }
    const exited = await Promise.race([
      proc.exited.then(() => true),
      Bun.sleep(EXIT_GRACE_MS).then(() => false),
    ])
    if (windows) {
      Bun.spawn(['taskkill', '/T', '/F', '/PID', String(proc.pid)], {
        stdout: 'ignore',
        stderr: 'ignore',
        stdin: 'ignore',
      })
      return
    }
    // What it left running in its group goes too, whether or not it exited itself.
    try {
      process.kill(-proc.pid, exited ? 'SIGKILL' : 'SIGTERM')
    } catch {
      // Already gone.
    }
    if (!exited) {
      const done = await Promise.race([
        proc.exited.then(() => true),
        Bun.sleep(EXIT_GRACE_MS).then(() => false),
      ])
      try {
        process.kill(-proc.pid, 'SIGKILL')
      } catch {
        // Gone.
      }
      if (!done) proc.kill('SIGKILL')
    }
  }
}
