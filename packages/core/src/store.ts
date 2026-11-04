import { Database } from 'bun:sqlite'
import type { AnyNewEvent, AnyStoredEvent } from './events'
import { newId } from './ids'

/**
 * A projection folds events into its own tables. `apply` runs in the same transaction
 * as the append, so a projection is never behind or ahead of the log.
 */
export interface Projection {
  name: string
  setup(db: Database): void
  apply(db: Database, event: AnyStoredEvent): void
}

export type EventListener = (events: AnyStoredEvent[]) => void

export interface EventRow {
  seq: number
  id: string
  type: string
  project_id: string
  data: string
  at: number
}

export function toStored(row: EventRow): AnyStoredEvent {
  return {
    seq: row.seq,
    id: row.id,
    type: row.type,
    projectId: row.project_id,
    data: JSON.parse(row.data),
    at: row.at,
  } as AnyStoredEvent
}

export interface EventStoreOptions {
  /** A file path, or `:memory:`. */
  path: string
  projections: Projection[]
  now?: () => number
}

/** The append-only event log, the single source of truth. */
export class EventStore {
  readonly db: Database
  private readonly projections: Projection[]
  private readonly listeners = new Set<EventListener>()
  private readonly now: () => number

  constructor(options: EventStoreOptions) {
    this.db = new Database(options.path, { create: true, strict: true })
    this.db.run('PRAGMA journal_mode = WAL')
    this.db.run('PRAGMA foreign_keys = ON')
    this.db.run(`CREATE TABLE IF NOT EXISTS events (
      seq INTEGER PRIMARY KEY AUTOINCREMENT,
      id TEXT NOT NULL UNIQUE,
      type TEXT NOT NULL,
      project_id TEXT NOT NULL,
      data TEXT NOT NULL,
      at INTEGER NOT NULL
    )`)
    this.db.run('CREATE INDEX IF NOT EXISTS events_project ON events (project_id, seq)')
    this.projections = options.projections
    this.now = options.now ?? Date.now
    for (const projection of this.projections) projection.setup(this.db)
  }

  /** Append events atomically and fold them into every projection. */
  append(events: AnyNewEvent[]): AnyStoredEvent[] {
    const insert = this.db.query<EventRow, [string, string, string, string, number]>(
      'INSERT INTO events (id, type, project_id, data, at) VALUES (?, ?, ?, ?, ?) RETURNING *',
    )
    const stored = this.db.transaction(() =>
      events.map((event) => {
        const row = insert.get(
          newId('evt'),
          event.type,
          event.projectId,
          JSON.stringify(event.data),
          this.now(),
        )
        if (!row) throw new Error('event insert returned no row')
        const storedEvent = toStored(row)
        for (const projection of this.projections) projection.apply(this.db, storedEvent)
        return storedEvent
      }),
    )()
    // The transaction decides the result. A failing listener is logged and must not keep
    // the others from seeing these events.
    for (const listener of this.listeners) {
      try {
        listener(stored)
      } catch (error) {
        console.error('event listener failed', error)
      }
    }
    return stored
  }

  /** Events after `afterSeq`, optionally of one project, oldest first. */
  read(options: { afterSeq?: number; projectId?: string; limit?: number } = {}): AnyStoredEvent[] {
    const { afterSeq = 0, projectId, limit = 1000 } = options
    const rows = projectId
      ? this.db
          .query<EventRow, [number, string, number]>(
            'SELECT * FROM events WHERE seq > ? AND project_id = ? ORDER BY seq LIMIT ?',
          )
          .all(afterSeq, projectId, limit)
      : this.db
          .query<EventRow, [number, number]>(
            'SELECT * FROM events WHERE seq > ? ORDER BY seq LIMIT ?',
          )
          .all(afterSeq, limit)
    return rows.map(toStored)
  }

  lastSeq(): number {
    return (
      this.db.query<{ seq: number | null }, []>('SELECT max(seq) AS seq FROM events').get()?.seq ??
      0
    )
  }

  subscribe(listener: EventListener): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  /**
   * Drop every projection table and fold the whole log again. Used after a projection's
   * shape changes; the log itself is never rewritten.
   */
  rebuild(reset: (db: Database) => void): void {
    this.db.transaction(() => {
      reset(this.db)
      for (const projection of this.projections) projection.setup(this.db)
      for (const event of this.readAllUnbounded()) {
        for (const projection of this.projections) projection.apply(this.db, event)
      }
    })()
  }

  close(): void {
    this.listeners.clear()
    this.db.close()
  }

  private readAllUnbounded(): AnyStoredEvent[] {
    return this.db.query<EventRow, []>('SELECT * FROM events ORDER BY seq').all().map(toStored)
  }
}
