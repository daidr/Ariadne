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

export interface RepoCapability {
  /** `owner/name` or a URL. */
  remote: string
  defaultBranch(): Promise<string>
}
