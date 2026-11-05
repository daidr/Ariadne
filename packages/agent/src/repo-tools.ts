import {
  confirmBlock,
  DomainError,
  type Ariadne,
  type ChecksState,
  type ThreadPullRequest,
} from '@ariadne/core'
import type { CheckInfo, PullRequestInfo, RepoCapability } from '@ariadne/plugin-sdk'
import { createTranslator } from '@ariadne/i18n'
import {
  json,
  optionalStr,
  str,
  type Tool,
  type ToolContext,
  type ToolResult,
} from './tool-helpers'

/** Conclusions that mean a check did not pass. */
const FAILING = new Set([
  'failure',
  'timed_out',
  'cancelled',
  'action_required',
  'error',
  'startup_failure',
])

/** All checks of a commit as one state, with the names of those that failed. */
export function summarizeChecks(checks: CheckInfo[]): ThreadPullRequest['checks'] {
  const failed = checks
    .filter((c) => c.status === 'completed' && FAILING.has(c.conclusion ?? ''))
    .map((c) => c.name)
  let state: ChecksState = 'success'
  if (!checks.length) state = 'none'
  else if (failed.length) state = 'failure'
  else if (checks.some((c) => c.status !== 'completed')) state = 'pending'
  return { state, failed }
}

/** How the host refers to a pull request in text: `#12`, or `!12` on GitLab. */
export const pullRequestRef = (pr: { number: number; reference?: string }) =>
  pr.reference ?? `#${pr.number}`

/** Read the pull request and its checks from the repository and record them. */
export async function syncThreadPullRequest(
  app: Ariadne,
  repo: RepoCapability,
  threadId: string,
  known?: PullRequestInfo,
  /** The autopilot just merged it, as this says. */
  auto?: Parameters<Ariadne['syncPullRequest']>[2],
): Promise<ThreadPullRequest | null> {
  const thread = app.getThread(threadId)
  const pr =
    known ??
    (thread.pullRequest
      ? await repo.pullRequest(thread.pullRequest.number)
      : thread.workspace?.branch
        ? await repo.pullRequestFor(thread.workspace.branch)
        : null)
  if (!pr) return null
  const synced: ThreadPullRequest = {
    number: pr.number,
    ...(pr.reference ? { reference: pr.reference } : {}),
    url: pr.url,
    title: pr.title,
    state: pr.state,
    headSha: pr.headSha,
    checks: summarizeChecks(await repo.checks(pr.headSha)),
  }
  app.syncPullRequest(threadId, synced, auto)
  return synced
}

async function branchOf(ctx: ToolContext): Promise<string> {
  const workspace = await ctx.workspace()
  if (!workspace.branch) throw new DomainError('error.noRepo')
  return workspace.branch
}

const repoStatus: Tool = {
  readOnly: true,
  spec: {
    name: 'repo_status',
    description:
      'Your branch on the repository: its pull request, and every check of the newest commit with its id, so you can read a failed one with read_check_log.',
    parameters: { type: 'object', properties: {} },
  },
  async run(_args, ctx) {
    const repo = await ctx.repo()
    const branch = await branchOf(ctx)
    const pr = await syncThreadPullRequest(ctx.app, repo, ctx.conversationId)
    return json({
      repository: repo.remote,
      branch,
      defaultBranch: await repo.defaultBranch(),
      pullRequest: pr && {
        number: pr.number,
        reference: pullRequestRef(pr),
        url: pr.url,
        state: pr.state,
        headSha: pr.headSha,
      },
      checks: pr ? await repo.checks(pr.headSha) : [],
    })
  },
}

/**
 * The standing approval needed before pushing to a public repository, per repository: a
 * project that switches to another public repository asks again.
 */
export const PUSH_PUBLIC = 'push-public'

export function pushPublicPurpose(remote: string): string {
  return `${PUSH_PUBLIC}:${remote}`
}

/**
 * Pushing to a public repository publishes at once and cannot be taken back, so the user
 * is asked once per project before the first push there; threads asking at the same time
 * share one card. Returns a result to hand back to the model instead of pushing, or
 * `null` when the push may go ahead.
 */
async function pushBlocked(ctx: ToolContext, repo: RepoCapability): Promise<ToolResult | null> {
  if (!(await repo.isPublic())) return null
  const t = createTranslator(ctx.locale)
  const state = ctx.app.requestApproval(
    ctx.projectId,
    ctx.conversationId,
    pushPublicPurpose(repo.remote),
    () =>
      confirmBlock({
        title: t('repo.pushPublic.question', { repo: repo.remote }),
        description: t('repo.pushPublic.pushConsequence'),
        tone: 'warning',
        approveText: t('repo.pushPublic.push'),
        denyText: t('repo.pushPublic.keep'),
      }),
  )
  if (state === 'approved') return null
  if (state === 'declined') {
    return {
      content:
        'The user declined pushing to this public repository. Do not push; keep your work committed on your branch and report "ready" with what is there.',
      isError: true,
    }
  }
  return {
    content:
      state === 'asked'
        ? 'This repository is public: what you push is published at once. The user was asked whether pushing is allowed; their answer will arrive as a new turn. Call this tool again only if they agree. If they answer in words without choosing, do what they say; you may call it again later, which asks again.'
        : 'This repository is public, and the user is already being asked whether pushing to it is allowed. Their answer will arrive as a new turn; do not push until then.',
  }
}

const syncBranch: Tool = {
  spec: {
    name: 'sync_branch',
    description:
      "Fetch the repository's newest commits, then tell how your branch stands against the default branch and against your branch on the remote. It moves nothing: merge origin/<default> or origin/<your branch> yourself with run_command, resolve conflicts, commit, and push again.",
    parameters: { type: 'object', properties: {} },
  },
  async run(_args, ctx) {
    const repo = await ctx.repo()
    const workspace = await ctx.workspace()
    const branch = await branchOf(ctx)
    await repo.fetch()
    const defaultBranch = await repo.defaultBranch()
    const count = async (range: string) => {
      const result = await workspace.exec(`git rev-list --left-right --count ${range}`)
      if (result.exitCode !== 0) return null
      const [ahead, behind] = result.stdout.trim().split(/\s+/).map(Number)
      return { ahead: ahead ?? 0, behind: behind ?? 0 }
    }
    const pr = ctx.app.getThread(ctx.conversationId).pullRequest
    return json({
      branch,
      defaultBranch: {
        name: `origin/${defaultBranch}`,
        ...(await count(`HEAD...origin/${defaultBranch}`)),
      },
      remoteBranch: await count(`HEAD...origin/${branch}`),
      hint:
        pr?.state === 'merged'
          ? `Your pull request ${pullRequestRef(pr)} was merged (squashed). Before new work, merge origin/${defaultBranch} so the next pull request holds only the new changes.`
          : undefined,
    })
  },
}

const pushBranch: Tool = {
  spec: {
    name: 'push_branch',
    description:
      "Push your branch to the repository. Only your own branch moves, and never by force: if it changed on the remote, the push fails and you merge the remote's changes first. Commit before you push.",
    parameters: { type: 'object', properties: {} },
  },
  async run(_args, ctx) {
    const repo = await ctx.repo()
    const branch = await branchOf(ctx)
    const blocked = await pushBlocked(ctx, repo)
    if (blocked) return blocked
    const { sha } = await repo.push(branch)
    return json({ branch, sha })
  },
}

const openPullRequest: Tool = {
  spec: {
    name: 'open_pull_request',
    description:
      "Push your branch and open a pull request into the default branch, or return the one that is already open. Write the title and description in the user's language: what changed and why, and how it was checked. You never merge; the user does. Its checks are watched for you: when they fail you are woken to fix them.",
    parameters: {
      type: 'object',
      properties: {
        title: { type: 'string' },
        body: { type: 'string', description: 'Markdown.' },
        draft: { type: 'boolean', description: 'Open as a draft, e.g. while the work goes on.' },
      },
      required: ['title', 'body'],
    },
  },
  async run(args, ctx) {
    const repo = await ctx.repo()
    const branch = await branchOf(ctx)
    const existing = await repo.pullRequestFor(branch)
    if (existing?.state === 'closed') {
      // Closing a pull request is usually the user saying no; a new one would undo that.
      return {
        content: `Your pull request ${pullRequestRef(existing)} was closed without merging, most likely by the user. Do not open another on your own: ask the user (post_ui) whether they want it reopened, or report "blocked" with why.`,
        isError: true,
      }
    }
    const blocked = await pushBlocked(ctx, repo)
    if (blocked) return blocked
    await repo.push(branch)
    const pr =
      existing?.state === 'open'
        ? existing
        : await repo.openPullRequest({
            branch,
            title: str(args, 'title'),
            body: optionalStr(args, 'body') ?? '',
            draft: args.draft === true,
          })
    await syncThreadPullRequest(ctx.app, repo, ctx.conversationId, pr)
    return json({
      number: pr.number,
      reference: pullRequestRef(pr),
      url: pr.url,
      state: pr.state,
      alreadyOpen: existing?.state === 'open',
    })
  },
}

const readCheckLog: Tool = {
  readOnly: true,
  spec: {
    name: 'read_check_log',
    description: "The end of a check's log, by the id repo_status lists, to see why it failed.",
    parameters: {
      type: 'object',
      properties: { checkId: { type: 'string' } },
      required: ['checkId'],
    },
  },
  async run(args, ctx) {
    const repo = await ctx.repo()
    return { content: (await repo.checkLog(str(args, 'checkId'))) || '(empty log)' }
  },
}

/** Repository failures (no access, a refused push) go back to the model to act on. */
function guarded(tool: Tool): Tool {
  return {
    spec: tool.spec,
    ...(tool.readOnly ? { readOnly: true } : {}),
    async run(args, ctx) {
      try {
        return await tool.run(args, ctx)
      } catch (error) {
        if (error instanceof DomainError) throw error
        return { content: error instanceof Error ? error.message : String(error), isError: true }
      }
    },
  }
}

export const repoTools: Tool[] = [
  repoStatus,
  syncBranch,
  pushBranch,
  openPullRequest,
  readCheckLog,
].map(guarded)
