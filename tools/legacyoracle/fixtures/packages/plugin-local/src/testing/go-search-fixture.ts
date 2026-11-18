import { searchByReading, walk, type SearchOptions } from '@ariadne/plugin-sdk'
import { LocalWorkspace } from '../index'

const input = JSON.parse(await Bun.stdin.text()) as {
  root: string
  queries: ({ pattern: string; path: string } & SearchOptions)[]
}
const workspace = new LocalWorkspace('source', input.root, null, { git: false })
const results = []
for (const query of input.queries) {
  results.push(await searchByReading(workspace, query.pattern, query.path, query))
}
const found = await walk(workspace, '.')
await Bun.write(Bun.stdout, JSON.stringify({ results, files: found.files.toSorted() }))
