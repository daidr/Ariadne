import { Database } from 'bun:sqlite'
import { artifactsProjection } from '../projections'
import { searchProjection, searchRows, searchTerms, searchTextOf, snippetOf } from '../search'
import type { AnyStoredEvent, ArtifactKind } from '../events'

const input = (await Bun.stdin.json()) as {
  events: AnyStoredEvent[]
  queries: { projectId: string; kind: 'thread' | 'artifact' | 'message'; query: string }[]
  texts: { kind: ArtifactKind; content: string; query: string }[]
}
const db = new Database(':memory:')
db.exec(await Bun.file(new URL('../../../../internal/schema/schema.sql', import.meta.url)).text())
try {
  const snapshots = input.events.map((event) => {
    artifactsProjection.apply(db, event)
    searchProjection.apply(db, event)
    return Object.fromEntries(
      [
        'artifacts',
        'artifact_versions',
        'artifact_comments',
        'artifact_feedback',
        'search_docs',
        'search_text',
      ].map((table) => [
        table,
        db
          .query(`SELECT ${table === 'search_text' ? 'rowid, ' : ''}* FROM ${table} ORDER BY rowid`)
          .all(),
      ]),
    )
  })
  const searches = input.queries.map(({ projectId, kind, query }) => {
    const terms = searchTerms(query)
    return { terms, rows: searchRows(db, projectId, kind, terms, 30) }
  })
  const texts = input.texts.map(({ kind, content, query }) => ({
    text: searchTextOf(kind, content),
    snippet: snippetOf(content, searchTerms(query)),
  }))
  console.info(JSON.stringify({ snapshots, searches, texts }))
} finally {
  db.close()
}
