import type {
  Ariadne,
  AnyStoredEvent,
  BackgroundUpdate,
  DraftUpdate,
  LiveUpdate,
} from '@ariadne/core'

const encoder = new TextEncoder()
export const MAX_SSE_PENDING_BYTES = 512 * 1024
export const MAX_SSE_PENDING_UPDATES = 256
interface Snapshot {
  key: string
  bytes: Uint8Array
}
function frame(event: AnyStoredEvent): Uint8Array {
  return encoder.encode(
    'id: ' + event.seq + '\nevent: ariadne\ndata: ' + JSON.stringify(event) + '\n\n',
  )
}
function snapshot(type: string, update: LiveUpdate | BackgroundUpdate | DraftUpdate): Snapshot {
  return {
    key: type + ':' + update.conversationId,
    bytes: encoder.encode('event: ' + type + '\ndata: ' + JSON.stringify(update) + '\n\n'),
  }
}

/** The log is the durable queue; only demanded frames and bounded transient snapshots live here. */
export function eventStream(
  app: Ariadne,
  options: {
    afterSeq: number
    projectId: string | null
    heartbeatMs?: number
    signal?: AbortSignal
  },
): Response {
  const pending = new Map<string, Uint8Array>()
  let pendingBytes = 0
  let lastSeq = options.afterSeq
  let dirty = true
  let closed = false
  let heartbeat = false
  let timer: ReturnType<typeof setInterval> | undefined
  let wake: (() => void) | undefined
  const stops: (() => void)[] = []
  let initial: Generator<Snapshot> | null = snapshots()
  let controller: ReadableStreamDefaultController<Uint8Array>
  const matches = (projectId: string) =>
    options.projectId === null || projectId === options.projectId
  const notify = () => {
    const resolve = wake
    wake = undefined
    resolve?.()
  }
  const abort = () => finish()
  function cleanup() {
    for (const stop of stops.splice(0)) stop()
    clearInterval(timer)
    options.signal?.removeEventListener('abort', abort)
    initial?.return(undefined)
    initial = null
    pending.clear()
    pendingBytes = 0
    notify()
  }
  function finish(error?: Error) {
    if (closed) return
    closed = true
    cleanup()
    if (error) controller.error(error)
    else controller.close()
  }
  function take(key: string): Uint8Array | undefined {
    const bytes = pending.get(key)
    if (bytes) {
      pending.delete(key)
      pendingBytes -= bytes.byteLength
    }
    return bytes
  }
  function put(type: string, update: LiveUpdate | BackgroundUpdate | DraftUpdate) {
    if (closed || !matches(update.projectId)) return
    const { key, bytes } = snapshot(type, update)
    take(key)
    if (
      pending.size >= MAX_SSE_PENDING_UPDATES ||
      pendingBytes + bytes.byteLength > MAX_SSE_PENDING_BYTES
    ) {
      finish(new Error('The event stream consumer is too slow; reconnect with Last-Event-ID'))
      return
    }
    pending.set(key, bytes)
    pendingBytes += bytes.byteLength
    notify()
  }
  function* snapshots(): Generator<Snapshot> {
    for (const command of app.live.list(options.projectId))
      yield snapshot('live', {
        projectId: command.projectId,
        conversationId: command.conversationId,
        command,
      })
    for (const update of app.background.list(options.projectId))
      yield snapshot('background', update)
    for (const update of app.drafts.list(options.projectId)) yield snapshot('draft', update)
  }
  const stream = new ReadableStream<Uint8Array>(
    {
      start(next) {
        controller = next
        // Subscribe before replay so new log entries cannot fall through a gap.
        stops.push(
          app.subscribe((events) => {
            if (events.some((event) => matches(event.projectId))) {
              dirty = true
              notify()
            }
          }),
        )
        stops.push(app.live.subscribe((update) => put('live', update)))
        stops.push(app.background.subscribe((update) => put('background', update)))
        stops.push(app.drafts.subscribe((update) => put('draft', update)))
        timer = setInterval(() => {
          heartbeat = true
          notify()
        }, options.heartbeatMs ?? 15_000)
        options.signal?.addEventListener('abort', abort, { once: true })
        if (options.signal?.aborted) finish()
      },
      async pull() {
        try {
          for (;;) {
            if (closed) return
            if (dirty) {
              const [event] = app.events({
                afterSeq: lastSeq,
                limit: 1,
                ...(options.projectId ? { projectId: options.projectId } : {}),
              })
              if (event) {
                lastSeq = event.seq
                controller.enqueue(frame(event))
                return
              }
              dirty = false
            }
            const entry = initial?.next()
            if (entry && !entry.done) {
              controller.enqueue(take(entry.value.key) ?? entry.value.bytes)
              return
            }
            initial = null
            const key = pending.keys().next().value
            if (key !== undefined) {
              controller.enqueue(take(key)!)
              return
            }
            if (heartbeat) {
              heartbeat = false
              controller.enqueue(encoder.encode(': ping\n\n'))
              return
            }
            await new Promise<void>((resolve) => {
              wake = resolve
            })
          }
        } catch (error) {
          finish(error instanceof Error ? error : new Error(String(error)))
        }
      },
      cancel() {
        if (closed) return
        closed = true
        cleanup()
      },
    },
    { highWaterMark: 0 },
  )
  return new Response(stream, {
    headers: {
      'content-type': 'text/event-stream',
      'cache-control': 'no-store',
      connection: 'keep-alive',
    },
  })
}
