import type { InlineContent, PartialBlock } from '@dou-ui/editor'

type Text = Extract<InlineContent, { type: 'text' }>

/** A run of text in the blocks, where it sits and where it starts in all the text. */
interface Run {
  list: InlineContent[]
  index: number
  start: number
  text: Text
}

/**
 * Every run of text in document order: a block's content, its table cells, then its
 * children, as the editor reads them. Code blocks take no comments and are left out.
 */
function runsOf(blocks: PartialBlock[]): { runs: Run[]; text: string } {
  const runs: Run[] = []
  let text = ''
  const visit = (list: InlineContent[]) => {
    for (const [index, item] of list.entries()) {
      if (item.type !== 'text') continue
      runs.push({ list, index, start: text.length, text: item })
      text += item.text
    }
  }
  const walk = (all: PartialBlock[]) => {
    for (const block of all) {
      if (block.type !== 'codeBlock') {
        if (typeof block.content === 'string')
          block.content = [{ type: 'text', text: block.content }]
        if (block.content) visit(block.content)
        for (const row of block.rows ?? []) {
          for (const [i, cell] of row.cells.entries()) {
            const content = typeof cell === 'string' ? cell : cell.content
            const list: InlineContent[] =
              typeof content === 'string' || content === undefined
                ? [{ type: 'text', text: content ?? '' }]
                : content
            row.cells[i] = typeof cell === 'string' ? { content: list } : { ...cell, content: list }
            visit(list)
          }
        }
      }
      if (block.children) walk(block.children)
    }
  }
  walk(blocks)
  return { runs, text }
}

/**
 * Mark each thread's quote, its first appearance, on a copy of the blocks read from
 * Markdown, which keeps no comments; with the ids found.
 */
export function anchorQuotes(
  source: readonly PartialBlock[],
  threads: readonly { id: string; quote: string }[],
): { blocks: PartialBlock[]; found: Set<string> } {
  const blocks = structuredClone(source) as PartialBlock[]
  const found = new Set<string>()
  for (const thread of threads) {
    if (!thread.quote) continue
    const { runs, text } = runsOf(blocks)
    const from = text.indexOf(thread.quote)
    if (from < 0) continue
    const to = from + thread.quote.length
    found.add(thread.id)
    // From the end, so splitting a run leaves the places of those before it.
    for (const run of runs.toReversed()) {
      const end = run.start + run.text.text.length
      if (end <= from || run.start >= to) continue
      const a = Math.max(from, run.start) - run.start
      const b = Math.min(to, end) - run.start
      const piece = (part: string, own: boolean): Text => ({
        ...run.text,
        text: part,
        ...(own ? { comments: [...(run.text.comments ?? []), thread.id] } : {}),
      })
      const parts = [
        piece(run.text.text.slice(0, a), false),
        piece(run.text.text.slice(a, b), true),
        piece(run.text.text.slice(b), false),
      ].filter((part) => part.text)
      run.list.splice(run.index, 1, ...parts)
    }
  }
  return { blocks, found }
}

/** The ids of the threads marked on the blocks. */
export function anchoredIds(blocks: readonly PartialBlock[]): Set<string> {
  const ids = new Set<string>()
  const visit = (list: PartialBlock['content']) => {
    if (!Array.isArray(list)) return
    for (const item of list)
      if (item.type === 'text') for (const id of item.comments ?? []) ids.add(id)
  }
  const walk = (all: readonly PartialBlock[]) => {
    for (const block of all) {
      visit(block.content)
      for (const row of block.rows ?? []) {
        for (const cell of row.cells) if (typeof cell !== 'string') visit(cell.content)
      }
      if (block.children) walk(block.children)
    }
  }
  walk(blocks)
  return ids
}
