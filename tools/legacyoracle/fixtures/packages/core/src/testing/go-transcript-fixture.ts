/** Wake and transcript oracle for the native backend migration. */
import { Database } from 'bun:sqlite'
import { transcriptProjection } from '../projections'
import type { AnyStoredEvent } from '../events'

const input = (await Bun.stdin.json()) as { seed: string; events: AnyStoredEvent[] }
const db = new Database(':memory:')
db.exec(await Bun.file(new URL('../../../../internal/schema/schema.sql', import.meta.url)).text())
db.exec(input.seed)
try {
  console.info(
    JSON.stringify(
      input.events.map((event) => {
        transcriptProjection.apply(db, event)
        return {
          transcript: db.query('SELECT * FROM transcript ORDER BY id').all(),
          conversations: db.query('SELECT * FROM conversations ORDER BY id').all(),
        }
      }),
    ),
  )
} finally {
  db.close()
}
