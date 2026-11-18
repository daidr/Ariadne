import { mentionRank, walkFiles } from '../mention-files'

const input = (await Bun.stdin.json()) as {
  files: string[]
  queries: string[]
  tree: Record<string, { name: string; kind: 'file' | 'directory' }[]>
}
console.info(
  JSON.stringify({
    ranks: input.queries.map((query) => input.files.map((file) => mentionRank(file, query))),
    selected: input.queries.map((query) =>
      input.files
        .flatMap((path) => {
          const rank = mentionRank(path, query)
          return rank === null ? [] : [{ path, rank }]
        })
        .toSorted((a, b) => a.rank - b.rank || a.path.length - b.path.length)
        .slice(0, 30)
        .map(({ path }) => path),
    ),
    walked: await walkFiles({ list: async (path) => input.tree[path] ?? [] }),
  }),
)
