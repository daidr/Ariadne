/** Lines compared at most on either side; a longer text is too costly to compare. */
const MAX_LINES = 4000
/** The changes are cut at about this many characters. */
export const MAX_DIFF_CHARS = 4000

/**
 * What changed between two texts, line by line: removed lines start with `-`, added
 * ones with `+`, and one unchanged line around each change starts with a space; `…`
 * stands for unchanged lines left out. `null` when nothing changed or the texts are too
 * long to compare; cut, with a last `…`, when the change is longer than `maxChars`.
 */
export function lineDiff(before: string, after: string, maxChars = MAX_DIFF_CHARS): string | null {
  if (before === after) return null
  const a = before.split('\n')
  const b = after.split('\n')
  // Lines alike at both ends are left out of the table.
  let start = 0
  while (start < a.length && start < b.length && a[start] === b[start]) start++
  let endA = a.length
  let endB = b.length
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--
    endB--
  }
  const midA = a.slice(start, endA)
  const midB = b.slice(start, endB)
  if (midA.length > MAX_LINES || midB.length > MAX_LINES) return null
  if (midA.length * midB.length > 4_000_000) return null

  // Longest common subsequence of the middle, read back as edits.
  const n = midA.length
  const m = midB.length
  const table = new Uint32Array((n + 1) * (m + 1))
  const at = (i: number, j: number) => i * (m + 1) + j
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      table[at(i, j)] =
        midA[i] === midB[j]
          ? table[at(i + 1, j + 1)]! + 1
          : Math.max(table[at(i + 1, j)]!, table[at(i, j + 1)]!)
    }
  }
  type Line = { mark: ' ' | '-' | '+'; text: string }
  const lines: Line[] = []
  if (start > 0) lines.push({ mark: ' ', text: a[start - 1]! })
  let i = 0
  let j = 0
  while (i < n || j < m) {
    if (i < n && j < m && midA[i] === midB[j]) {
      lines.push({ mark: ' ', text: midA[i]! })
      i++
      j++
    } else if (i < n && (j === m || table[at(i + 1, j)]! >= table[at(i, j + 1)]!)) {
      lines.push({ mark: '-', text: midA[i]! })
      i++
    } else {
      lines.push({ mark: '+', text: midB[j]! })
      j++
    }
  }
  if (endA < a.length) lines.push({ mark: ' ', text: a[endA]! })

  // Keep one unchanged line on each side of a change.
  const keep = lines.map(
    (line, index) =>
      line.mark !== ' ' ||
      lines[index - 1]?.mark === '-' ||
      lines[index - 1]?.mark === '+' ||
      lines[index + 1]?.mark === '-' ||
      lines[index + 1]?.mark === '+',
  )
  const out: string[] = []
  if (start > 1) out.push('…')
  let length = 0
  for (const [index, line] of lines.entries()) {
    if (!keep[index]) {
      if (out.at(-1) !== '…') out.push('…')
      continue
    }
    const text = `${line.mark}${line.text}`
    if (length + text.length > maxChars) {
      out.push('…')
      return out.join('\n')
    }
    length += text.length + 1
    out.push(text)
  }
  if (endA + 1 < a.length && out.at(-1) !== '…') out.push('…')
  return out.join('\n')
}
