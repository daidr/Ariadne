/** Message and notice projection oracle; never part of the production runtime. */
import { Database } from 'bun:sqlite'
import {
  attentionsProjection,
  messagesProjection,
  todosProjection,
  noticesProjection,
  memoriesProjection,
} from '../projections'
import type { AnyStoredEvent } from '../events'

const input = (await Bun.stdin.json()) as { seed: string; events: AnyStoredEvent[] }
const db = new Database(':memory:')
db.exec(await Bun.file(new URL('../../../../internal/schema/schema.sql', import.meta.url)).text())
db.exec(input.seed)
try {
  console.info(
    JSON.stringify(
      input.events.map((event) => {
        for (const projection of [
          attentionsProjection,
          messagesProjection,
          todosProjection,
          noticesProjection,
          memoriesProjection,
        ]) {
          projection.apply(db, event)
        }
        return Object.fromEntries(
          [
            'attentions',
            'attention_waiters',
            'used_approvals',
            'messages',
            'todos',
            'notices',
            'memories',
          ].map((table) => [table, db.query(`SELECT * FROM ${table} ORDER BY rowid`).all()]),
        )
      }),
    ),
  )
} finally {
  db.close()
}
