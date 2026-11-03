import type { CheckInfo, PullRequestInfo } from '@ariadne/plugin-sdk'

/** A failed GitHub request, with GitHub's own message. */
export class GitHubError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message)
    this.name = 'GitHubError'
  }
}

interface RawPull {
  number: number
  html_url: string
  title: string
  state: 'open' | 'closed'
  merged_at: string | null
  draft?: boolean
  head: { sha: string; ref: string }
  base: { ref: string }
  created_at: string
}

interface RawCheckRun {
  id: number
  name: string
  status: 'queued' | 'in_progress' | 'completed' | 'waiting' | 'requested' | 'pending'
  conclusion: string | null
  html_url: string | null
}

interface RawStatus {
  context: string
  state: 'error' | 'failure' | 'pending' | 'success'
  target_url: string | null
}

/** Log text kept for a thread: the end of a log, where the failure usually is. */
const MAX_LOG = 20_000

export function toPullRequest(raw: RawPull): PullRequestInfo {
  return {
    number: raw.number,
    url: raw.html_url,
    title: raw.title,
    state: raw.merged_at ? 'merged' : raw.state,
    draft: !!raw.draft,
    headSha: raw.head.sha,
    base: raw.base.ref,
  }
}

/** The few REST endpoints the repo capability needs. */
export class GitHubApi {
  constructor(
    private readonly options: {
      base: string
      owner: string
      name: string
      token: () => Promise<string | null>
      fetch: typeof fetch
    },
  ) {}

  private get repoPath() {
    return `/repos/${this.options.owner}/${this.options.name}`
  }

  async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const response = await this.raw(method, path, body)
    return (await response.json()) as T
  }

  private async raw(method: string, path: string, body?: unknown): Promise<Response> {
    const token = await this.options.token()
    const headers: Record<string, string> = {
      accept: 'application/vnd.github+json',
      'x-github-api-version': '2022-11-28',
      'user-agent': 'ariadne',
    }
    if (token) headers.authorization = `Bearer ${token}`
    if (body !== undefined) headers['content-type'] = 'application/json'
    const response = await this.options.fetch(`${this.options.base}${path}`, {
      method,
      headers,
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    })
    if (!response.ok) {
      const text = await response.text()
      let message = text
      try {
        const parsed = JSON.parse(text) as { message?: string; errors?: { message?: string }[] }
        message = [parsed.message, ...(parsed.errors ?? []).map((e) => e.message)]
          .filter(Boolean)
          .join(': ')
      } catch {
        // Not JSON; keep the text.
      }
      throw new GitHubError(
        response.status,
        `GitHub ${method} ${path}: ${response.status} ${message}`.trim(),
      )
    }
    return response
  }

  repository() {
    return this.request<{ default_branch: string; html_url: string; clone_url: string }>(
      'GET',
      this.repoPath,
    )
  }

  async pullRequestFor(branch: string): Promise<PullRequestInfo | null> {
    const head = encodeURIComponent(`${this.options.owner}:${branch}`)
    const pulls = await this.request<RawPull[]>(
      'GET',
      `${this.repoPath}/pulls?head=${head}&state=all&per_page=10`,
    )
    const ranked = pulls
      .filter((p) => p.head.ref === branch)
      .toSorted(
        (a, b) =>
          Number(b.state === 'open') - Number(a.state === 'open') ||
          b.created_at.localeCompare(a.created_at),
      )
    return ranked[0] ? toPullRequest(ranked[0]) : null
  }

  async pullRequest(number: number): Promise<PullRequestInfo> {
    return toPullRequest(await this.request<RawPull>('GET', `${this.repoPath}/pulls/${number}`))
  }

  async openPullRequest(input: {
    branch: string
    base: string
    title: string
    body: string
    draft: boolean
  }): Promise<PullRequestInfo> {
    const raw = await this.request<RawPull>('POST', `${this.repoPath}/pulls`, {
      head: input.branch,
      base: input.base,
      title: input.title,
      body: input.body,
      draft: input.draft,
    })
    return toPullRequest(raw)
  }

  /** Check runs and commit statuses together: CI may report either way. */
  async checks(sha: string): Promise<CheckInfo[]> {
    const [runs, combined] = await Promise.all([
      this.request<{ check_runs: RawCheckRun[] }>(
        'GET',
        `${this.repoPath}/commits/${sha}/check-runs?per_page=100`,
      ),
      this.request<{ statuses: RawStatus[] }>('GET', `${this.repoPath}/commits/${sha}/status`),
    ])
    const checks: CheckInfo[] = runs.check_runs.map((run) => ({
      id: String(run.id),
      name: run.name,
      status:
        run.status === 'completed'
          ? 'completed'
          : run.status === 'in_progress'
            ? 'in_progress'
            : 'queued',
      conclusion: run.status === 'completed' ? run.conclusion : null,
      url: run.html_url,
    }))
    for (const status of combined.statuses) {
      checks.push({
        id: `status:${status.context}`,
        name: status.context,
        status: status.state === 'pending' ? 'in_progress' : 'completed',
        conclusion: status.state === 'pending' ? null : status.state,
        url: status.target_url,
      })
    }
    return checks
  }

  /**
   * The end of a check's log. Actions jobs have a downloadable log under the same id;
   * other checks only have the summary they reported.
   */
  async checkLog(checkId: string): Promise<string> {
    if (checkId.startsWith('status:')) {
      return 'This check is a commit status; it has no log here. Open its link for details.'
    }
    if (!/^\d+$/.test(checkId)) throw new GitHubError(400, `no check ${checkId}`)
    try {
      const response = await this.raw('GET', `${this.repoPath}/actions/jobs/${checkId}/logs`)
      const text = await response.text()
      return text.length > MAX_LOG
        ? `[… ${text.length - MAX_LOG} characters cut …]\n${text.slice(-MAX_LOG)}`
        : text
    } catch (error) {
      if (!(error instanceof GitHubError) || error.status !== 404) throw error
    }
    const run = await this.request<{
      output: { title: string | null; summary: string | null; text: string | null }
    }>('GET', `${this.repoPath}/check-runs/${checkId}`)
    return [run.output.title, run.output.summary, run.output.text].filter(Boolean).join('\n\n')
  }
}
