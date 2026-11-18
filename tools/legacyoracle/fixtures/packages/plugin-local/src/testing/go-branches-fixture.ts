import { branchChanges, fileVersions, type GitRun } from '../branches'
import { run, serverGitEnv } from '../index'

const input = JSON.parse(await Bun.stdin.text()) as {
  root: string
  branch: string
  base: string
  baseRef: string
  worktree: string
  from: string
  head: string
}
const git: GitRun = (args, options = {}) =>
  run(['git', ...args], {
    cwd: options.cwd ?? input.root,
    env: { ...serverGitEnv(['file']), ...options.env },
    whole: options.whole,
  })
const changes = await branchChanges(git, input)
const versions: Record<string, Awaited<ReturnType<typeof fileVersions>>> = {}
for (const file of changes?.files ?? []) {
  versions[file.path] = await fileVersions(git, {
    from: input.from,
    head: input.head,
    path: file.path,
    previousPath: file.previousPath,
  })
}
await Bun.write(Bun.stdout, JSON.stringify({ changes, versions }))
