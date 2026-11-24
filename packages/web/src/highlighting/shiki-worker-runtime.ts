import type {
  ShikiHighlightRequest,
  ShikiHighlightResponse,
  ShikiWorkerScope,
} from './shiki-worker-protocol'

export interface ShikiHtmlHighlightRunner {
  highlightToHtml(code: string, lang: string): Promise<string>
}

/**
 * Bind a Shiki HTML highlighter to a Web Worker global scope.
 * Each incoming request is resolved independently and posted back with its matching `id`.
 */
export function attachShikiWorker(scope: ShikiWorkerScope, runner: ShikiHtmlHighlightRunner): void {
  const reply = (message: ShikiHighlightResponse) => {
    // DedicatedWorkerGlobalScope.postMessage has no targetOrigin argument.
    // oxlint-disable-next-line unicorn/require-post-message-target-origin
    scope.postMessage(message)
  }

  scope.addEventListener('message', (event: MessageEvent<ShikiHighlightRequest>) => {
    const request = event.data
    if (!request || typeof request.id !== 'number') return
    void runner
      .highlightToHtml(request.code, request.lang)
      .then((html) => {
        reply({ id: request.id, ok: true, html })
      })
      .catch((error: unknown) => {
        reply({
          id: request.id,
          ok: false,
          error: error instanceof Error ? error.message : String(error),
        })
      })
  })
}
