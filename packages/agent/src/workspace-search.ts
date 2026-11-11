import type { SearchOptions, SourceView, Workspace } from '@ariadne/plugin-sdk'
import { globToRegExp, listFiles, searchByReading, walk } from '@ariadne/plugin-sdk'

export { execArgv, MAX_MATCH_LINES, searchLines } from '@ariadne/plugin-sdk'

/** Paths found at most, before the list says how many more there are. */
export const MAX_FOUND = 500

function matching(files: string[], pattern: string) {
  const match = globToRegExp(pattern)
  const found = files.filter((file) => match.test(file)).toSorted()
  return { found: found.slice(0, MAX_FOUND), total: found.length }
}

/**
 * The workspace's files matching `pattern` under `path`, sorted, without what git ignores
 * (see `listFiles`).
 */
export async function findFiles(
  workspace: Workspace,
  pattern: string,
  path: string,
  signal?: AbortSignal,
): Promise<{ found: string[]; total: number; partial: boolean }> {
  const { files, partial } = await listFiles(workspace, path, signal)
  return { ...matching(files, pattern), partial }
}

/**
 * The same over a source that is only read: by the view's own listing when it has one,
 * else directory by directory.
 */
export async function findViewFiles(
  view: SourceView,
  pattern: string,
  path: string,
  signal?: AbortSignal,
): Promise<{ found: string[]; total: number; partial: boolean }> {
  const listed = view.files
    ? await view.files(path, signal ? { signal } : {})
    : await walk(view, path, signal).then(({ files, more }) => ({ files, partial: more }))
  return { ...matching(listed.files, pattern), partial: listed.partial }
}

/** Lines of a source that is only read matching `pattern`, as `file:line:text`. */
export function searchView(
  view: SourceView,
  pattern: string,
  path: string,
  options: SearchOptions = {},
): Promise<{ lines: string[]; error: string | null }> {
  return view.search
    ? view.search(pattern, path, options)
    : searchByReading(view, pattern, path, options)
}
