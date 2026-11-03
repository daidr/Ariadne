/** Capabilities a context source can offer. Upper layers depend on these, never on a provider. */
export interface Capabilities {
  workspace: WorkspaceCapability
  repo: RepoCapability
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

/** A path that points outside the workspace. */
export class WorkspacePathError extends Error {
  constructor(readonly path: string) {
    super(`${path} is outside the workspace`)
    this.name = 'WorkspacePathError'
  }
}
