import { processes, type CommandSandbox } from '@ariadne/plugin-sdk'

/** What the agent last wrote to stderr is kept this long, to say why it failed. */
const STDERR_KEPT = 4000
/** An agent that does not exit this long after its input closed is killed. */
const EXIT_GRACE_MS = 2000
/** A single line longer than this ends the agent: a broken program cannot fill memory. */
const MAX_LINE_BYTES = 32 * 1024 * 1024

const windows = process.platform === 'win32'

/** The agent's program could not be started, or ended. */
export class AgentProcessError extends Error {}

/**
 * The words of a command line the user wrote, e.g. `my-agent --acp "a b"`. Quotes group
 * words; a backslash outside single quotes keeps the next character as it is.
 */
export function splitCommand(line: string): string[] {
  const words: string[] = []
  let word = ''
  let started = false
  let quote: '"' | "'" | null = null
  for (let i = 0; i < line.length; i++) {
    const char = line[i]!
    if (quote) {
      if (char === quote) quote = null
      else if (char === '\\' && quote === '"' && i + 1 < line.length) word += line[++i]
      else word += char
      continue
    }
    if (char === '"' || char === "'") {
      quote = char
      started = true
    } else if (char === '\\' && i + 1 < line.length && !windows) {
      word += line[++i]
      started = true
    } else if (/\s/.test(char)) {
      if (started) words.push(word)
      word = ''
      started = false
    } else {
      word += char
      started = true
    }
  }
  if (started) words.push(word)
  return words
}

/** The server's environment without what is Ariadne's own (its token, its settings). */
export function agentEnv(extra: Record<string, string> = {}): Record<string, string> {
  const env: Record<string, string> = {}
  for (const [key, value] of Object.entries(process.env)) {
    if (value === undefined || /^ARIADNE_/i.test(key)) continue
    env[key] = value
  }
  return { ...env, ...extra }
}

export interface AgentProcessOptions {
  /** The program and its arguments, before the sandbox wraps them. */
  argv: string[]
  cwd: string
  env: Record<string, string>
  sandbox: CommandSandbox
  /** What it is, for the ledger of processes to kill after a crash. */
  label: string
}

/**
 * An agent program talking one JSON message per line on its stdin and stdout. It runs in
 * the sandbox threads' commands run in (Ariadne's home and the browsers' profiles are out
 * of its sight), in a process group of its own, so what it starts ends with it.
 */
export class AgentProcess {
  private proc: ReturnType<typeof Bun.spawn<'pipe', 'pipe', 'pipe'>> | null = null
  private stderr = ''
  private closing = false
  private release: () => void = () => {}
  private onExit: ((reason: string) => void) | null = null

  constructor(private readonly options: AgentProcessOptions) {}

  /** The end of what it wrote to stderr. */
  stderrTail(): string {
    return this.stderr.trim()
  }

  /** Start it: `onLine` gets every JSON value it prints, `onExit` once when it ends by itself. */
  start(onLine: (value: unknown) => void, onExit: (reason: string) => void): void {
    const [program = '', ...args] = this.options.argv
    // A bare name is looked up in the PATH the agent gets, so the sandbox runs the same file.
    const path = Bun.which(program, { PATH: this.options.env.PATH ?? '' }) ?? program
    const argv = this.options.sandbox.wrap([path, ...args], { keep: [this.options.cwd] })
    let proc
    try {
      proc = Bun.spawn(argv, {
        cwd: this.options.cwd,
        env: this.options.env,
        stdin: 'pipe',
        stdout: 'pipe',
        stderr: 'pipe',
        windowsHide: true,
        detached: !windows,
      })
    } catch (error) {
      throw new AgentProcessError(
        `could not start ${program}: ${error instanceof Error ? error.message : String(error)}`,
      )
    }
    this.proc = proc
    this.onExit = onExit
    this.release = processes.track(proc.pid, { group: !windows, label: this.options.label })
    void this.readStderr(proc.stderr)
    void this.readLines(proc.stdout, onLine)
    void proc.exited.then((code) => {
      this.release()
      if (this.closing) return
      const tail = this.stderrTail().split('\n').slice(-5).join('\n')
      this.end(`${program} exited with code ${code}${tail ? `: ${tail}` : ''}`)
    })
  }

  private end(reason: string) {
    const onExit = this.onExit
    this.onExit = null
    onExit?.(reason)
  }

  private async readLines(stream: ReadableStream<Uint8Array>, onLine: (value: unknown) => void) {
    const decoder = new TextDecoder()
    let pieces: string[] = []
    let size = 0
    const take = (line: string) => {
      const text = line.trim()
      if (!text) return
      let value: unknown
      try {
        value = JSON.parse(text)
      } catch {
        // A stray log line on stdout is not a message.
        return
      }
      onLine(value)
    }
    try {
      for await (const chunk of stream) {
        let text = decoder.decode(chunk, { stream: true })
        let newline = text.indexOf('\n')
        while (newline !== -1) {
          pieces.push(text.slice(0, newline))
          take(pieces.join(''))
          pieces = []
          size = 0
          text = text.slice(newline + 1)
          newline = text.indexOf('\n')
        }
        if (text) {
          pieces.push(text)
          size += text.length
        }
        if (size > MAX_LINE_BYTES) {
          this.end(`the agent wrote a line over ${MAX_LINE_BYTES / 1024 / 1024} MB`)
          void this.close()
          return
        }
      }
    } catch {
      // Ended with the process; `exited` says how.
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

  /** Write one message as a line. */
  send(value: unknown): void {
    const proc = this.proc
    if (!proc || this.closing) throw new AgentProcessError('the agent is not running')
    try {
      proc.stdin.write(`${JSON.stringify(value)}\n`)
      void proc.stdin.flush()
    } catch (error) {
      throw new AgentProcessError(
        `could not write to the agent: ${error instanceof Error ? error.message : String(error)}`,
      )
    }
  }

  get running(): boolean {
    return !!this.proc && !this.closing && this.proc.exitCode === null
  }

  /** Close its input, give it a moment to exit, then end it and what it started. */
  async close(): Promise<void> {
    const proc = this.proc
    if (!proc || this.closing) return
    this.closing = true
    this.onExit = null
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
      if (!exited) {
        Bun.spawn(['taskkill', '/T', '/F', '/PID', String(proc.pid)], {
          stdout: 'ignore',
          stderr: 'ignore',
          stdin: 'ignore',
          windowsHide: true,
        })
      }
    } else {
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
    this.release()
  }
}
