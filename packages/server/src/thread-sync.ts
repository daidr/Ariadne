/** What the page needs to bring its copy of a project's threads up to date. */
export interface ThreadSyncResult<T> {
  /** Given back as `since` next time. */
  cursor: string
  /** `threads` holds only the ones changed since the cursor asked with, not all of them. */
  partial: boolean
  threads: T[]
  /** Every thread's id in order; left out of a partial answer when none came, went or moved. */
  ids?: string[]
}

interface Known {
  /** The `rev` at which anything of it last changed. */
  rev: number
  /** The `rev` at which a thread last came, went or moved. */
  idsRev: number
  /** The ids in the order last seen. */
  ids: string
  /** Each thread as it was last seen, and the `rev` at which it last changed. */
  threads: Map<string, { hash: number | bigint; rev: number }>
}

/**
 * Remembers each thread as it was last sent, so a page that names what it has gets only
 * the threads that differ. What a thread shows comes from many tables, so a change is
 * found by comparing the whole thread rather than by tracking every write to them. The
 * memory is this process's: a cursor from before a restart gets everything again.
 */
export class ThreadSync {
  private readonly epoch = crypto.randomUUID().slice(0, 8)
  private readonly projects = new Map<string, Known>()
  /** Counts the answers in which something had changed, over every project. */
  private rev = 0

  delta<T extends { id: string }>(
    projectId: string,
    threads: T[],
    since: string | null,
  ): ThreadSyncResult<T> {
    let known = this.projects.get(projectId)
    if (!known)
      this.projects.set(projectId, (known = { rev: 0, idsRev: 0, ids: '', threads: new Map() }))
    const hashes = threads.map((thread) => Bun.hash(JSON.stringify(thread)))
    const ids = threads.map((thread) => thread.id).join('\n')
    const moved = ids !== known.ids
    const changed = threads.some(
      (thread, index) => known.threads.get(thread.id)?.hash !== hashes[index],
    )
    if (changed || moved) {
      const rev = (known.rev = ++this.rev)
      const before = known.threads
      known.threads = new Map(
        threads.map((thread, index) => {
          const was = before.get(thread.id)
          const hash = hashes[index]!
          return [thread.id, was?.hash === hash ? was : { hash, rev }]
        }),
      )
      if (moved) [known.ids, known.idsRev] = [ids, rev]
    }
    const cursor = `${this.epoch}.${known.rev}`
    const from = this.revOf(since, known)
    if (from === null) return { cursor, partial: false, threads }
    const seen = known.threads
    return {
      cursor,
      partial: true,
      threads: threads.filter((thread) => seen.get(thread.id)!.rev > from),
      ...(known.idsRev > from ? { ids: threads.map((thread) => thread.id) } : {}),
    }
  }

  /** The project is gone; so is what was remembered of it. */
  forget(projectId: string) {
    this.projects.delete(projectId)
  }

  /** The `rev` a cursor of this process names, or `null` when it is not one. */
  private revOf(since: string | null, known: Known): number | null {
    const [epoch, text] = (since ?? '').split('.')
    if (epoch !== this.epoch || !text || !/^\d+$/.test(text)) return null
    const rev = Number(text)
    return rev <= known.rev ? rev : null
  }
}
