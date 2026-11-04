/** Capabilities a context source can offer. Upper layers depend on these, never on a provider. */
export interface Capabilities {
  workspace: WorkspaceCapability
  repo: RepoCapability
  tracker: TrackerCapability
}

export type CapabilityName = keyof Capabilities

export interface ExecResult {
  exitCode: number
  stdout: string
  stderr: string
}

/** A writable directory on some machine, with files and commands inside it. */
export interface Workspace {
  id: string
  /** Shown to the user, e.g. a path or `host:path`. */
  label: string
  /** The branch the workspace works on, when it is a checkout of a repository. */
  branch: string | null
  /**
   * Paths are relative to the workspace root. Implementations refuse paths that leave it,
   * including through symbolic links, with a `WorkspacePathError`.
   */
  readFile(path: string): Promise<string>
  writeFile(path: string, content: string): Promise<void>
  list(path: string): Promise<{ name: string; kind: 'file' | 'directory' }[]>
  exec(
    command: string,
    options?: { cwd?: string; timeoutMs?: number; signal?: AbortSignal },
  ): Promise<ExecResult>
  dispose(): Promise<void>
}

export interface WorkspaceRequest {
  /** The thread the workspace is for; providers use it to name worktrees. */
  threadId: string
  /** Check out this branch or ref, when the source is a repository. */
  ref?: string
}

export interface WorkspaceCapability {
  /** Whether two threads can work at the same time without seeing each other's changes. */
  isolated: boolean
  open(request: WorkspaceRequest): Promise<Workspace>
}

export interface PullRequestInfo {
  number: number
  /**
   * How the host refers to it in text, e.g. `!12` for a GitLab merge request, where `#12`
   * would link an issue. `#<number>` when left out.
   */
  reference?: string
  url: string
  title: string
  state: 'open' | 'closed' | 'merged'
  draft: boolean
  /** The newest commit of the pull request's branch. */
  headSha: string
  base: string
}

/** One check of a commit, e.g. one CI job. */
export interface CheckInfo {
  id: string
  name: string
  status: 'queued' | 'in_progress' | 'completed'
  /** Set once completed: `success`, `failure`, `cancelled`, `skipped`, ... */
  conclusion: string | null
  url: string | null
}

/** A hosted repository: branches, pull requests and the checks that run on them. */
export interface RepoCapability {
  /** `owner/name`. */
  remote: string
  /** The repository's page, for links. */
  webUrl: string
  defaultBranch(): Promise<string>
  /** Whether anyone can read the repository: what is pushed there is public at once. */
  isPublic(): Promise<boolean>
  /**
   * Bring the remote's branches into the workspaces as `origin/<branch>`, without moving
   * any local branch.
   */
  fetch(): Promise<void>
  /**
   * Push a branch the workspaces created to the remote. Only the given branch moves;
   * providers refuse to force-push.
   */
  push(branch: string): Promise<{ sha: string }>
  /** The open (or newest) pull request whose head is `branch`. */
  pullRequestFor(branch: string): Promise<PullRequestInfo | null>
  pullRequest(number: number): Promise<PullRequestInfo>
  openPullRequest(input: {
    branch: string
    base?: string
    title: string
    body: string
    draft?: boolean
  }): Promise<PullRequestInfo>
  checks(sha: string): Promise<CheckInfo[]>
  /** The end of a check's log, for a thread to see why it failed. */
  checkLog(checkId: string): Promise<string>
}

/** An issue as a list shows it. Titles and bodies are written by anyone who can file one. */
export interface IssueSummary {
  number: number
  url: string
  title: string
  state: 'open' | 'closed'
  author: string | null
  labels: string[]
  comments: number
  updatedAt: string
}

export interface IssueComment {
  id: string
  author: string | null
  body: string
  createdAt: string
  url: string
}

export interface IssueDetail extends IssueSummary {
  body: string
  createdAt: string
  /** The newest comments, oldest first; `comments` says how many there are in all. */
  recent: IssueComment[]
}

/**
 * Where the project's issues live: reading them is context for the work. Changing one
 * reaches other people, so Ariadne asks the user before each change and calls these only
 * with their yes.
 */
export interface TrackerCapability {
  /** Names the tracker in links and records, e.g. `owner/name`. */
  name: string
  webUrl: string
  /** Newest updated first. Pull requests are not issues and are left out. */
  list(query: {
    state?: 'open' | 'closed' | 'all'
    labels?: string[]
    /** Words to look for in titles and bodies. */
    search?: string
    limit?: number
  }): Promise<IssueSummary[]>
  issue(number: number): Promise<IssueDetail>
  comment(number: number, body: string): Promise<IssueComment>
  setState(
    number: number,
    state: 'open' | 'closed',
    reason?: 'completed' | 'not_planned',
  ): Promise<IssueSummary>
}

/** A path that points outside the workspace. */
export class WorkspacePathError extends Error {
  constructor(readonly path: string) {
    super(`${path} is outside the workspace`)
    this.name = 'WorkspacePathError'
  }
}
