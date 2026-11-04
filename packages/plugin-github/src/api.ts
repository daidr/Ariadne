import type {
  CheckInfo,
  IssueComment,
  IssueDetail,
  IssueSummary,
  PullRequestInfo,
  PullRequestPreview,
} from '@ariadne/plugin-sdk'

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

interface RawIssue {
  number: number
  html_url: string
  title: string
  state: 'open' | 'closed'
  user: { login: string } | null
  labels: (string | { name?: string })[]
  comments: number
  body: string | null
  created_at: string
  updated_at: string
  pull_request?: unknown
  repository_url?: string
}

interface RawComment {
  id: number
  user: { login: string } | null
  body: string | null
  created_at: string
  html_url: string
}

/** Comments read with an issue: the newest, which say where it stands now. */
const RECENT_COMMENTS = 20

function toIssue(raw: RawIssue): IssueSummary {
  return {
    number: raw.number,
    url: raw.html_url,
    title: raw.title,
    state: raw.state,
    author: raw.user?.login ?? null,
    labels: raw.labels
      .map((label) => (typeof label === 'string' ? label : (label.name ?? '')))
      .filter(Boolean),
    comments: raw.comments,
    updatedAt: raw.updated_at,
  }
}

function toComment(raw: RawComment): IssueComment {
  return {
    id: String(raw.id),
    author: raw.user?.login ?? null,
    body: raw.body ?? '',
    createdAt: raw.created_at,
    url: raw.html_url,
  }
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
    return this.request<{
      default_branch: string
      html_url: string
      clone_url: string
      private: boolean
    }>('GET', this.repoPath)
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

  /** A pull request with what the card over a link to it shows. */
  async pullRequestPreview(number: number): Promise<PullRequestPreview> {
    const raw = await this.request<
      RawPull & {
        user: { login: string } | null
        additions: number
        deletions: number
        changed_files: number
        closed_at: string | null
      }
    >('GET', `${this.repoPath}/pulls/${number}`)
    const info = toPullRequest(raw)
    return {
      kind: 'pull-request',
      repository: `${this.options.owner}/${this.options.name}`,
      number: raw.number,
      url: raw.html_url,
      title: raw.title,
      state: info.state,
      draft: info.draft,
      author: raw.user?.login ?? null,
      additions: raw.additions,
      deletions: raw.deletions,
      changedFiles: raw.changed_files,
      at: raw.merged_at ?? raw.closed_at ?? raw.created_at,
    }
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

  /** Issues, newest updated first. The issues endpoint also lists pull requests; not here. */
  async issues(query: {
    state?: 'open' | 'closed' | 'all'
    labels?: string[]
    search?: string
    limit?: number
  }): Promise<IssueSummary[]> {
    const limit = Math.min(Math.max(query.limit ?? 30, 1), 100)
    const state = query.state ?? 'open'
    if (query.search?.trim()) {
      // Only words: a qualifier (`repo:`, `org:`, `is:` …) could reach other repositories
      // the token can read, so they are dropped, and so are the boolean operators.
      const words = query.search
        .split(/\s+/)
        .filter((word) => word && !/^-?[\w.-]*:/.test(word) && !/^(?:AND|OR|NOT)$/.test(word))
        .map((word) => word.replace(/^-+/, ''))
        .filter(Boolean)
      const terms = [
        ...words,
        `repo:${this.options.owner}/${this.options.name}`,
        'is:issue',
        ...(state === 'all' ? [] : [`is:${state}`]),
        ...(query.labels ?? []).map((label) => `label:"${label.replace(/"/g, '')}"`),
      ]
      const found = await this.request<{ items: RawIssue[] }>(
        'GET',
        `/search/issues?q=${encodeURIComponent(terms.join(' '))}&sort=updated&per_page=${limit}`,
      )
      // And whatever search returns, only this repository's issues are this project's.
      const own = `/repos/${this.options.owner}/${this.options.name}`.toLowerCase()
      return found.items
        .filter((item) => !item.pull_request && item.repository_url?.toLowerCase().endsWith(own))
        .map(toIssue)
    }
    const params = new URLSearchParams({
      state,
      sort: 'updated',
      direction: 'desc',
      per_page: String(limit),
    })
    if (query.labels?.length) params.set('labels', query.labels.join(','))
    const raw = await this.request<RawIssue[]>('GET', `${this.repoPath}/issues?${params}`)
    return raw.filter((item) => !item.pull_request).map(toIssue)
  }

  async issue(number: number): Promise<IssueDetail> {
    const raw = await this.request<RawIssue>('GET', `${this.repoPath}/issues/${number}`)
    if (raw.pull_request) throw new GitHubError(404, `#${number} is a pull request, not an issue`)
    // The last page holds the newest comments; when it is short, the one before fills up.
    const pages = Math.max(1, Math.ceil(raw.comments / RECENT_COMMENTS))
    const page = (n: number) =>
      this.request<RawComment[]>(
        'GET',
        `${this.repoPath}/issues/${number}/comments?per_page=${RECENT_COMMENTS}&page=${n}`,
      )
    let comments = raw.comments ? await page(pages) : []
    if (pages > 1 && comments.length < RECENT_COMMENTS) {
      comments = [...(await page(pages - 1)), ...comments].slice(-RECENT_COMMENTS)
    }
    return {
      ...toIssue(raw),
      body: raw.body ?? '',
      createdAt: raw.created_at,
      recent: comments.map(toComment),
    }
  }

  async comment(number: number, body: string): Promise<IssueComment> {
    return toComment(
      await this.request<RawComment>('POST', `${this.repoPath}/issues/${number}/comments`, {
        body,
      }),
    )
  }

  async setIssueState(
    number: number,
    state: 'open' | 'closed',
    reason?: 'completed' | 'not_planned',
  ): Promise<IssueSummary> {
    return toIssue(
      await this.request<RawIssue>('PATCH', `${this.repoPath}/issues/${number}`, {
        state,
        ...(state === 'closed' && reason ? { state_reason: reason } : {}),
        ...(state === 'open' ? { state_reason: 'reopened' } : {}),
      }),
    )
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
