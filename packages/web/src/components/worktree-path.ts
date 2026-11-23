/**
 * Split a project's worktree paths into a shared parent directory and per-item relative paths,
 * so narrow lists show the distinguishing tail instead of repeating the same long prefix.
 */
export interface SplitWorktreePaths {
  commonDir: string | null
  relative: string[]
}

interface ParsedPath {
  root: string
  sep: '/' | '\\'
  parts: string[]
  drive: boolean
}

function parsePath(raw: string): ParsedPath {
  const sep: '/' | '\\' = raw.includes('\\') && !raw.includes('/') ? '\\' : '/'
  const unc = sep === '\\' && raw.startsWith('\\\\')
  let trimmed = raw
  if (unc) {
    trimmed = trimmed.slice(2)
  }
  const posixRoot = !unc && sep === '/' && trimmed.startsWith('/')
  if (posixRoot) {
    trimmed = trimmed.slice(1)
  }
  const parts = trimmed.split(sep).filter(Boolean)
  const drive = !unc && !posixRoot && parts.length > 0 && /^[A-Za-z]:$/.test(parts[0]!)
  const root = unc ? '\\\\' : posixRoot ? '/' : ''
  return { root, sep, parts, drive }
}

export function splitWorktreePaths(paths: readonly string[]): SplitWorktreePaths {
  if (paths.length === 0) return { commonDir: null, relative: [] }

  const parsed = paths.map(parsePath)
  const first = parsed[0]!
  // Every entry must keep at least its own leaf directory name.
  if (parsed.some((item) => item.parts.length < 2)) {
    return { commonDir: null, relative: [...paths] }
  }
  if (parsed.some((item) => item.root !== first.root || item.sep !== first.sep)) {
    return { commonDir: null, relative: [...paths] }
  }

  let commonLen = first.parts.length - 1
  for (let i = 1; i < parsed.length; i++) {
    const parts = parsed[i]!.parts
    const limit = Math.min(commonLen, parts.length - 1)
    let matched = 0
    while (matched < limit && parts[matched] === first.parts[matched]) {
      matched++
    }
    commonLen = matched
  }

  // Avoid emitting bare roots like `/` or `C:` or `\\server` as a directory header.
  const minCommonLen = first.drive || first.root === '\\\\' ? 2 : 1
  if (commonLen < minCommonLen) {
    return { commonDir: null, relative: [...paths] }
  }

  const commonDir = `${first.root}${first.parts.slice(0, commonLen).join(first.sep)}`
  const relative = parsed.map((item) => item.parts.slice(commonLen).join(item.sep))
  return { commonDir, relative }
}
