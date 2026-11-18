/**
 * User-facing text from a plugin, one entry per locale. Chinese is the source language and
 * must be present; the UI falls back to it when the user's locale is missing.
 */
export type LocalizedText = { 'zh-CN': string } & Partial<Record<string, string>>

/** A JSON-schema-like description of a plugin's settings, rendered as a form by the web UI. */
export interface SettingsSchema {
  type: 'object'
  properties: Record<string, SettingField>
  required?: string[]
}

export type SettingField =
  | {
      type: 'string'
      title: LocalizedText
      description?: LocalizedText
      secret?: boolean
      /**
       * A directory path: the form offers to pick it from a listing, where the provider can
       * list directories (see `ContextProvider.browse`).
       */
      directory?: boolean
      default?: string
    }
  | { type: 'number'; title: LocalizedText; description?: LocalizedText; default?: number }
  | { type: 'boolean'; title: LocalizedText; description?: LocalizedText; default?: boolean }
  | {
      type: 'enum'
      title: LocalizedText
      description?: LocalizedText
      options: string[]
      default?: string
    }

/**
 * A pull request (or GitHub's kin: a GitLab merge request) as a link to it shows, without
 * asking its host: `label` is how a message names it, e.g. `daidr/Ariadne#34`.
 */
export interface LinkSummary {
  kind: 'pull-request'
  label: string
}

/** What the card over a pull request link shows. Titles are written by anyone who can open one. */
export interface PullRequestPreview {
  kind: 'pull-request'
  /** `owner/name`. */
  repository: string
  number: number
  /** How the host refers to it in text, `#<number>` when left out (see `PullRequestInfo`). */
  reference?: string
  url: string
  title: string
  state: 'open' | 'closed' | 'merged'
  draft: boolean
  author: string | null
  /** Lines added and removed; `null` when the host does not say (GitLab). */
  additions: number | null
  deletions: number | null
  /** `null` when the host does not say exactly (GitLab past 1000 files). */
  changedFiles: number | null
  /** When it was merged, closed or opened, whichever is its state now; ISO time. */
  at: string
}

export type LinkPreview = PullRequestPreview

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
  /** The base tip when the comparison was made. */
  baseHead?: string
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
  /** The branch moved on from the commit shown at confirmation. */
  | { kind: 'moved' }

/** A thread's worktree on disk. */
export interface WorktreeInfo {
  threadId: string
  path: string
  /** Space it takes, when it could be measured. */
  bytes: number | null
}

/** One directory a listing shows; `path` is ready to use, so callers never join paths. */
export interface DirectoryEntry {
  name: string
  path: string
}

/**
 * The directories in one directory, for picking a path in a form. `path` is the directory
 * listed, `parent` the one above it (`null` at a root). When what was typed is not a
 * directory, its parent is listed, keeping only the names that start like its last part.
 */
export interface DirectoryListing {
  path: string
  parent: string | null
  directories: DirectoryEntry[]
}
