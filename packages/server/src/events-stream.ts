import type { Ariadne, AnyStoredEvent } from '@ariadne/core'

const encoder = new TextEncoder()

function frame(event: AnyStoredEvent): Uint8Array {
  return encoder.encode(`id: ${event.seq}\nevent: ariadne\ndata: ${JSON.stringify(event)}\n\n`)
}

/**
 * Server-sent events: first every stored event after `afterSeq`, then live ones.
 * Replay and subscribe happen in one synchronous step, so no event falls in between.
 */
export function eventStream(
  app: Ariadne,
  options: { afterSeq: number; projectId: string | null; heartbeatMs?: number },
): Response {
  let cleanup: (() => void) | undefined
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      const matches = (event: AnyStoredEvent) =>
        options.projectId === null || event.projectId === options.projectId
      let lastSeq = options.afterSeq
      const send = (event: AnyStoredEvent) => {
        if (event.seq <= lastSeq || !matches(event)) return
        lastSeq = event.seq
        controller.enqueue(frame(event))
      }
      // Page through the backlog, then switch to live delivery.
      for (;;) {
        const page = app.events({
          afterSeq: lastSeq,
          limit: 500,
          ...(options.projectId ? { projectId: options.projectId } : {}),
        })
        page.forEach(send)
        if (page.length < 500) break
      }
      const unsubscribe = app.subscribe((events) => events.forEach(send))
      const heartbeat = setInterval(
        () => controller.enqueue(encoder.encode(': ping\n\n')),
        options.heartbeatMs ?? 15_000,
      )
      cleanup = () => {
        unsubscribe()
        clearInterval(heartbeat)
      }
    },
    cancel() {
      cleanup?.()
    },
  })
  return new Response(stream, {
    headers: {
      'content-type': 'text/event-stream',
      'cache-control': 'no-store',
      connection: 'keep-alive',
    },
  })
}
