import type { CallTiming, ReasoningRecord, TokenUsage, ToolCallRecord } from '@ariadne/core'
import { MAX_RETRY_AFTER_MS, type ModelRequest, type ModelStreamPart } from '@ariadne/plugin-sdk'
import { usageOf } from './compaction'
import type { Limits } from './limits'
import type { LocalizedMessage } from '@ariadne/i18n'
import type { ResolvedModel } from './model'

/** Why a turn stopped early, for the user. */
export class TurnError extends Error {
  /**
   * `exhausted`: the model failed on an error worth retrying and its retries ran out, so
   * another model may do better.
   */
  constructor(
    readonly localized: LocalizedMessage,
    readonly exhausted: { detail: string } | null = null,
  ) {
    super(localized.key)
  }
}

/**
 * One model response. Only errors the provider marks retryable are tried again, at most
 * `limits.modelRetries` times with a growing wait, and each retry is recorded so the user
 * sees it; what a failed attempt streamed is dropped. A request with no output for
 * `timeoutSeconds` is given up and counts as such an error. `null` when aborted.
 */
export async function requestStep(
  model: ResolvedModel,
  request: ModelRequest & { signal: AbortSignal },
  onRetry: (retry: { attempt: number; delayMs: number; detail: string }) => void,
  baseDelayMs: number,
  limits: Limits,
  timeoutSeconds?: number,
): Promise<{
  text: string
  reasoning: ReasoningRecord[]
  toolCalls: ToolCallRecord[]
  usage: TokenUsage | null
  timing: CallTiming
  /** The output hit the length limit; a call it was writing then is left out. */
  cut: boolean
} | null> {
  for (let attempt = 0; ; attempt++) {
    let text = ''
    const reasoning: ReasoningRecord[] = []
    const toolCalls: ToolCallRecord[] = []
    let usage: TokenUsage | null = null
    let failure: Extract<ModelStreamPart, { type: 'finish' }> | null = null
    let cut = false
    // What came last before the end: the part the limit cut short.
    let last: ModelStreamPart['type'] | null = null
    const timing: CallTiming = { startedAt: Date.now(), firstOutputAt: null }
    // Each attempt has its own signal, so a silent request can be given up alone.
    const attemptController = new AbortController()
    const forward = () => attemptController.abort(request.signal.reason)
    request.signal.addEventListener('abort', forward, { once: true })
    let silent = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const wait = () => {
      if (!timeoutSeconds) return
      clearTimeout(timer)
      timer = setTimeout(() => {
        silent = true
        attemptController.abort('timeout')
      }, timeoutSeconds * 1000)
    }
    wait()
    const attemptRequest = { ...request, signal: attemptController.signal }
    try {
      for await (const part of model.provider.stream(model.settings as never, attemptRequest)) {
        wait()
        if (part.type === 'text' || part.type === 'reasoning' || part.type === 'tool-call') {
          // Reasoning is handed on whole; it began when its first piece came.
          timing.firstOutputAt ??= (part.type === 'reasoning' && part.startedAt) || Date.now()
        }
        if (part.type === 'text' || part.type === 'reasoning' || part.type === 'tool-call') {
          last = part.type
        }
        if (part.type === 'text') text += part.text
        else if (part.type === 'reasoning') {
          const { type: _, startedAt: __, ...block } = part
          reasoning.push(block)
        } else if (part.type === 'tool-call')
          toolCalls.push({ id: part.id, name: part.name, arguments: part.arguments })
        else if (part.type === 'usage') usage = usageOf(part)
        else if (part.type === 'finish' && part.reason === 'error') {
          failure = part
          break
        } else if (part.type === 'finish' && part.reason === 'length') {
          // Cut-off tool arguments are not a call: the one being written goes.
          cut = true
          if (last === 'tool-call') toolCalls.pop()
        }
      }
    } finally {
      clearTimeout(timer)
      request.signal.removeEventListener('abort', forward)
    }
    if (request.signal.aborted) return null
    if (silent) {
      failure = {
        type: 'finish',
        reason: 'error',
        retryable: true,
        error: `no response for ${timeoutSeconds} seconds`,
      }
    }
    if (!failure) return { text, reasoning, toolCalls, usage, timing, cut }
    const detail = (failure.error ?? '').slice(0, 500)
    if (!failure.retryable || attempt >= limits.modelRetries) {
      // A service that cannot take images refuses every request that carries one.
      const images = request.messages.some(
        (message) => (message.role === 'user' || message.role === 'tool') && message.images?.length,
      )
      throw new TurnError(
        { key: images ? 'error.modelRequestImages' : 'error.modelRequest', params: { detail } },
        // Busy past the retries: another model may answer. A wrong key or a bad request
        // would fail there the same way.
        failure.busy ? { detail } : null,
      )
    }
    const delayMs = Math.min(MAX_RETRY_AFTER_MS, failure.retryAfterMs ?? baseDelayMs * 2 ** attempt)
    onRetry({ attempt: attempt + 1, delayMs, detail })
    await abortableSleep(delayMs, request.signal)
    if (request.signal.aborted) return null
  }
}

export function abortableSleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) return resolve()
    const timer = setTimeout(done, ms)
    function done() {
      clearTimeout(timer)
      signal.removeEventListener('abort', done)
      resolve()
    }
    signal.addEventListener('abort', done, { once: true })
  })
}
