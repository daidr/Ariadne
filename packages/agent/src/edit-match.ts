/**
 * Where a piece of text the model wants replaced is in a file: as written, or the same
 * lines with only their indentation and trailing whitespace different. When it is in neither, the nearest lines are
 * found, so the model sees what is there instead of guessing again.
 */

export type EditOutcome =
  | { ok: true; text: string; count: number; loose: { from: number; to: number }[] }
  | { ok: false; reason: string }

/**
 * A line without its indentation and trailing whitespace. Whitespace inside a line stays:
 * it may be in a string, which a loose match must not change.
 */
const squash = (line: string) => line.trim()

/** The lines of `text`, each with the offset it starts at; a `\r` before `\n` stays out. */
function linesOf(text: string): { line: string; start: number; end: number }[] {
  const lines: { line: string; start: number; end: number }[] = []
  let start = 0
  for (;;) {
    const newline = text.indexOf('\n', start)
    const end = newline === -1 ? text.length : newline
    lines.push({ line: text.slice(start, end).replace(/\r$/, ''), start, end })
    if (newline === -1) return lines
    start = newline + 1
  }
}

/** The lines of a snippet without blank lines at its ends, which the model often adds. */
function snippetLines(old: string): string[] {
  const lines = old.replace(/\r\n/g, '\n').split('\n')
  while (lines.length > 1 && !lines[0]!.trim()) lines.shift()
  while (lines.length > 1 && !lines.at(-1)!.trim()) lines.pop()
  return lines
}

/** Line windows of `text` whose lines equal `old`'s once indentation is set aside. */
function looseMatches(text: string, old: string) {
  const want = snippetLines(old).map(squash)
  if (!want.some(Boolean)) return { lines: linesOf(text), found: [] as number[], size: 0 }
  const lines = linesOf(text)
  const have = lines.map((l) => squash(l.line))
  const found: number[] = []
  for (let i = 0; i + want.length <= have.length; i++) {
    let same = true
    for (let j = 0; j < want.length && same; j++) same = have[i + j] === want[j]
    if (same) {
      found.push(i)
      i += want.length - 1
    }
  }
  return { lines, found, size: want.length }
}

/** How alike two lines are, 0 to 1, by the pairs of characters they share. */
function likeness(a: string, b: string): number {
  if (a === b) return 1
  if (a.length < 2 || b.length < 2) return 0
  const pairs = new Map<string, number>()
  for (let i = 0; i < a.length - 1; i++) {
    const pair = a.slice(i, i + 2)
    pairs.set(pair, (pairs.get(pair) ?? 0) + 1)
  }
  let shared = 0
  for (let i = 0; i < b.length - 1; i++) {
    const pair = b.slice(i, i + 2)
    const left = pairs.get(pair) ?? 0
    if (left > 0) {
      shared++
      pairs.set(pair, left - 1)
    }
  }
  return (2 * shared) / (a.length + b.length - 2)
}

/** Lines further apart than this are not compared: a long file stays quick. */
const MAX_COMPARED = 400_000

/**
 * The lines of `text` most like `old`, with their numbers and a little around them, and
 * the first line that differs; `null` when nothing is much alike.
 */
export function nearest(text: string, old: string): string | null {
  const want = snippetLines(old).map(squash)
  const lines = linesOf(text)
  const have = lines.map((l) => squash(l.line))
  if (!want.length || !have.length) return null
  const size = Math.min(want.length, have.length)
  const windows = have.length - size + 1
  // Very long snippets in long files: compare a sample of the snippet's lines.
  const step = Math.max(1, Math.ceil((windows * size) / MAX_COMPARED))
  let best = { at: 0, score: -1 }
  for (let i = 0; i < windows; i++) {
    let score = 0
    let compared = 0
    for (let j = 0; j < size; j += step) {
      score += likeness(have[i + j]!, want[j]!)
      compared++
    }
    score /= compared
    if (score > best.score) best = { at: i, score }
  }
  if (best.score < 0.5) return null
  const from = Math.max(0, best.at - 2)
  const to = Math.min(lines.length, best.at + size + 2)
  const shown = lines
    .slice(from, to)
    .map((l, i) => `${String(from + i + 1).padStart(5)}\t${l.line}`)
    .join('\n')
  let differs = ''
  for (let j = 0; j < size; j++) {
    if (have[best.at + j] !== want[j]) {
      differs = `\nThe first line that differs is ${best.at + j + 1}: the file has\n${lines[best.at + j]!.line}\nwhere "old" has\n${snippetLines(old)[j]}`
      break
    }
  }
  return `The nearest text is at lines ${best.at + 1}-${best.at + size}:\n${shown}${differs}`
}

/**
 * Replace `old` with `replacement` in `text`: where it occurs as written, else where the
 * same lines occur with other whitespace (indentation, trailing spaces, line endings).
 * More than one place is refused unless `all`.
 */
export function applyEdit(
  text: string,
  old: string,
  replacement: string,
  all: boolean,
): EditOutcome {
  const count = text.split(old).length - 1
  if (count === 1 || (count > 1 && all)) {
    return { ok: true, text: text.split(old).join(replacement), count, loose: [] }
  }
  if (count > 1) {
    return {
      ok: false,
      reason: `"old" occurs ${count} times; include more of the lines around it to make it unique, or pass all`,
    }
  }
  const { lines, found, size } = looseMatches(text, old)
  if (found.length === 1 || (found.length > 1 && all)) {
    let out = ''
    let from = 0
    const loose = found.map((at) => ({ from: at + 1, to: at + size }))
    for (const at of found) {
      const start = lines[at]!.start
      const end = lines[at + size - 1]!.end
      // The snippet's own last line ending, if it had one, is the file's.
      out += text.slice(from, start) + replacement.replace(/\r?\n$/, '')
      from = end
    }
    out += text.slice(from)
    return { ok: true, text: out, count: found.length, loose }
  }
  if (found.length > 1) {
    return {
      ok: false,
      reason: `"old" occurs ${found.length} times once whitespace is set aside (at lines ${found.map((at) => at + 1).join(', ')}); include more of the lines around it, or pass all`,
    }
  }
  const near = nearest(text, old)
  return {
    ok: false,
    reason: `"old" does not occur${near ? `. ${near}` : ', and no lines are much like it; read the file again'}`,
  }
}
