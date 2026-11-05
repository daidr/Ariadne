import type {
  CheckInfo,
  IssueComment,
  IssueDetail,
  IssueSummary,
  MergeMethod,
  PullRequestInfo,
  PullRequestMergeOutcome,
  PullRequestPreview,
} from '@ariadne/plugin-sdk'

/** A failed GitLab request, with GitLab's own message. */
export class GitLabError extends Error {
  constructor(
    readonly status: number,
    message: string,
    /** GitLab's own words, without the request. */
    readonly reason = message,
  ) {
    super(message)
    this.name = 'GitLabError'
  }
}

export interface RawProject {
  id: number
  default_branch: string | null
  web_url: string
  path_with_namespace: string
  /** Left out by some instances for anonymous reads; only public projects are readable then. */
  visibility?: 'public' | 'internal' | 'private'
}

interface RawMergeRequest {
  iid: number
  web_url: string
  title: string
  state: 'opened' | 'closed' | 'locked' | 'merged'
  draft?: boolean
  work_in_progress?: boolean
  sha: string | null
  diff_refs?: { head_sha: string | null } | null
  source_branch: string
  source_project_id: number
  target_branch: string
  created_at: string
}

interface RawStatus {
  id: number
  name: string
  status:
    | 'created'
    | 'waiting_for_resource'
    | 'preparing'
    | 'pending'
    | 'running'
    | 'success'
    | 'failed'
    | 'canceled'
    | 'canceling'
    | 'skipped'
    | 'manual'
    | 'scheduled'
  allow_failure?: boolean
  target_url: string | null
}

interface RawIssue {
  iid: number
  web_url: string
  title: string
  state: 'opened' | 'closed'
  author: { username: string } | null
  labels: (string | { name?: string })[]
  user_notes_count: number
  description: string | null
  created_at: string
  updated_at: string
}

interface RawNote {
  id: number
  author: { username: string } | null
  body: string | null
  created_at: string
  /** Notes GitLab writes itself: "changed the description", "added label …". */
  system: boolean
}

/** Comments read with an issue: the newest, which say where it stands now. */
const RECENT_COMMENTS = 20
/** Notes fetched to find them: system notes are mixed in and left out. */
const NOTES_PAGE = 100

/** Log text kept for a thread: the end of a log, where the failure usually is. */
const MAX_LOG = 20_000

function toIssue(raw: RawIssue): IssueSummary {
  return {
    number: raw.iid,
    url: raw.web_url,
    title: raw.title,
    state: raw.state === 'opened' ? 'open' : 'closed',
    author: raw.author?.username ?? null,
    labels: raw.labels
      .map((label) => (typeof label === 'string' ? label : (label.name ?? '')))
      .filter(Boolean),
    comments: raw.user_notes_count,
    updatedAt: raw.updated_at,
  }
}

function toComment(raw: RawNote, issueUrl: string): IssueComment {
  return {
    id: String(raw.id),
    author: raw.author?.username ?? null,
    body: raw.body ?? '',
    createdAt: raw.created_at,
    url: `${issueUrl}#note_${raw.id}`,
  }
}

export function toMergeRequest(raw: RawMergeRequest): PullRequestInfo {
  return {
    number: raw.iid,
    // `#12` is an issue on GitLab; merge requests are `!12`.
    reference: `!${raw.iid}`,
    url: raw.web_url,
    title: raw.title,
    // `locked` is a merge request in the middle of being merged.
    state: raw.state === 'merged' ? 'merged' : raw.state === 'closed' ? 'closed' : 'open',
    draft: !!(raw.draft ?? raw.work_in_progress),
    headSha: raw.sha ?? raw.diff_refs?.head_sha ?? '',
    base: raw.target_branch,
  }
}

/** A commit status as a check: GitLab's job states folded into queued, running and done. */
export function toCheck(raw: RawStatus): CheckInfo {
  const base = { id: String(raw.id), name: raw.name, url: raw.target_url }
  switch (raw.status) {
    case 'running':
    case 'canceling':
      return { ...base, status: 'in_progress', conclusion: null }
    case 'success':
      return { ...base, status: 'completed', conclusion: 'success' }
    case 'failed':
      // A job allowed to fail does not fail the pipeline.
      return { ...base, status: 'completed', conclusion: raw.allow_failure ? 'neutral' : 'failure' }
    case 'canceled':
      return { ...base, status: 'completed', conclusion: 'cancelled' }
    case 'skipped':
    case 'manual':
      // A manual job waits for someone to start it; nothing a thread pushes changes that.
      return { ...base, status: 'completed', conclusion: 'skipped' }
    default:
      return { ...base, status: 'queued', conclusion: null }
  }
}

/** A job log without terminal colours and GitLab's collapsible-section markers. */
export function plainLog(text: string): string {
  return (
    text
      // oxlint-disable-next-line no-control-regex
      .replace(/section_(?:start|end):\d+:[^\r\n]*?\r?\x1b\[0K/g, '')
      // oxlint-disable-next-line no-control-regex
      .replace(/\x1b\[[0-9;?]*[A-Za-z]/g, '')
      .replace(/\r(?!\n)/g, '\n')
  )
}

/** The few REST (v4) endpoints the repo and tracker capabilities need. */
export class GitLabApi {
  constructor(
    private readonly options: {
      /** e.g. `https://gitlab.com/api/v4`. */
      base: string
      /** The project's full path, e.g. `group/sub/project`. */
      path: string
      /** The project's page, e.g. `https://gitlab.com/group/sub/project`. */
      webUrl: string
      token: () => Promise<string | null>
      fetch: typeof fetch
    },
  ) {}

  private get projectPath() {
    return `/projects/${encodeURIComponent(this.options.path)}`
  }

  async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const response = await this.raw(method, path, body)
    return (await response.json()) as T
  }

  private async raw(method: string, path: string, body?: unknown): Promise<Response> {
    const token = await this.options.token()
    const headers: Record<string, string> = { accept: 'application/json', 'user-agent': 'ariadne' }
    // Bearer takes personal, project, group and OAuth tokens alike.
    if (token) headers.authorization = `Bearer ${token}`
    if (body !== undefined) headers['content-type'] = 'application/json'
    const response = await this.options.fetch(`${this.options.base}${path}`, {
      method,
      headers,
      // A redirect could take the token to another host; it is reported instead.
      redirect: 'manual',
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    })
    if (response.status >= 300 && response.status < 400) {
      throw new GitLabError(
        response.status,
        `GitLab ${method} ${path}: ${response.status} moved to ${response.headers.get('location') ?? 'another address'}; check the instance and repository settings`,
      )
    }
    if (!response.ok) {
      const text = await response.text()
      let message = text
      try {
        const parsed = JSON.parse(text) as {
          message?: unknown
          error?: string
          error_description?: string
        }
        const what = parsed.message ?? parsed.error_description ?? parsed.error
        if (what !== undefined) message = typeof what === 'string' ? what : JSON.stringify(what)
      } catch {
        // Not JSON; keep the text.
      }
      throw new GitLabError(
        response.status,
        `GitLab ${method} ${path}: ${response.status} ${message}`.trim(),
        message.trim() || String(response.status),
      )
    }
    return response
  }

  project() {
    return this.request<RawProject>('GET', this.projectPath)
  }

  /** The open (else newest) merge request from `branch` of this project, not of a fork. */
  async mergeRequestFor(branch: string, projectId: number): Promise<PullRequestInfo | null> {
    const params = new URLSearchParams({
      source_branch: branch,
      state: 'all',
      order_by: 'created_at',
      sort: 'desc',
      per_page: '20',
    })
    const found = await this.request<RawMergeRequest[]>(
      'GET',
      `${this.projectPath}/merge_requests?${params}`,
    )
    const ranked = found
      .filter((mr) => mr.source_branch === branch && mr.source_project_id === projectId)
      .toSorted(
        (a, b) =>
          Number(b.state === 'opened') - Number(a.state === 'opened') ||
          b.created_at.localeCompare(a.created_at),
      )
    return ranked[0] ? toMergeRequest(ranked[0]) : null
  }

  /** A merge request with what the card over a link to it shows. */
  async mergeRequestPreview(iid: number): Promise<PullRequestPreview> {
    const raw = await this.request<
      RawMergeRequest & {
        author: { username: string } | null
        /** A number as text, or `1000+` past that many files. */
        changes_count: string | null
        merged_at: string | null
        closed_at: string | null
      }
    >('GET', `${this.projectPath}/merge_requests/${iid}`)
    const info = toMergeRequest(raw)
    const files = /^\d+$/.test(raw.changes_count ?? '') ? Number(raw.changes_count) : null
    return {
      kind: 'pull-request',
      repository: this.options.path,
      number: raw.iid,
      reference: `!${raw.iid}`,
      url: raw.web_url,
      title: raw.title,
      state: info.state,
      draft: info.draft,
      author: raw.author?.username ?? null,
      // GitLab does not count lines for a merge request.
      additions: null,
      deletions: null,
      changedFiles: files,
      at:
        (info.state === 'merged'
          ? raw.merged_at
          : info.state === 'closed'
            ? raw.closed_at
            : null) ?? raw.created_at,
    }
  }

  async mergeRequest(iid: number): Promise<PullRequestInfo> {
    return toMergeRequest(
      await this.request<RawMergeRequest>('GET', `${this.projectPath}/merge_requests/${iid}`),
    )
  }

  async openMergeRequest(input: {
    branch: string
    base: string
    title: string
    body: string
    draft: boolean
  }): Promise<PullRequestInfo> {
    // GitLab marks drafts by the title.
    const title =
      input.draft && !/^draft:/i.test(input.title) ? `Draft: ${input.title}` : input.title
    return toMergeRequest(
      await this.request<RawMergeRequest>('POST', `${this.projectPath}/merge_requests`, {
        source_branch: input.branch,
        target_branch: input.base,
        title,
        description: input.body,
      }),
    )
  }

  /**
   * Merge the merge request while its source branch is at `sha`; `squash` as the method
   * says. GitLab answers 409 when the branch moved, 406 when it cannot be merged (conflicts
   * among the reasons) and 405 when it may not be (a draft, an unfinished pipeline).
   */
  async mergeMergeRequest(
    iid: number,
    input: { method: MergeMethod; sha: string },
  ): Promise<PullRequestMergeOutcome> {
    try {
      const merged = await this.request<{
        merge_commit_sha?: string | null
        squash_commit_sha?: string | null
      }>('PUT', `${this.projectPath}/merge_requests/${iid}/merge`, {
        sha: input.sha,
        squash: input.method === 'squash',
      })
      return { kind: 'merged', sha: merged.merge_commit_sha ?? merged.squash_commit_sha ?? null }
    } catch (error) {
      if (!(error instanceof GitLabError)) throw error
      if (error.status === 409) return { kind: 'moved' }
      if (![400, 405, 406, 422].includes(error.status)) throw error
      const mr = await this.request<{ has_conflicts?: boolean }>(
        'GET',
        `${this.projectPath}/merge_requests/${iid}`,
      )
      if (mr.has_conflicts) return { kind: 'conflict' }
      return { kind: 'refused', detail: error.reason }
    }
  }

  /**
   * The commit's statuses: the jobs of its pipelines and whatever outside CI reported,
   * the newest of each name.
   */
  async checks(sha: string): Promise<CheckInfo[]> {
    const raw = await this.request<RawStatus[]>(
      'GET',
      `${this.projectPath}/repository/commits/${encodeURIComponent(sha)}/statuses?per_page=100`,
    )
    const newest = new Map<string, RawStatus>()
    for (const status of raw) {
      const seen = newest.get(status.name)
      if (!seen || status.id > seen.id) newest.set(status.name, status)
    }
    return [...newest.values()].toSorted((a, b) => a.id - b.id).map(toCheck)
  }

  /** The end of a job's log. Statuses outside CI have no log here. */
  async checkLog(checkId: string): Promise<string> {
    if (!/^\d+$/.test(checkId)) throw new GitLabError(400, `no check ${checkId}`)
    let text: string
    try {
      text = await (await this.raw('GET', `${this.projectPath}/jobs/${checkId}/trace`)).text()
    } catch (error) {
      if (!(error instanceof GitLabError) || error.status !== 404) throw error
      return 'This check was reported from outside GitLab CI; it has no log here. Open its link for details.'
    }
    const plain = plainLog(text)
    return plain.length > MAX_LOG
      ? `[… ${plain.length - MAX_LOG} characters cut …]\n${plain.slice(-MAX_LOG)}`
      : plain
  }

  /** Issues, newest updated first. Search stays within the project by the endpoint itself. */
  async issues(query: {
    state?: 'open' | 'closed' | 'all'
    labels?: string[]
    search?: string
    limit?: number
  }): Promise<IssueSummary[]> {
    const limit = Math.min(Math.max(query.limit ?? 30, 1), 100)
    const params = new URLSearchParams({
      order_by: 'updated_at',
      sort: 'desc',
      per_page: String(limit),
    })
    const state = query.state ?? 'open'
    if (state !== 'all') params.set('state', state === 'open' ? 'opened' : 'closed')
    if (query.labels?.length) params.set('labels', query.labels.join(','))
    if (query.search?.trim()) params.set('search', query.search.trim())
    const raw = await this.request<RawIssue[]>('GET', `${this.projectPath}/issues?${params}`)
    return raw.map(toIssue)
  }

  async issue(iid: number): Promise<IssueDetail> {
    const raw = await this.request<RawIssue>('GET', `${this.projectPath}/issues/${iid}`)
    const notes = raw.user_notes_count
      ? await this.request<RawNote[]>(
          'GET',
          `${this.projectPath}/issues/${iid}/notes?sort=desc&order_by=created_at&per_page=${NOTES_PAGE}`,
        )
      : []
    const recent = notes
      .filter((note) => !note.system)
      .slice(0, RECENT_COMMENTS)
      .toReversed()
    return {
      ...toIssue(raw),
      body: raw.description ?? '',
      createdAt: raw.created_at,
      recent: recent.map((note) => toComment(note, this.issueUrl(iid))),
    }
  }

  async comment(iid: number, body: string): Promise<IssueComment> {
    return toComment(
      await this.request<RawNote>('POST', `${this.projectPath}/issues/${iid}/notes`, { body }),
      this.issueUrl(iid),
    )
  }

  private issueUrl(iid: number) {
    return `${this.options.webUrl}/-/issues/${iid}`
  }

  /** GitLab has no close reason; an issue is closed or reopened as it is. */
  async setIssueState(iid: number, state: 'open' | 'closed'): Promise<IssueSummary> {
    return toIssue(
      await this.request<RawIssue>('PUT', `${this.projectPath}/issues/${iid}`, {
        state_event: state === 'closed' ? 'close' : 'reopen',
      }),
    )
  }
}
