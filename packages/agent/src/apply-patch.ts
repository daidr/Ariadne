/**
 * The patch format GPT-family models are trained to edit files with (Codex's apply_patch):
 *
 *   *** Begin Patch
 *   *** Add File: path        then the file's lines, each starting with +
 *   *** Delete File: path
 *   *** Update File: path     optionally *** Move to: other/path, then hunks:
 *   @@ a line to find first   (optional) then lines starting with " ", - or +
 *   *** End of File           (optional) the hunk ends the file
 *   *** End Patch
 *
 * Parsed leniently, as models write it: a heredoc around it, a missing Begin or End line,
 * an empty line inside a hunk standing for an empty context line.
 */

export type PatchOp =
  | { kind: 'add'; path: string; text: string }
  | { kind: 'delete'; path: string }
  | { kind: 'update'; path: string; moveTo: string | null; hunks: Hunk[] }

export interface Hunk {
  /** The text after @@: a line to find before the hunk's lines. */
  anchor: string
  lines: { op: ' ' | '-' | '+'; text: string }[]
  /** The hunk ends the file. */
  atEnd: boolean
}

export class PatchError extends Error {}

const FILE_HEADER = /^\*\*\* (Add|Delete|Update) File: (.+)$/

export function parsePatch(input: string): PatchOp[] {
  let lines = input.replace(/\r\n/g, '\n').split('\n')
  // A heredoc the model wrapped it in: apply_patch <<'EOF' … EOF.
  if (/^\s*(apply_patch|applypatch)\b.*<</.test(lines[0] ?? '')) {
    lines = lines.slice(1)
    const end = lines.findLastIndex((line) => line.trim())
    if (end !== -1 && /^['"]?[A-Z_]+['"]?$/.test(lines[end]!.trim())) lines = lines.slice(0, end)
  }
  const begin = lines.findIndex((line) => line.trim() === '*** Begin Patch')
  if (begin !== -1) lines = lines.slice(begin + 1)
  const end = lines.findIndex((line) => line.trim() === '*** End Patch')
  if (end !== -1) lines = lines.slice(0, end)

  const ops: PatchOp[] = []
  let at = 0
  const skipBlank = () => {
    while (at < lines.length && !lines[at]!.trim()) at++
  }
  skipBlank()
  while (at < lines.length) {
    const header = FILE_HEADER.exec(lines[at]!.trim())
    if (!header) {
      throw new PatchError(
        `line ${at + 1}: expected "*** Add File:", "*** Update File:" or "*** Delete File:", found "${lines[at]!.slice(0, 80)}"`,
      )
    }
    const path = header[2]!.trim()
    at++
    if (header[1] === 'Delete') {
      ops.push({ kind: 'delete', path })
    } else if (header[1] === 'Add') {
      const added: string[] = []
      while (at < lines.length && !FILE_HEADER.test(lines[at]!.trim())) {
        const line = lines[at]!
        if (line.startsWith('+')) added.push(line.slice(1))
        else if (!line.trim()) added.push('')
        else throw new PatchError(`line ${at + 1}: every line of an added file starts with +`)
        at++
      }
      while (added.length && added.at(-1) === '') added.pop()
      ops.push({ kind: 'add', path, text: added.length ? `${added.join('\n')}\n` : '' })
    } else {
      let moveTo: string | null = null
      const move = /^\*\*\* Move to: (.+)$/.exec(lines[at]?.trim() ?? '')
      if (move) {
        moveTo = move[1]!.trim()
        at++
      }
      const hunks: Hunk[] = []
      let hunk: Hunk | null = null
      while (at < lines.length && !FILE_HEADER.test(lines[at]!.trim())) {
        const line = lines[at]!
        if (line.startsWith('@@')) {
          hunk = { anchor: line.slice(2).trim(), lines: [], atEnd: false }
          hunks.push(hunk)
        } else if (line.trim() === '*** End of File') {
          if (hunk) hunk.atEnd = true
        } else {
          if (!hunk) {
            hunk = { anchor: '', lines: [], atEnd: false }
            hunks.push(hunk)
          }
          const op = line[0]
          if (op === ' ' || op === '-' || op === '+') hunk.lines.push({ op, text: line.slice(1) })
          else if (!line.length) hunk.lines.push({ op: ' ', text: '' })
          else {
            throw new PatchError(
              `line ${at + 1}: a line of a hunk starts with " ", "-" or "+", found "${line.slice(0, 80)}"`,
            )
          }
        }
        at++
      }
      // Empty lines between a file's hunks and the next header are no context.
      for (const each of hunks) {
        while (each.lines.length && each.lines.at(-1)!.op === ' ' && !each.lines.at(-1)!.text) {
          each.lines.pop()
        }
      }
      const real = hunks.filter((each) => each.lines.length)
      if (!real.length && !moveTo) throw new PatchError(`${path}: an update with no changes`)
      ops.push({ kind: 'update', path, moveTo, hunks: real })
    }
    skipBlank()
  }
  if (!ops.length) throw new PatchError('the patch changes no file')
  return ops
}

/** Ways a line of the patch may match one of the file, tried in order. */
const LOOSENESS: ((line: string) => string)[] = [
  (line) => line,
  (line) => line.trimEnd(),
  (line) => line.trim(),
  // Typographic quotes and dashes a model wrote for plain ones, or the other way round.
  (line) =>
    line
      .trim()
      .replace(/[‘’‚‛]/g, "'")
      .replace(/[“”„‟]/g, '"')
      .replace(/[‐‑‒–—―]/g, '-'),
]

/** Where `wanted` occurs in `lines` from `from` on, as loosely as it takes; -1 when not. */
function seek(lines: string[], wanted: string[], from: number, atEnd: boolean): number {
  if (!wanted.length) return from
  for (const loose of LOOSENESS) {
    const target = wanted.map(loose)
    const matchesAt = (start: number) =>
      target.every((line, i) => loose(lines[start + i]!) === line)
    if (atEnd) {
      const start = lines.length - wanted.length
      if (start >= from && matchesAt(start)) return start
    }
    for (let start = from; start + wanted.length <= lines.length; start++) {
      if (matchesAt(start)) return start
    }
  }
  return -1
}

/** `text` with the hunks applied, or why one does not fit. */
export function applyHunks(text: string, hunks: Hunk[], path: string): string {
  const crlf = text.includes('\r\n')
  const endsWithNewline = text.endsWith('\n') || !text
  const lines = text.replace(/\r\n/g, '\n').split('\n')
  if (endsWithNewline) lines.pop()
  let cursor = 0
  for (const [index, hunk] of hunks.entries()) {
    const which = hunks.length > 1 ? `hunk ${index + 1} of ${path}` : path
    if (hunk.anchor) {
      const found = seek(lines, [hunk.anchor], cursor, false)
      if (found === -1)
        throw new PatchError(`${which}: the @@ line "${hunk.anchor}" is not in the file`)
      cursor = found + 1
    }
    const old = hunk.lines.filter((line) => line.op !== '+').map((line) => line.text)
    const replacement = hunk.lines.filter((line) => line.op !== '-').map((line) => line.text)
    let start: number
    if (!old.length) {
      // Only additions: after the @@ line, or at the end of the file.
      start = hunk.anchor && !hunk.atEnd ? cursor : lines.length
    } else {
      start = seek(lines, old, cursor, hunk.atEnd)
      if (start === -1) {
        throw new PatchError(
          `${which}: these lines are not in the file${cursor ? ' after the previous hunk' : ''}:\n${old.slice(0, 6).join('\n')}${old.length > 6 ? '\n…' : ''}`,
        )
      }
    }
    lines.splice(start, old.length, ...replacement)
    cursor = start + replacement.length
  }
  const joined = lines.join(crlf ? '\r\n' : '\n')
  return lines.length && endsWithNewline ? `${joined}${crlf ? '\r\n' : '\n'}` : joined
}

/** Whether a model takes apply_patch better than edit_file: the GPT and Codex families. */
export const editsByPatch = (model: string) =>
  /(^|[/:.\s])(gpt-|o[1-9](-|$)|codex)/i.test(model) || /codex/i.test(model)
