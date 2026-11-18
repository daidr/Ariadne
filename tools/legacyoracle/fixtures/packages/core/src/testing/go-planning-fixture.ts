import { Database } from 'bun:sqlite'
import { planProjection, reviewsProjection, promptsProjection } from '../projections'
import type { AnyStoredEvent } from '../events'

const events = (await Bun.stdin.json()) as AnyStoredEvent[]
const db = new Database(':memory:')
db.exec(await Bun.file(new URL('../../../../internal/schema/schema.sql', import.meta.url)).text())
try {
  console.info(
    JSON.stringify(
      events.map((event) => {
        for (const projection of [planProjection, reviewsProjection, promptsProjection])
          projection.apply(db, event)
        return Object.fromEntries(
          [
            'plan_nodes',
            'plan_edges',
            'plan_node_issues',
            'plan_revisions',
            'plan_proposals',
            'issue_writes',
            'reviews',
            'prompts',
          ].map((table) => [table, db.query(`SELECT * FROM ${table} ORDER BY rowid`).all()]),
        )
      }),
    ),
  )
} finally {
  db.close()
}
