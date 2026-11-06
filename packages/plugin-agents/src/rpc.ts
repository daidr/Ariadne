import { AgentProcessError, type AgentProcess } from './process'

type Id = string | number

interface Message {
  jsonrpc?: string
  id?: Id | null
  method?: string
  params?: unknown
  result?: unknown
  error?: { code?: number; message?: string; data?: unknown }
}

/** An error the other side answered a request with. */
export class RpcError extends Error {
  constructor(
    readonly code: number,
    message: string,
    readonly data?: unknown,
  ) {
    super(message)
  }
}

/** A request from the agent was not handled: it gets this error. */
export class Unhandled extends Error {}

/**
 * JSON-RPC over an agent's stdin and stdout, both ways: Ariadne asks the agent, and the
 * agent asks Ariadne (to run a tool, to approve something). `strict` writes the
 * `"jsonrpc": "2.0"` field; some agents speak the protocol without it.
 */
export class RpcPeer {
  private next = 1
  private readonly waiting = new Map<
    Id,
    { resolve: (v: unknown) => void; reject: (e: Error) => void }
  >()
  private ended: string | null = null

  constructor(
    private readonly process: AgentProcess,
    private readonly strict: boolean,
    private readonly handlers: {
      request: (method: string, params: unknown) => Promise<unknown>
      notification: (method: string, params: unknown) => void
    },
  ) {}

  /** Feed one line the agent printed. */
  take(value: unknown): void {
    const message = value as Message
    if (!message || typeof message !== 'object') return
    if (message.method && message.id !== undefined && message.id !== null) {
      void this.answer(message.id, message.method, message.params)
    } else if (message.method) {
      this.handlers.notification(message.method, message.params)
    } else if (message.id !== undefined && message.id !== null) {
      const waiter = this.waiting.get(message.id)
      if (!waiter) return
      this.waiting.delete(message.id)
      if (message.error) {
        waiter.reject(
          new RpcError(
            message.error.code ?? -32000,
            message.error.message ?? 'error',
            message.error.data,
          ),
        )
      } else waiter.resolve(message.result)
    }
  }

  /** The agent is gone: every open request fails with `reason`. */
  end(reason: string): void {
    this.ended = reason
    for (const waiter of this.waiting.values()) waiter.reject(new AgentProcessError(reason))
    this.waiting.clear()
  }

  request<T = unknown>(method: string, params: unknown): Promise<T> {
    if (this.ended) return Promise.reject(new AgentProcessError(this.ended))
    const id = this.next++
    return new Promise<T>((resolve, reject) => {
      this.waiting.set(id, { resolve: resolve as (v: unknown) => void, reject })
      try {
        this.process.send({ ...(this.strict ? { jsonrpc: '2.0' } : {}), id, method, params })
      } catch (error) {
        this.waiting.delete(id)
        reject(error as Error)
      }
    })
  }

  notify(method: string, params?: unknown): void {
    try {
      this.process.send({
        ...(this.strict ? { jsonrpc: '2.0' } : {}),
        method,
        ...(params === undefined ? {} : { params }),
      })
    } catch {
      // Gone; its exit is reported elsewhere.
    }
  }

  private async answer(id: Id, method: string, params: unknown) {
    let reply: Record<string, unknown>
    try {
      reply = { id, result: (await this.handlers.request(method, params)) ?? null }
    } catch (error) {
      const code = error instanceof Unhandled ? -32601 : -32603
      reply = {
        id,
        error: { code, message: error instanceof Error ? error.message : String(error) },
      }
    }
    try {
      this.process.send({ ...(this.strict ? { jsonrpc: '2.0' } : {}), ...reply })
    } catch {
      // Gone.
    }
  }
}

/** A provider error that may go away if tried again: the service was busy or broke off. */
export function busyText(text: string): boolean {
  return /overload|rate.?limit|too many requests|\b429\b|\b5\d\d\b|capacity|temporar|timed? ?out|ECONNRESET|socket hang up/i.test(
    text,
  )
}
