import type {
  Capabilities,
  GitObjects,
  RepoCapability,
  WorkspaceCapability,
} from '@ariadne/plugin-sdk'

/**
 * `host/path` of a git URL (`https://host/a/b.git`, `ssh://git@host:22/a/b`, `git@host:a/b`),
 * lower-cased and without `.git`; `null` when it is not one.
 */
export function repositoryOf(url: string): string | null {
  const text = url.trim()
  let host: string
  let path: string
  const scp = /^(?:[^@/]+@)?([^:/]+):(?!\/)(.+)$/.exec(text)
  if (scp && !/^[a-z][a-z0-9+.-]*:\/\//i.test(text)) {
    host = scp[1]!
    path = scp[2]!
  } else {
    try {
      const parsed = new URL(text)
      host = parsed.hostname
      path = parsed.pathname
    } catch {
      return null
    }
  }
  path = path.replace(/^\/+|\/+$/g, '').replace(/\.git$/i, '')
  if (!host || !path) return null
  return `${host}/${path}`.toLowerCase()
}

/**
 * Hosted repository `repo` with the workspaces of a checkout on another machine: threads
 * work there, the commits come through Ariadne. Ariadne fetches into its own clone and
 * sends the remote branches on to the checkout as `origin/*`; a push takes the thread's
 * branch from the checkout into the clone and pushes it from here, so the credentials
 * stay on this machine. Only a checkout whose `origin` is the hosted repository is taken,
 * so no other repository's commits are pushed there.
 */
export async function withHosting(
  source: Partial<Capabilities>,
  hosting: Partial<Capabilities>,
): Promise<Partial<Capabilities>> {
  const checkout = source.workspace?.checkout
  if (!checkout) throw new Error('the directory is not a git checkout')
  const repo = hosting.repo
  if (!repo?.objects) throw new Error('this source cannot be used as the hosting')
  const origin = await checkout.originUrl()
  const expected = repositoryOf(repo.webUrl)
  if (!origin || repositoryOf(origin) !== expected) {
    throw new Error(
      origin
        ? `the checkout's origin is ${origin}, not ${repo.webUrl}`
        : `the checkout has no origin; it must be a clone of ${repo.webUrl}`,
    )
  }
  const sync = new Sync(checkout.objects, repo)
  const tracked = checkout.tracking()
  const workspace: WorkspaceCapability = {
    ...tracked,
    async open(request) {
      // A branch the thread already has is opened as it is; a new one starts from the
      // newest default branch.
      if (request.base !== undefined && !request.ref) return tracked.open(request)
      await sync.down()
      const base = request.base ?? (await repo.defaultBranch())
      return tracked.open({ ...request, base, ref: request.ref ?? `refs/remotes/origin/${base}` })
    },
  }
  const hostedRepo: RepoCapability = {
    ...repo,
    fetch: () => sync.down(),
    async push(branch) {
      await sync.up(branch)
      const pushed = await repo.push(branch)
      // The checkout's `origin/<branch>` follows, as a push from there would move it.
      await checkout.objects.receive(null, { [`refs/remotes/origin/${branch}`]: pushed.sha })
      return pushed
    },
  }
  return { workspace, repo: hostedRepo, ...(hosting.tracker ? { tracker: hosting.tracker } : {}) }
}

const ORIGIN = 'refs/remotes/origin/'

/** Moves commits between the remote checkout and the hosted repository's clone, one at a time. */
class Sync {
  private queue: Promise<unknown> = Promise.resolve()

  constructor(
    private readonly checkout: GitObjects,
    private readonly repo: RepoCapability & { objects?: GitObjects },
  ) {}

  private serial<T>(work: () => Promise<T>): Promise<T> {
    const next = this.queue.then(work, work)
    this.queue = next.catch(() => undefined)
    return next
  }

  /**
   * Fetch, then give the checkout the remote's branches as `origin/*`; the `origin/*` of
   * branches deleted on the remote go, as a fetch with `--prune` there would remove them.
   */
  down(): Promise<void> {
    return this.serial(async () => {
      const clone = this.repo.objects!
      await this.repo.fetch()
      const refs: Record<string, string | null> = await clone.refs([ORIGIN])
      const tips = [...new Set(Object.values(refs) as string[])]
      const present = new Set(await this.checkout.has(tips))
      const missing = Object.keys(refs).filter((ref) => !present.has(refs[ref]!))
      const theirs = await this.checkout.refs([ORIGIN, 'refs/heads/'])
      for (const ref of Object.keys(theirs)) {
        // `origin/HEAD` points at another ref; deleting it would delete that one.
        if (ref.startsWith(ORIGIN) && ref !== `${ORIGIN}HEAD` && !(ref in refs)) refs[ref] = null
      }
      let bundle: Uint8Array | null = null
      if (missing.length) {
        const shared = await clone.has([...new Set(Object.values(theirs))])
        bundle = await clone.bundle(missing, shared)
      }
      await this.checkout.receive(bundle, refs)
    })
  }

  /** Bring the checkout's branch into the clone, for a push from there. */
  up(branch: string): Promise<void> {
    return this.serial(async () => {
      const clone = this.repo.objects!
      const ref = `refs/heads/${branch}`
      const sha = (await this.checkout.refs([ref]))[ref]
      if (!sha) throw new Error(`there is no branch ${branch} to push`)
      let bundle: Uint8Array | null = null
      if (!(await clone.has([sha])).length) {
        const ours = Object.values(await clone.refs([ORIGIN, 'refs/heads/']))
        const shared = await this.checkout.has([...new Set(ours)])
        bundle = await this.checkout.bundle([ref], shared)
      }
      await clone.receive(bundle, { [ref]: sha })
    })
  }
}
