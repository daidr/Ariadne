import { DomainError, type Ariadne, type ChecksState, type ThreadPullRequest } from '@ariadne/core'
import type { CheckInfo, PullRequestInfo, RepoCapability } from '@ariadne/plugin-sdk'
import { json, optionalStr, str, type Tool, type ToolContext } from './tool-helpers'

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

/** Read the pull request and its checks from the repository and record them. */
export async function syncThreadPullRequest(
  app: Ariadne,
  repo: RepoCapability,
  threadId: string,
  known?: PullRequestInfo,
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
    url: pr.url,
    title: pr.title,
    state: pr.state,
    headSha: pr.headSha,
    checks: summarizeChecks(await repo.checks(pr.headSha)),
  }
  app.syncPullRequest(threadId, synced)
  return synced
}

async function branchOf(ctx: ToolContext): Promise<string> {
  const workspace = await ctx.workspace()
  if (!workspace.branch) throw new DomainError('error.noRepo')
  return workspace.branch
}

const repoStatus: Tool = {
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
      pullRequest: pr && { number: pr.number, url: pr.url, state: pr.state, headSha: pr.headSha },
      checks: pr ? await repo.checks(pr.headSha) : [],
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
    await repo.push(branch)
    const existing = await repo.pullRequestFor(branch)
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
      url: pr.url,
      state: pr.state,
      alreadyOpen: existing?.state === 'open',
    })
  },
}

const readCheckLog: Tool = {
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

export const repoTools: Tool[] = [repoStatus, pushBranch, openPullRequest, readCheckLog].map(
  guarded,
)
