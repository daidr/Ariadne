import type { ShikiLanguageKey } from './languages'

export interface ShikiHighlightRequest {
  id: number
  code: string
  lang: ShikiLanguageKey
}

export type ShikiHighlightResponse =
  { id: number; ok: true; html: string } | { id: number; ok: false; error: string }

/** Minimal Worker surface needed by the pool, making it straightforward to test without real threads. */
export interface ShikiWorkerPort {
  postMessage(message: ShikiHighlightRequest): void
  addEventListener(
    type: 'message',
    listener: (event: MessageEvent<ShikiHighlightResponse>) => void,
  ): void
  addEventListener(type: 'error', listener: (event: ErrorEvent) => void): void
  removeEventListener(
    type: 'message',
    listener: (event: MessageEvent<ShikiHighlightResponse>) => void,
  ): void
  removeEventListener(type: 'error', listener: (event: ErrorEvent) => void): void
  terminate(): void
}

export interface ShikiWorkerScope {
  postMessage(message: ShikiHighlightResponse): void
  addEventListener(
    type: 'message',
    listener: (event: MessageEvent<ShikiHighlightRequest>) => void,
  ): void
}
