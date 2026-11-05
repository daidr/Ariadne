import type { Ariadne } from '@ariadne/core'
import type { SourceView } from '@ariadne/plugin-sdk'
import type { WorkspaceManager } from './workspaces'

/** Files offered after `@` at most, best matches first. */
export const MENTION_FILES = 30
/** How many files a walk of a directory without git collects at most. */
const MAX_WALKED = 20_000
/** Directories a walk does not go into: installed or built things, never what one points at. */
const SKIPPED = new Set(['node_modules', '.git', 'dist', 'build', 'target', '.venv', '__pycache__'])
/** A list is read again after this long, so new files show up. */
const FRESH_MS = 30_000

const cache = new Map<string, { at: number; files: Promise<string[]> }>()

/** Every file of a directory, through its listing, bounded and without installed things. */
export async function walkFiles(view: Pick<SourceView, 'list'>, max = MAX_WALKED) {
  const files: string[] = []
  const queue = ['']
  while (queue.length && files.length < max) {
    const dir = queue.shift()!
    let entries: Awaited<ReturnType<SourceView['list']>>
    try {
      entries = await view.list(dir || '.')
    } catch {
      continue
    }
    for (const entry of entries) {
      const path = dir ? `${dir}/${entry.name}` : entry.name
      if (entry.kind === 'directory') {
        if (!SKIPPED.has(entry.name)) queue.push(path)
      } else files.push(path)
    }
  }
  return files.slice(0, max)
}

/**
 * The files a conversation can point at: those of a thread's workspace once it has one
 * (git's own list there, ignored files left out), else those of the project's source.
 * Opening a workspace just to list it is avoided, so a thread without one lists the source.
 */
async function filesOf(
  app: Ariadne,
  workspaces: WorkspaceManager,
  projectId: string,
  threadId: string | null,
): Promise<string[]> {
  const known = threadId ? app.getThread(threadId).workspace : null
  if (threadId && known && !known.removed && workspaces.available(projectId)) {
    const workspace = await workspaces.forThread(projectId, threadId)
    if (workspace.branch) {
      const argv = ['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard']
      const shell = workspaces.shell(projectId).argv
      // Without a shell where the workspace can, so it reads the same on every platform.
      const result = workspace.execFile
        ? await workspace.execFile(argv, { timeoutMs: 15_000 })
        : await workspace.exec(argv.join(' '), {
            timeoutMs: 15_000,
            ...(shell ? { shell: [...shell] } : {}),
          })
      if (result.exitCode === 0) {
        return [...new Set(result.stdout.split('\0').filter(Boolean))].slice(0, MAX_WALKED)
      }
    }
    return walkFiles(workspace)
  }
  return walkFiles(await workspaces.view(projectId))
}

/** How well `path` answers `query`, lower is better; `null` when it does not contain it. */
export function mentionRank(path: string, query: string): number | null {
  const lower = path.toLowerCase()
  const q = query.toLowerCase()
  const name = lower.slice(lower.lastIndexOf('/') + 1)
  if (!q) return lower.split('/').length
  if (name.startsWith(q)) return 0
  if (name.includes(q)) return 1
  if (lower.includes(q)) return 2
  return null
}

/** The files offered for `query` after `@`, best first. */
export async function mentionFiles(
  app: Ariadne,
  workspaces: WorkspaceManager,
  projectId: string,
  threadId: string | null,
  query: string,
): Promise<string[]> {
  const key = `${projectId}\0${threadId ?? ''}`
  // Lists gone stale go, so threads long done do not keep theirs.
  for (const [other, { at }] of cache) if (Date.now() - at > FRESH_MS) cache.delete(other)
  let entry = cache.get(key)
  if (!entry || Date.now() - entry.at > FRESH_MS) {
    entry = { at: Date.now(), files: filesOf(app, workspaces, projectId, threadId) }
    cache.set(key, entry)
    // A list that failed is not kept.
    entry.files.catch(() => cache.delete(key))
  }
  const files = await entry.files
  return files
    .flatMap((path) => {
      const rank = mentionRank(path, query)
      return rank === null ? [] : [{ path, rank }]
    })
    .toSorted((a, b) => a.rank - b.rank || a.path.length - b.path.length)
    .slice(0, MENTION_FILES)
    .map((item) => item.path)
}
