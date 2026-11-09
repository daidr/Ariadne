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
   * The branch `branch` started from and is compared and merged against, e.g. `main`;
   * `null` when there is none (not a repository, or the checkout was on no branch).
   */
  base: string | null
  /**
   * On the machine Ariadne runs on: what a command serves on `localhost` there is reached
   * from the user's browser.
   */
  local?: boolean
  /**
   * Paths are relative to the workspace root. Implementations refuse paths that leave it,
   * including through symbolic links, with a `WorkspacePathError`.
   */
  readFile(path: string): Promise<string>
  /**
   * A file's bytes, for files that are not text (images). Larger than `maxBytes` is
   * refused with a `FileTooLargeError`, without reading it all.
   */
  readBytes(path: string, maxBytes: number): Promise<Uint8Array>
  writeFile(path: string, content: string): Promise<void>
  list(path: string): Promise<{ name: string; kind: 'file' | 'directory' }[]>
  exec(command: string, options?: ExecOptions): Promise<ExecResult>
  /**
   * Run a program with its arguments, without a shell in between, e.g. `['git', 'status']`:
   * the same on every platform, whatever shell the machine has. A program that is not
   * there resolves with exit code 127. Without it, callers quote `argv` for `bash -c`.
   */
  execFile?(argv: string[], options?: Omit<ExecOptions, 'shell'>): Promise<ExecResult>
  dispose(): Promise<void>
}

export interface ExecOptions {
  cwd?: string
  timeoutMs?: number
  signal?: AbortSignal
  /**
   * The program and its arguments the command line is handed to, e.g. `['zsh', '-c']`;
   * the machine's own when left out: `bash` on Linux and macOS, PowerShell on Windows.
   */
  shell?: readonly string[]
  /** Added to the command's environment. */
  env?: Record<string, string>
  /**
   * Called with output as it comes, before the call resolves: what a command left running
   * in the background has printed so far.
   */
  onOutput?: (chunk: string, stream: 'stdout' | 'stderr') => void
}

/**
 * The source as it is, to look at without changing it: what the coordinator reads to
 * know the project before it briefs threads. Paths are relative to its root and kept
 * inside it, as a workspace's are; git's own directory is not shown.
 */
export interface SourceView {
  /**
   * What is shown when it is not the directory as it is now, e.g. `origin/main` for the
   * default branch of a hosted repository as last fetched.
   */
  ref?: string
  /** Larger than `maxBytes` is refused with a `FileTooLargeError`, without reading it all. */
  readBytes(path: string, maxBytes: number): Promise<Uint8Array>
  list(path: string): Promise<{ name: string; kind: 'file' | 'directory' }[]>
  /**
   * Every file under `path`, relative to the root, without what git ignores: what finding
   * files by name goes through. `partial` when the listing stopped short. A view without
   * it is listed directory by directory, which is slow on another machine.
   */
  files?(
    path: string,
    options?: { signal?: AbortSignal },
  ): Promise<{ files: string[]; partial: boolean }>
  /**
   * Lines under `path` matching the regular expression `pattern`, as `file:line:text`;
   * `error` when the pattern or the search failed. A view without it is searched by
   * reading file after file.
   */
  search?(
    pattern: string,
    path: string,
    options?: { signal?: AbortSignal },
  ): Promise<{ lines: string[]; error: string | null }>
}

/**
 * Only the reading half of `files`, so a view handed out cannot write or run anything
 * whatever object stands behind it.
 */
export function readOnlyView(
  files: Pick<SourceView, 'readBytes' | 'list' | 'files' | 'search'>,
  ref?: string,
): SourceView {
  return {
    ...(ref ? { ref } : {}),
    readBytes: async (path, maxBytes) => files.readBytes(path, maxBytes),
    list: async (path) => files.list(path),
    ...(files.files ? { files: async (path, options) => files.files!(path, options) } : {}),
    ...(files.search
      ? { search: async (pattern, path, options) => files.search!(pattern, path, options) }
      : {}),
  }
}

export interface WorkspaceRequest {
  /** The thread the workspace is for; providers use it to name worktrees. */
  threadId: string
  /** Check out this branch or ref, when the source is a repository. */
  ref?: string
  /**
   * The name of the thread's own branch, made when it does not exist yet; providers pick
   * one from the thread id when left out.
   */
  branch?: string
  /**
   * The base recorded when the workspace was first opened. A workspace opened again (after
   * a restart, or once its worktree was removed) keeps it rather than taking whatever the
   * checkout is on now.
   */
  base?: string | null
}

export interface WorkspaceCapability {
  /** Whether two threads can work at the same time without seeing each other's changes. */
  isolated: boolean
  open(request: WorkspaceRequest): Promise<Workspace>
  /** The source itself, read-only; without it the coordinator cannot look at the files. */
  view?(): Promise<SourceView>
  /**
   * Nothing to work in, only to look at: a single file rather than a directory. A project
   * reads it as a reference and cannot take it as the source its threads work in.
   */
  viewOnly?: true
  /** Threads' branches and worktrees, when workspaces are worktrees of a git repository. */
  branches?: BranchCapability
  /**
   * The checkout the workspaces are worktrees of, when a hosted repository can come with
   * them: its pull requests and checks, with the commits moved through Ariadne.
   */
  checkout?: HostedCheckout
}

/**
 * A git repository's refs and objects, to move commits between two clones of the same
 * repository without either reaching the other.
 */
export interface GitObjects {
  /** The refs under these prefixes (e.g. `refs/remotes/origin/`), name to commit. */
  refs(prefixes: string[]): Promise<Record<string, string>>
  /** Those of `shas` the repository has. */
  has(shas: string[]): Promise<string[]>
  /**
   * A git bundle of the commits the refs `tips` reach and the commits `exclude` do not;
   * `exclude` are commits the receiving side has.
   */
  bundle(tips: string[], exclude: string[]): Promise<Uint8Array>
  /**
   * Take in a bundle's objects, when there is one, then set the refs to these commits; a
   * ref given `null` is deleted.
   */
  receive(bundle: Uint8Array | null, refs: Record<string, string | null>): Promise<void>
}

/** A checkout on some machine that a hosted repository's clone works with. */
export interface HostedCheckout {
  /** Where its `origin` points, to check it is the hosted repository. */
  originUrl(): Promise<string | null>
  objects: GitObjects
  /**
   * The same workspaces, with new branches started from and compared against
   * `origin/<base>`, and no merge: the branches reach the base through pull requests.
   */
  tracking(): WorkspaceCapability
}

export type ChangedFileStatus = 'added' | 'modified' | 'deleted' | 'renamed' | 'copied' | 'type'

/** One file a branch changed against its base. */
export interface ChangedFile {
  path: string
  /** Where a renamed or copied file came from. */
  previousPath: string | null
  status: ChangedFileStatus
  /** Lines added and removed; `null` for a binary file. */
  added: number | null
  removed: number | null
}

/** What a thread's branch changed, from where it left its base to its newest commit. */
export interface BranchChanges {
  branch: string
  base: string
  /** The commit the branch is compared against: the newest one it shares with the base. */
  from: string
  /** The branch's newest commit. */
  head: string
  /** Commits on the branch the base does not have, newest first (at most 100). */
  commits: { sha: string; subject: string }[]
  files: ChangedFile[]
  /** Files changed in the worktree and not committed: not part of the changes or a merge. */
  uncommitted: string[]
}

/** A file before and after: `null` where it does not exist. */
export interface FileVersions {
  before: string | null
  after: string | null
  /** Not text, or too large to show: the contents are left out. */
  skipped: 'binary' | 'large' | null
}

export type MergeMethod = 'squash' | 'merge'

export type MergeOutcome =
  /** The base now ends with `sha`, which takes in the branch up to `head`. */
  | { kind: 'merged'; sha: string; head: string }
  /** Nothing merged: these files changed on both sides. */
  | { kind: 'conflict'; files: string[] }
  /** The base already has everything the branch has. */
  | { kind: 'empty' }

/** A thread's worktree on disk. */
export interface WorktreeInfo {
  threadId: string
  path: string
  /** Space it takes, when it could be measured. */
  bytes: number | null
}

/** A thread and the branch its workspace was opened on, as recorded then. */
export interface ThreadBranch {
  threadId: string
  branch: string
}

/**
 * Threads' branches in the repository their worktrees belong to. Branches stay when a
 * worktree is removed: they hold the work, and opening the workspace again checks one out.
 */
export interface BranchCapability {
  /** `null` when the thread has no branch yet. */
  changes(thread: ThreadBranch, base: string): Promise<BranchChanges | null>
  /** A changed file at `from` (as `previousPath` when renamed) and at `head`. */
  file(input: {
    from: string
    head: string
    path: string
    previousPath: string | null
  }): Promise<FileVersions>
  /** Whether the base already has the branch's newest commit, e.g. merged by hand. */
  merged(thread: ThreadBranch, base: string): Promise<boolean>
  /**
   * Bring the branch into the base without touching the thread's worktree. Left out where
   * branches reach the base another way, e.g. through pull requests.
   */
  merge?(input: {
    threadId: string
    branch: string
    base: string
    method: MergeMethod
    message: string
  }): Promise<MergeOutcome>
  /**
   * Take back a merge made with `merge`, which left `base` at `sha`: the base goes back to
   * where it was before. Refused (`MergeRefused`) once commits came after it, it was pushed,
   * or another branch than `branch` (the merged one) was started from it: moving the base
   * back would lose them, or bring the merge back with them.
   */
  undoMerge?(input: { branch: string; base: string; sha: string }): Promise<void>
  /**
   * Remove the thread's worktree, keeping its branch. A worktree with changes not committed
   * stays (`false`), unless `discard` says to drop them, as when its project is deleted.
   */
  remove(threadId: string, options?: { discard?: boolean }): Promise<boolean>
  worktrees(): Promise<WorktreeInfo[]>
  /**
   * The thread's worktree as it is now, committed or not (ignored files left out), written
   * as a git tree without touching the worktree, its index or its branch; `null` when the
   * thread has no worktree. Two of them show what a turn changed, see `diff`.
   */
  snapshot?(threadId: string): Promise<string | null>
  /** What changed from one tree or commit to another, file by file. */
  diff?(from: string, to: string): Promise<ChangedFile[]>
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

/**
 * What came of merging a pull request:
 * - `merged`: the base now ends with `sha`, when the host said which commit.
 * - `conflict`: the branch conflicts with the base; nothing was merged.
 * - `moved`: the pull request got commits after `sha`; nothing was merged.
 * - `refused`: the host would not merge it as it is (a draft, required approvals or
 *   checks, branch protection); `detail` is the host's reason.
 */
export type PullRequestMergeOutcome =
  | { kind: 'merged'; sha: string | null }
  | { kind: 'conflict' }
  | { kind: 'moved' }
  | { kind: 'refused'; detail: string }

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
  /**
   * Merge a pull request into its base, only while its newest commit is still `sha`.
   * What the host refuses to merge comes back as an outcome; other failures throw. Left
   * out by providers that cannot; the user then merges on the host.
   */
  mergePullRequest?(
    number: number,
    input: { method: MergeMethod; sha: string },
  ): Promise<PullRequestMergeOutcome>
  /** The end of a check's log, for a thread to see why it failed. */
  checkLog(checkId: string): Promise<string>
  /**
   * The clone `fetch` and `push` work in, when commits can come into it from a checkout
   * elsewhere (see `HostedCheckout`).
   */
  objects?: GitObjects
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
  constructor(
    readonly path: string,
    /** Why, when it is not outside: e.g. a path kept out of reach inside it. */
    reason?: string,
  ) {
    super(`${path} ${reason ?? 'is outside the workspace'}`)
    this.name = 'WorkspacePathError'
  }
}

/** A file larger than the caller can take. */
export class FileTooLargeError extends Error {
  constructor(
    readonly path: string,
    readonly size: number,
    readonly max: number,
  ) {
    super(`${path} has ${size} bytes, more than ${max}`)
    this.name = 'FileTooLargeError'
  }
}

/**
 * Why a merge, or taking one back, did not happen, for reasons the user can act on:
 * - `checkout`: the base is checked out and the files there stand in the way (changes not
 *   committed to the same files).
 * - `base-moved`: the base got new commits while merging; trying again takes them in.
 * - `filters`: the base is checked out and the repository has `.gitattributes` filters
 *   (Git LFS, for one), which would not run when the checkout is updated.
 * - `identity`: git has no `user.name` or `user.email` to write the commit with.
 * - `git-version`: git is too old to merge without a working tree (2.38 or later).
 * - `no-base`: the base branch is gone.
 * - `undo-moved`: the base got commits after the merge to take back.
 * - `undo-pushed`: a remote branch already has the merge to take back.
 * - `undo-branched`: another local branch was started from the merge to take back.
 */
export class MergeRefused extends Error {
  constructor(
    readonly reason:
      | 'checkout'
      | 'base-moved'
      | 'filters'
      | 'identity'
      | 'git-version'
      | 'no-base'
      | 'undo-moved'
      | 'undo-pushed'
      | 'undo-branched',
    readonly detail: string,
  ) {
    super(`merge refused (${reason}): ${detail}`)
    this.name = 'MergeRefused'
  }
}
