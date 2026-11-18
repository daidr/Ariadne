/** Existing projection oracle. Bun is used only by migration tests. */
import { Database } from 'bun:sqlite'
import { projectsProjection, threadsProjection, conversationsProjection } from '../projections'
import type { AnyStoredEvent } from '../events'

const events = (await Bun.stdin.json()) as AnyStoredEvent[]
const db = new Database(':memory:')
db.exec(await Bun.file(new URL('../../../../internal/schema/schema.sql', import.meta.url)).text())
const snapshots = events.map((event) => {
  for (const projection of [projectsProjection, threadsProjection, conversationsProjection]) {
    projection.apply(db, event)
  }
  return Object.fromEntries(
    ['projects', 'threads', 'conversations', 'transcript'].map((table) => [
      table,
      db.query(`SELECT * FROM ${table} ORDER BY rowid`).all(),
    ]),
  )
})
console.info(JSON.stringify(snapshots))
db.close()
