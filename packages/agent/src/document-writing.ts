import { applyEdit, nearest } from './edit-match'
import { isOpen, type OpenPath } from './partial-json'
import { ToolInputError } from './tool-helpers'

/**
 * A document as it reads while the model is still writing its change, so the change can
 * show where it lands as it comes: replacements one after another, or the whole text
 * written again from the top.
 */

/** Apply the model's replacements in order; any that does not find its text refuses all. */
export function applyDocumentEdits(text: string, edits: unknown): string {
  if (!Array.isArray(edits) || !edits.length) {
    throw new ToolInputError('"edits" must be a non-empty array')
  }
  let next = text
  for (const [index, raw] of edits.entries()) {
    const edit = (raw ?? {}) as { old?: unknown; new?: unknown }
    if (typeof edit.old !== 'string' || !edit.old) {
      throw new ToolInputError(`edits[${index}]: "old" must be a non-empty string`)
    }
    if (typeof edit.new !== 'string') {
      throw new ToolInputError(`edits[${index}]: "new" must be a string`)
    }
    const outcome = applyEdit(next, edit.old, edit.new, false)
    if (!outcome.ok) {
      const near = nearest(next, edit.old)
      throw new ToolInputError(
        `edits[${index}]: ${outcome.reason}${near ? `\n${near}` : ''}\nNothing was changed. Copy "old" from the document as read_artifact gives it.`,
      )
    }
    next = outcome.text
  }
  return next
}

/**
 * The document with the replacements written so far: the finished ones, and the one being
 * written once its `old` is whole, with as much of `new` as there is. Stops at the first
 * that does not apply; the call itself reports that when it runs.
 */
export function editsSoFar(base: string, edits: unknown, open: OpenPath | null): string {
  if (!Array.isArray(edits)) return base
  let next = base
  for (const [index, raw] of edits.entries()) {
    const edit = (raw ?? {}) as { old?: unknown; new?: unknown }
    if (typeof edit.old !== 'string' || !edit.old || typeof edit.new !== 'string') break
    // `old` still growing would match somewhere else with every piece.
    if (isOpen(open, 'edits', index, 'old')) break
    const outcome = applyEdit(next, edit.old, edit.new, false)
    if (!outcome.ok) break
    next = outcome.text
  }
  return next
}

/** A line with words in it is taken to be in one place; a rule or a brace is in many. */
const distinct = (line: string) => (line.match(/[\p{L}\p{N}]/gu)?.length ?? 0) >= 2

/** Beyond this many line pairs the alignment is not worth its time: the text shows whole. */
const MAX_ALIGNED = 2_000_000

/**
 * The document while the model writes all of it again, `written` being the text so far:
 * what is written, then the rest of `base` it has not come to yet. The lines of `base`
 * it has passed are found by matching lines in order, so a part it adds shows growing
 * where it goes, and a part it left out goes once it has written past it. `null` when
 * the two are too long to align.
 */
export function rewriteSoFar(base: string, written: string): string | null {
  const lines = written.split('\n')
  // The last line is still being written.
  const growing = lines.pop() ?? ''
  const before = base.split('\n')
  const passed = lastPassed(before, lines)
  if (passed === null) return null
  const rest = before.slice(passed + 1)
  // A line being copied as it was is already there, the first of the rest.
  const copied = !growing || !!rest.find((line) => line.trim())?.startsWith(growing)
  if (!copied) return [...lines, growing, ...rest].join('\n')
  // The blank lines written after the last text are the ones the rest begins with.
  let blank = 0
  while (blank < lines.length && !lines[lines.length - 1 - blank]!.trim()) blank++
  let skip = 0
  while (skip < blank && skip < rest.length && !rest[skip]!.trim()) skip++
  return [...lines, ...rest.slice(skip)].join('\n')
}

/**
 * The last line of `before` that `lines` has reached: the one the last line the two share,
 * in order, is. Blank lines are everywhere, so they do not count. -1 when none.
 */
function lastPassed(before: string[], lines: string[]): number | null {
  const a = before.flatMap((line, index) => (line.trim() ? [{ line, index }] : []))
  const b = lines.filter((line) => line.trim())
  if (!a.length || !b.length) return -1
  if (a.length * b.length > MAX_ALIGNED) return null
  // Longest common subsequence, from the ends: `width` cells a row, one row per line of `a`.
  const width = b.length + 1
  const table = new Uint32Array((a.length + 1) * width)
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      table[i * width + j] =
        a[i]!.line === b[j]
          ? table[(i + 1) * width + j + 1]! + 1
          : Math.max(table[(i + 1) * width + j]!, table[i * width + j + 1]!)
    }
  }
  const pairs: { i: number; j: number }[] = []
  for (let i = 0, j = 0; i < a.length && j < b.length;) {
    if (a[i]!.line === b[j]) pairs.push({ i: i++, j: j++ })
    else if (table[(i + 1) * width + j]! >= table[i * width + j + 1]!) i++
    else j++
  }
  // A line without words (a rule, a table's dashes) is in many places: one that jumps
  // ahead in `before` counts only when the line after it follows there too.
  let last = -1
  let at = -1
  for (const [index, pair] of pairs.entries()) {
    const next = pairs[index + 1]
    const follows = pair.i === at + 1
    const followed = !!next && next.i === pair.i + 1 && next.j === pair.j + 1
    if (!follows && !followed && !distinct(a[pair.i]!.line)) continue
    at = pair.i
    last = a[pair.i]!.index
  }
  return last
}
