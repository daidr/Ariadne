import { readPartialJson } from './partial-json'
import type { LiveCall, Tool, ToolContext } from './tool-helpers'

/**
 * Tool calls shown at work while the model is still writing them: a tool that can (one
 * that writes a document or a whiteboard) is handed the arguments as far as they have
 * come, a few times a second, and shows the change landing. The call itself runs once it
 * is whole, as always, and takes over from what was shown; `reset` takes back whatever
 * was shown for calls that never ran (the response failed, was cut short, or stopped).
 */
export interface LiveCalls {
  /** The arguments of the response's `call`th tool call (from 0), as far as written. */
  input(call: number, name: string, text: string): void
  reset(): void
}

/** The arguments are read at most this often. */
const INTERVAL_MS = 120
/**
 * Showing a piece is work the server does between everything else (a long document is
 * read whole each time): the next waits this many times as long as the last took, so the
 * showing never has more than a part of the server's time.
 */
const REST = 3

interface Entry {
  name: string
  live: LiveCall | null
  /** When the next piece may be shown. */
  next: number
  text: string
}

/** Hand a call what is written of it so far; one that cannot be shown is given up. */
function show(entry: Entry) {
  if (!entry.live) return
  const read = readPartialJson(entry.text)
  if (!read || typeof read.value !== 'object' || !read.value || Array.isArray(read.value)) return
  try {
    entry.live.input(read.value as Record<string, unknown>, read.open)
  } catch {
    // Showing is a courtesy: whatever is wrong with the call, running it reports.
    entry.live.cancel()
    entry.live = null
  }
}

export function liveCalls(byName: ReadonlyMap<string, Tool>, ctx: ToolContext): LiveCalls {
  const open = new Map<number, Entry>()
  return {
    input(call, name, text) {
      let entry = open.get(call)
      if (!entry) {
        entry = { name, live: byName.get(name)?.live?.(ctx) ?? null, next: 0, text }
        open.set(call, entry)
      }
      entry.text = text
      const began = performance.now()
      if (!entry.live || began < entry.next) return
      show(entry)
      const ended = performance.now()
      entry.next = ended + Math.max(INTERVAL_MS, (ended - began) * REST)
    },
    reset() {
      for (const entry of open.values()) entry.live?.cancel()
      open.clear()
    },
  }
}
