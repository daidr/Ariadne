import type { LocalizedMessage } from '@ariadne/i18n'
import type { AskBlock, BlockAnswer, ShowBlock } from './blocks'
import type { FileType } from './files'
import type { MergeMethod, ProjectSettings } from './project-settings'

export type { MergeMethod }

/** Who wrote a message. */
export type Author = 'user' | 'coordinator' | 'thread'

/**
 * `chat` is an ordinary message. `relay` is a message the coordinator sent into a thread:
 * the thread's panel shows it faded and collapsed.
 */
export type MessageKind = 'chat' | 'relay'

/** What a thread reports about its own work. */
export type ReportKind = 'progress' | 'waiting' | 'ready' | 'blocked' | 'failed' | 'done'

export type TodoStatus = 'doing' | 'next' | 'waiting' | 'done'

/** An issue of the project's tracker, as a plan node links it. */
export interface LinkedIssue {
  /** Where the issue lives, e.g. `owner/name`. */
  tracker: string
  number: number
  title: string
  url: string
}

export type IssueWriteAction = 'comment' | 'close' | 'reopen'

/** One change to an issue, made with the user's yes on `attentionId`. */
export interface IssueWrite {
  threadId: string
  attentionId: string
  purpose: string
  tracker: string
  number: number
  action: IssueWriteAction
  /** What is written, exactly as the user agreed to it (the comment, or why it is closed). */
  text: string
}

export interface TodoItem {
  label: string
  status: TodoStatus
  /** For `waiting`: what the item waits on. */
  detail?: string
}

/** A tool call as the model made it; `arguments` is the model's JSON text, unparsed. */
export interface ToolCallRecord {
  id: string
  name: string
  arguments: string
}

/**
 * A file that came with a message or a tool result: an image the model looks at, or text
 * it reads. The bytes stay in the project's files; `name` is what the user or tool called it.
 */
export interface Attachment {
  fileId: string
  name: string
  mime: FileType
  size: number
}

/**
 * Something the user pointed at with `@` in a message: a thread or an artifact of the
 * project (titled as it was then), or a file of the workspace the conversation reads.
 */
export type MessageMention =
  | { kind: 'thread'; id: string; title: string }
  | { kind: 'artifact'; id: string; title: string }
  | { kind: 'file'; path: string }
  | { kind: 'skill'; name: string }

/**
 * The user's comment on one line of a thread's changes, sent with others in one message.
 * `line` counts from 1 in the file as the changes showed it; `quote` is that line's text,
 * so the thread finds it again after the file moved on.
 */
export interface LineComment {
  path: string
  line: number
  quote: string
  body: string
  /** On the file as it was before the changes; absent for the changed version. */
  side?: 'original'
}

/** What a review comment is about: the only things a pre-delivery review reports. */
export type ReviewCommentKind = 'bug' | 'acceptance' | 'security'

/** A reviewing thread's comment on one line of the author's changes, as `LineComment`. */
export interface ReviewComment extends LineComment {
  kind: ReviewCommentKind
}

/**
 * What the author did about one comment of a review round: `comment` counts from 1 in the
 * round's order. Declining takes a reason (`note`); a fix may say what it did.
 */
export interface ReviewReply {
  comment: number
  outcome: 'fixed' | 'declined'
  note: string
}

export type ReviewVerdict = 'pass' | 'changes'

export type TurnFileStatus = 'added' | 'modified' | 'deleted' | 'renamed' | 'copied' | 'type'

/** Files changed, and lines added and removed over them. */
export interface ChangeTotals {
  files: number
  added: number
  removed: number
}

/** One file a turn changed in its thread's workspace. */
export interface TurnFile {
  path: string
  previousPath: string | null
  status: TurnFileStatus
  /** Lines added and removed; `null` for a binary file. */
  added: number | null
  removed: number | null
}

/**
 * A block of a model's reasoning as its provider returned it; `signature` (or the opaque
 * `text` of a `redacted` block) is what the provider checks when it is sent back.
 */
export interface ReasoningRecord {
  text: string
  signature?: string
  redacted?: boolean
}

/**
 * Tokens of one model call. `input` is everything read, `cached` the part of it read from
 * the provider's cache, `reasoning` the part of `output` spent thinking (when known).
 */
export interface TokenUsage {
  input: number
  output: number
  cached?: number
  /** The part of `input` written to the provider's cache. */
  cacheWrite?: number
  reasoning?: number
  /**
   * What the request cost, estimated: as the service reported it, or from the price the
   * user gave the model, at the time.
   */
  cost?: Cost
}

export type Currency = 'USD' | 'CNY'
export const CURRENCIES: readonly Currency[] = ['USD', 'CNY']

/** An amount of money in one currency. */
export interface Cost {
  amount: number
  currency: Currency
}

/**
 * The parts of a step's model request that the transcript does not hold, by the hash of
 * their `prompt.recorded` text: the system prompt, the tool definitions (JSON), and
 * `call`: JSON of the provider kind, model, effort and settings (secrets left out) the
 * request went with.
 */
export interface StepInput {
  system: string
  tools: string
  call: string
  /** Whether images went to the model as images; without, they were described in words. */
  images: boolean
}

/** When a model call was sent and when its first output came, in ms since the epoch. */
export interface CallTiming {
  startedAt: number
  firstOutputAt: number | null
}

/**
 * How a turn ended. `interrupted` means the process stopped mid-turn; `cancelled` means
 * the user stopped it.
 */
export type TurnOutcome = 'completed' | 'failed' | 'cancelled' | 'interrupted'

export type PlanNodeKind = 'task' | 'milestone' | 'decision'

export interface PlanNodeFields {
  kind: PlanNodeKind
  title: string
  description: string
  /** How the user will judge the node finished. */
  acceptance: string
}

/**
 * One change to the plan graph. Node ids are assigned before the event is written, so
 * replaying a revision always produces the same graph. `link` makes `to` depend on `from`.
 */
export type PlanOp =
  | ({ op: 'add'; id: string } & PlanNodeFields)
  | ({
      op: 'update'
      id: string
      /**
       * Values the fields must still have, so undoing an edit refuses to overwrite a later
       * edit of the same field.
       */
      expect?: Partial<Omit<PlanNodeFields, 'kind'>>
    } & Partial<Omit<PlanNodeFields, 'kind'>>)
  | { op: 'remove'; id: string }
  | { op: 'link'; from: string; to: string }
  | { op: 'unlink'; from: string; to: string }

export type PlanAuthor = 'user' | 'coordinator'

/** Where a project's work happens. Only local directories for now. */
export interface ProjectSource {
  kind: string
  settings: Record<string, unknown>
  /**
   * A hosted repository the source's workspaces push to and open pull requests on, for a
   * source on another machine.
   */
  hosting?: { kind: string; settings: Record<string, unknown> }
}

/** Markdown documents, whiteboards and dashboard snapshots; see artifact-content.ts. */
export type ArtifactKind = 'document' | 'whiteboard' | 'dashboard'

/**
 * A change to an artifact's comments. A thread of comments is about a stretch of the
 * document's text, kept as that text (`quote`) rather than as a place, so it is found again
 * after anyone rewrites the Markdown around it. Times are the event's.
 */
export type ArtifactCommentAction =
  | {
      type: 'create'
      threadId: string
      commentId: string
      /** A document's commented text; empty on a whiteboard. */
      quote: string
      body: string
      /** On a whiteboard: the item or connector the thread is about. */
      anchor?: ArtifactCommentAnchor
    }
  | { type: 'reply'; threadId: string; commentId: string; body: string }
  | { type: 'edit'; threadId: string; commentId: string; body: string }
  | { type: 'delete'; threadId: string; commentId: string }
  | { type: 'resolve'; threadId: string }
  | { type: 'reopen'; threadId: string }
  | { type: 'remove'; threadId: string }

/** What a whiteboard's comment thread is about: one item, or one connector. */
export type ArtifactCommentAnchor = { node: string } | { edge: string }

/**
 * One thing the user did in an artifact since the last round of feedback, as the agent
 * reads it. `where` says in words where on a whiteboard it is, e.g. which option and
 * which part of its screenshot.
 */
export type ArtifactFeedbackItem =
  /** A document's changed lines (- removed, + added), cut to a bounded length. */
  | { kind: 'edit'; diff: string }
  /** The options picked on a whiteboard now, by id and title; empty when none is. */
  | { kind: 'choice'; chosen: { id: string; title: string }[] }
  | {
      kind: 'comment'
      threadId: string
      action: 'create' | 'reply'
      body: string
      /** The document's text or, on a whiteboard, where the item is. */
      where: string
    }
  /** Something the user put on a whiteboard: a drawing, a shape, a note, an image. */
  | { kind: 'mark'; nodeId: string; type: string; text: string; where: string }
  /** Other changes to a whiteboard, in words: moved, removed, retitled. */
  | { kind: 'board'; changes: string[] }

/** How the checks of a commit stand, all together. */
export type ChecksState = 'none' | 'pending' | 'success' | 'failure'

/** A thread's pull request on the project's repository. */
export interface ThreadPullRequest {
  number: number
  /** How the host refers to it in text, e.g. `!12`; `#<number>` when left out. */
  reference?: string
  url: string
  title: string
  state: 'open' | 'closed' | 'merged'
  headSha: string
  checks: { state: ChecksState; failed: string[] }
}

/** A thread's branch merged into its base by the user. */
export interface BranchMerge {
  branch: string
  base: string
  method: MergeMethod
  /** The commit the base moved to. */
  sha: string
  /** The branch's newest commit, which the merge took in. */
  head: string
  /** Merged by the project's autopilot, not by the user's hand. */
  auto?: true
}

/**
 * Why the autopilot left changes that passed the review for the user to merge:
 * - `moved`: the branch (or its pull request) has commits the review did not read.
 * - `no-pull-request`: a hosted repository's branch without an open pull request.
 * - `refused`: the merge was refused, e.g. the checkout is in the way or the pull request
 *   cannot be merged as it is; `detail` says why.
 * - `failed`: merging went wrong.
 */
export type AutopilotHold = 'moved' | 'no-pull-request' | 'refused' | 'failed'

/** Every durable fact Ariadne records. Payloads are plain JSON. */
export interface EventMap {
  'project.created': { name: string; coordinatorId: string }
  'project.renamed': { name: string }
  /**
   * The project was deleted with everything it held; this is all the log keeps of it.
   * Nothing else of the project is written after it.
   */
  'project.deleted': Record<string, never>
  /** The project's context source; `null` removes it. */
  'project.source.set': { source: ProjectSource | null }
  /** How many of the shared work slots this project may take at once; `null` is no cap. */
  'project.slots.set': { slots: number | null }
  /** The external tool servers (MCP) this project's conversations may use, by id. */
  'project.mcp.set': { servers: string[] }
  /** The project's own settings, whole: instructions, workspace setup, tools, budget. */
  'project.settings.set': { settings: ProjectSettings }
  /**
   * The user paused the project's autopilot, or let it go on. Paused, nothing is merged or
   * started on its own; threads at work go on.
   */
  'project.autopilot.paused': { paused: boolean }
  /**
   * `model` is the id of the model setting the coordinator chose for it, if any; `branch`
   * the readable part of its branch's name it suggested.
   */
  'thread.created': {
    threadId: string
    title: string
    nodeId: string | null
    model: string | null
    branch: string | null
    /**
     * Set for a thread that reviews another's changes before they reach the user: the
     * author's id. It reads the author's workspace and changes nothing.
     */
    reviewOf?: string
    /** Opened by the autopilot on a plan node whose dependencies were done. */
    auto?: true
  }
  /** The thread's model changed; `null` goes back to the default for threads. */
  'thread.model_set': { threadId: string; model: string | null; by: string }
  /**
   * A thread's workspace was prepared, e.g. a git worktree on its own branch. `base` is the
   * branch that one started from, which its changes are compared and merged against.
   */
  'workspace.opened': {
    threadId: string
    label: string
    branch: string | null
    base: string | null
  }
  /**
   * The thread's worktree was removed; its branch stays, and the next use checks it out
   * again. `merged`: removed on its own once the work was merged (or its pull request
   * closed) and the thread was finished with it; `user`: the user removed it.
   */
  'workspace.removed': { threadId: string; reason: 'merged' | 'user' }
  /** The user merged the thread's branch into its base; the base now ends with `sha`. */
  'branch.merged': { threadId: string } & BranchMerge
  /**
   * Merging the thread's branch (by the user or the autopilot) stopped at these files,
   * changed on both sides.
   * Nothing was merged; the thread gets to bring the base in and resolve them.
   */
  'branch.conflicted': {
    threadId: string
    branch: string
    base: string
    files: string[]
    /** Set when it was the thread's pull request that could not be merged. */
    pullRequest?: number
  }
  /**
   * Changes that passed the review were left for the user: the autopilot would not merge
   * them as they were. `head` is the branch's commit it looked at; it does not try that
   * commit again.
   */
  'autopilot.held': { threadId: string; head: string | null; reason: AutopilotHold; detail: string }
  'thread.started': { threadId: string }
  'thread.reported': {
    threadId: string
    report: ReportKind
    /** For the coordinator: what changed and what it depends on. */
    summary: string
    /** For the user: one short line on what to do or look at, shown wherever the thread is. */
    headline: string
    /** Artifacts the user should look at, by id; shown by their titles. */
    artifacts: string[]
    /** The branch's newest commit when it reported, once the user merged it before. */
    head?: string
    /** A `ready` that goes to the project's pre-delivery review before the user sees it. */
    reviewing?: boolean
    /** A `ready` that reaches the user after this many review rounds. */
    reviewRounds?: number
  }
  /**
   * The thread's changes, as reported `ready`, wait for review round `round` (from 1 in
   * this delivery) by `reviewerId`; `head` is the branch's newest commit then.
   */
  'review.requested': { threadId: string; reviewerId: string; round: number; head: string | null }
  /**
   * The reviewer's verdict on a round. `pass` hands the changes to the user; `changes`
   * sends the comments to the author, who fixes or answers each before reporting again.
   */
  'review.submitted': {
    threadId: string
    reviewerId: string
    round: number
    verdict: ReviewVerdict
    summary: string
    comments: ReviewComment[]
  }
  /**
   * The reviewer left the round without a verdict: its turn failed or ended without one, or
   * it was stopped. The author's changes go to the user as they are, as with a pass.
   */
  'review.abandoned': { threadId: string; reviewerId: string; round: number }
  /** The author's answer to each comment of a round, made with its next `ready`. */
  'review.replied': { threadId: string; round: number; replies: ReviewReply[] }
  /**
   * The thread is held: its running turn ends and no new one starts until it is resumed.
   * `by` is `user` or the coordinator's id.
   */
  'thread.stopped': { threadId: string; reason: string; by: string }
  /** Work goes on; `reason` is why it had been stopped, for the thread to read. */
  'thread.resumed': { threadId: string; by: string; reason: string }
  /**
   * The user is done with what the thread's last report put up for review, without merging
   * it here: they looked it over, merged it elsewhere, or let it go.
   */
  'thread.reviewed': { threadId: string }
  /**
   * Its turn ended without a report: it is told once to report what came of it. A second
   * silent turn is left for the user.
   */
  'thread.nudged': { threadId: string }
  /**
   * Something waits on the user: an ask block (choice, confirmation or form). An agent's
   * comes with the message it posted it in (`messageId`); the system's own (approvals,
   * plan proposals) are only listed under "needs me".
   */
  'attention.opened': {
    attentionId: string
    /** `null` when the coordinator asks. */
    threadId: string | null
    block: AskBlock
    messageId: string | null
    /**
     * Set when the system asks for a standing approval (e.g. `push-public`), so a tool can
     * check later whether the user agreed. Its block is a confirmation.
     */
    purpose?: string | null
  }
  'attention.answered': { attentionId: string; answer: BlockAnswer }
  /**
   * The agent that asked takes its question back, e.g. once the user answered it in words
   * or it no longer matters. It leaves "needs me"; the message shows it was withdrawn.
   */
  'attention.withdrawn': { attentionId: string; reason: string }
  /**
   * Another thread waits on the same standing approval: it hears the answer too, so the
   * user answers one card instead of one per thread.
   */
  'attention.joined': { attentionId: string; threadId: string }
  'message.posted': {
    messageId: string
    /** The coordinator id or a thread id. */
    conversationId: string
    author: Author
    kind: MessageKind
    content: string
    /** Tables, charts and steps shown under the text; an ask block is its attention's. */
    blocks?: ShowBlock[]
    /** Images and text files the user sent with it. */
    attachments?: Attachment[]
    /** What the user pointed at with `@`. */
    mentions?: MessageMention[]
    /** The user's comments on lines of the thread's changes. */
    comments?: LineComment[]
  }
  'message.edited': { messageId: string; content: string; reason: string }
  'message.read': { conversationId: string; upTo: string }
  'todo.updated': { conversationId: string; items: TodoItem[] }
  /**
   * A standing preference or fact of the project ("write in Chinese", "the API lives in
   * packages/server"), saved or changed. Every conversation of the project sees them.
   */
  'memory.saved': {
    memoryId: string
    text: string
    author: string
    /** The notice of an agent's change this one takes back, when the user undid it. */
    undoes?: string
  }
  'memory.removed': { memoryId: string; author: string; undoes?: string }
  /**
   * A conversation's older history was summarised: the model sees `summary` in place of
   * every transcript entry it had read up to `upTo` (a position in the model's order).
   * The summary was asked for from the transcript as of `sawSeq`.
   */
  'conversation.compacted': {
    conversationId: string
    summary: string
    upTo: number
    sawSeq: number
    usage: TokenUsage | null
    /** Hash of the call's `prompt.recorded` JSON, as in `StepInput.call`. */
    call: string
    timing: CallTiming
    /**
     * What the model thought while summarising, unsigned. It goes back with the notes:
     * services that think by default (DeepSeek) refuse an assistant message without it.
     */
    reasoning: ReasoningRecord[]
    /**
     * The todo list and open questions as they stood when the summary was written; they
     * go with the notes. Fixed here, so the request keeps its beginning (and the
     * provider's cache) until the next summary: later changes are calls in the history.
     */
    state: string
  }
  /** The user settled a notice without anything else to record, e.g. undid a stale one. */
  'notice.dismissed': { noticeId: string }
  /**
   * Older tool results of a conversation were cleared to make room: the model reads a
   * short placeholder instead of each long result it had read up to `upTo` (a position in
   * the model's order). The log keeps them whole. Decided on the transcript as of `sawSeq`.
   */
  'conversation.results_cleared': { conversationId: string; upTo: number; sawSeq: number }
  /** Summarising a conversation's history failed; the turn went on with all of it. */
  'conversation.compaction_failed': { conversationId: string; detail: string }
  /**
   * The repository's instruction file (AGENTS.md) as the thread's workspace holds it now:
   * recorded when it first appears and whenever it changes, `content: null` once removed.
   * The model reads the newest recorded version, so every request can be rebuilt.
   */
  'conversation.instructions': {
    conversationId: string
    path: string
    hash: string
    content: string | null
    /** Whether an earlier version had been recorded, so the model is told it moved. */
    changed: boolean
    /** The version differs from the checkout's commit: someone edited it in the workspace. */
    uncommitted?: boolean
  }
  /** A model request failed in a way worth trying again; `attempt` counts the retries. */
  'conversation.model_retry': {
    conversationId: string
    attempt: number
    delayMs: number
    detail: string
  }
  /**
   * The model's retries ran out on an error worth retrying (rate limit, overload, a server
   * error), and the turn goes on with the role's fallback model. Labels as the user named them.
   */
  'conversation.model_fallback': {
    conversationId: string
    turnId: string
    from: string
    to: string
    detail: string
  }
  /**
   * The agent asked to be woken at `until` (epoch ms), with `note` saying what it wants to
   * look at then. A conversation waits for one thing at a time: a newer wait replaces it.
   */
  'conversation.wait_set': { conversationId: string; waitId: string; until: number; note: string }
  /**
   * The wait is over: it came due, the user woke it early (`now`), or the user or the agent
   * called it off (`cancelled`, by `user` or the conversation's own id). Due and early waits
   * wake the agent with the note.
   */
  'conversation.wait_ended': {
    conversationId: string
    waitId: string
    how: 'due' | 'now' | 'cancelled'
    by: string
    /** The wait's own `until` and `note`, so whoever reads this needs nothing else. */
    until: number
    note: string
  }
  /** A routine of the project came due: the coordinator is woken with its words. */
  'routine.ran': { routineId: string; prompt: string; dueAt: number }
  /** The agent of a conversation began working. */
  'turn.started': { conversationId: string; turnId: string; model: string }
  /**
   * One model response inside a turn. `sawSeq` is the newest event the model's input
   * included, so inputs up to it count as handled.
   */
  'turn.step': {
    conversationId: string
    turnId: string
    text: string
    reasoning: ReasoningRecord[]
    toolCalls: ToolCallRecord[]
    /**
     * Why the model stopped: it answered, it called tools, or its output hit the length
     * limit (`length`): the text is what came, the calls only those that came whole.
     */
    finish: 'stop' | 'tool-calls' | 'length'
    sawSeq: number
    usage: TokenUsage | null
    input: StepInput
    timing: CallTiming
  }
  /**
   * One model response of a read-only research a conversation's `research` call ran, in a
   * context of its own: its own question, system prompt and tools (hashes, as in
   * `StepInput`), and the results of the calls it made, which only it read. The calling
   * conversation sees the research's answer alone, as that call's result.
   */
  'research.step': {
    conversationId: string
    turnId: string
    /** The `research` call it answers. */
    toolCallId: string
    /** Counted from 1 within the research. */
    number: number
    question: string
    text: string
    reasoning: ReasoningRecord[]
    toolCalls: ToolCallRecord[]
    results: { toolCallId: string; name: string; content: string; isError: boolean }[]
    finish: 'stop' | 'tool-calls' | 'length'
    usage: TokenUsage | null
    input: { system: string; tools: string; call: string }
    timing: CallTiming
    /** When the response was whole; its calls ran from then until the event. */
    answeredAt: number
  }
  /**
   * A web search a conversation's `web_search` call had a model's own service run: a
   * request beside the turn's steps, whose tokens count toward the conversation's.
   */
  'search.answered': {
    conversationId: string
    turnId: string
    /** The model asked, as the user sees it named. */
    model: string
    usage: TokenUsage | null
  }
  /**
   * A piece of model input seen for the first time in the project, stored once by its
   * hash; steps name the pieces they were sent, so the trace can show every request whole.
   */
  'prompt.recorded': { hash: string; content: string }
  'tool.finished': {
    conversationId: string
    turnId: string
    toolCallId: string
    name: string
    content: string
    isError: boolean
    /** Images the tool handed the model, e.g. a picture read from the workspace. */
    files?: Attachment[]
    /**
     * When the call ran. Calls that only look run at the same time and are recorded once
     * all of them are done, so the event's own time says neither. Missing for a call the
     * turn closed without running it.
     */
    timing?: { startedAt: number; finishedAt: number }
  }
  /**
   * What a turn changed in its thread's workspace, committed or not: from the workspace as
   * the turn first touched it (`from`) to the workspace as the turn left it (`to`), both
   * git trees. Only for a turn that changed something. `files` stops at a bound; `total`
   * counts them all.
   */
  'turn.changed': {
    conversationId: string
    turnId: string
    from: string
    to: string
    files: TurnFile[]
    total: { files: number; added: number; removed: number }
    /** When the turn started: its replies are those posted since. Missing on older events. */
    startedAt?: number
    /**
     * The whole workspace as the turn left it, against where its branch left the base:
     * what the thread has changed so far, committed or not. Missing where it could not
     * be read.
     */
    branch?: ChangeTotals
  }
  'turn.finished': {
    conversationId: string
    turnId: string
    outcome: TurnOutcome
    /** Why it failed, for the user. */
    error: LocalizedMessage | null
  }
  /** The user asked a conversation to try again after a failed turn. */
  'turn.requested': { conversationId: string }
  /**
   * A change to the plan graph took effect. `revertOf` names the revision it undoes.
   * `proposalId` names the proposal it carries out, after the user agreed.
   */
  'plan.revised': {
    revisionId: string
    author: PlanAuthor
    reason: string
    ops: PlanOp[]
    revertOf: string | null
    proposalId: string | null
  }
  /**
   * A change big enough to need the user's yes (removing nodes, changing acceptance or
   * milestones). It waits on an attention whose first option applies it.
   */
  'plan.proposed': {
    proposalId: string
    attentionId: string
    reason: string
    ops: PlanOp[]
    /** Set when the proposal undoes an earlier revision. */
    revertOf: string | null
  }
  /** The user agreed, but the plan had moved on and the proposal no longer applied. */
  'plan.proposal.stale': { proposalId: string; reason: string }
  /** The user has seen that an agreed proposal did not take effect. */
  'plan.proposal.dismissed': { proposalId: string }
  /**
   * An issue of the project's tracker belongs to a plan node: the node's threads read it as
   * part of their work. The title is as it was when linked, for showing without a fetch.
   */
  'plan.issue_linked': { nodeId: string; issue: LinkedIssue; by: string }
  'plan.issue_unlinked': { nodeId: string; tracker: string; number: number; by: string }
  /**
   * A thread is about to change an issue (comment, close, reopen) with the user's yes on
   * `attentionId`. Recorded first, so a change cut off by a crash is not made blindly again.
   * Each yes is used for one change only.
   */
  'issue.writing': IssueWrite
  /** The change went through; `url` shows it on the tracker. */
  'issue.written': IssueWrite & { url: string | null }
  /**
   * A change was cut off or failed before it was known to be made; `threadId` was told to
   * check (absent in logs written before it was recorded).
   */
  'issue.write_unsure': { attentionId: string; threadId?: string }
  /**
   * A thread makes the call the user's yes on `attentionId` was for; the yes is used now, so
   * the same call again asks again. `what` names it: `mcp:<server>/<tool>` or `command`.
   */
  'approval.used': { threadId: string; attentionId: string; what: string }
  /** The thread's pull request as last seen on the repository; written only on a change. */
  'pull_request.synced': { threadId: string } & ThreadPullRequest
  /** Checks of the pull request's newest commit failed; the thread gets to fix them. */
  'checks.failed': {
    threadId: string
    number: number
    headSha: string
    failed: string[]
    /** How many failures in a row this is; missing in older logs, where it counts as 1. */
    attempt?: number
  }
  /** The pull request was merged or closed. */
  'pull_request.closed': {
    threadId: string
    number: number
    merged: boolean
    /** Merged by the project's autopilot: how, and the commit the base moved to. */
    auto?: { method: MergeMethod; base: string; sha: string | null }
  }
  'artifact.created': {
    artifactId: string
    kind: ArtifactKind
    title: string
    nodeId: string | null
    /** The coordinator, a thread id, or `user`. */
    author: string
    content: string
  }
  'artifact.revised': {
    artifactId: string
    version: number
    title: string
    content: string
    author: string
    reason: string
    /**
     * For the user's edit: the conversation told about it, the one that wrote the
     * artifact last, or the coordinator.
     */
    notify?: string | null
    /** For the user's edit of a document: the changed lines, cut to a bounded length. */
    diff?: string | null
    /** The commented text as it reads after the user's edit, by comment thread. */
    quotes?: Record<string, string>
  }
  'artifact.commented': {
    artifactId: string
    /** `user`, the coordinator or a thread. */
    by: string
    action: ArtifactCommentAction
    /** For the user's new comment or reply: the conversation woken to answer it. */
    notify: string | null
  }
  /**
   * What the user did in an artifact since the last round, sent on its own once they
   * stopped for a moment or left it; it wakes `notify`. `images` are pictures of the
   * whiteboard options the user marked, with their marks.
   */
  'artifact.feedback': {
    artifactId: string
    round: number
    /** The version the artifact had when it was sent. */
    version: number
    notify: string
    items: ArtifactFeedbackItem[]
    images: Attachment[]
  }
  /** The agent finished a round of the user's feedback; `summary` is what it did, for the user. */
  'artifact.feedback_done': {
    artifactId: string
    round: number
    by: string
    summary: string
  }
}

export type EventType = keyof EventMap

/** An event as stored: every event belongs to one project. */
export interface StoredEvent<T extends EventType = EventType> {
  seq: number
  id: string
  type: T
  projectId: string
  data: EventMap[T]
  at: number
}

export type AnyStoredEvent = { [T in EventType]: StoredEvent<T> }[EventType]

export interface NewEvent<T extends EventType = EventType> {
  type: T
  projectId: string
  data: EventMap[T]
}

export type AnyNewEvent = { [T in EventType]: NewEvent<T> }[EventType]
