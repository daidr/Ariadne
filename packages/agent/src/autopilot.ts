import {
  confirmBlock,
  DomainError,
  type AnyStoredEvent,
  type Ariadne,
  type AutopilotHold,
  type PlanOp,
  type PlanView,
  type ProjectView,
  type ReviewRoundView,
  type ThreadView,
} from '@ariadne/core'
import { createTranslator, type Locale } from '@ariadne/i18n'
import type { BranchCapability, RepoCapability } from '@ariadne/plugin-sdk'
import { MergeRefused } from '@ariadne/plugin-sdk'
import { syncThreadPullRequest } from './repo-tools'
import { overBudget } from './runner'

/** What the autopilot needs of the workspaces: branches to merge, and the repository. */
export interface AutopilotWorkspaces {
  branches(projectId: string): Promise<BranchCapability | null>
  hasRepo(projectId: string): boolean
  repo(projectId: string): Promise<RepoCapability>
}

/** Events after which the autopilot looks at the project again; the rest change nothing it reads. */
const WAKING = new Set<AnyStoredEvent['type']>([
  'project.settings.set',
  'project.autopilot.paused',
  'thread.reported',
  'thread.stopped',
  'thread.resumed',
  'thread.reviewed',
  'review.submitted',
  'review.abandoned',
  'branch.merged',
  'pull_request.synced',
  'pull_request.closed',
  'plan.revised',
  'attention.answered',
  'attention.withdrawn',
  'turn.finished',
])

/**
 * The project's autopilot, for projects that switched it on: it merges changes that passed
 * the AI review (with green checks and no conflict), opens threads on plan nodes whose
 * dependencies are done, at most `threads` at work at once, and asks the user before the
 * work goes on past a milestone. Paused, it does neither; what needs the user (blocked,
 * failed, out of budget, comments left after the last round) stays with them as always.
 *
 * It looks at a project after the events that can change what it would do, one project at
 * a time, and once at start for what happened while the server was down. Threads are
 * opened only while the coordinator is not at work, so the coordinator opens and briefs
 * the threads it means to, and the autopilot starts the rest.
 */
export class Autopilot {
  private unsubscribe: (() => void) | null = null
  private queue: Promise<void> = Promise.resolve()
  private readonly waiting = new Set<string>()
  private stopped = false

  constructor(
    private readonly app: Ariadne,
    private readonly workspaces: AutopilotWorkspaces,
    private readonly options: {
      locale: () => Locale
      /** How long after an event to look, so a burst of events is looked at once. */
      delayMs?: number
      onError?: (error: unknown) => void
    },
  ) {}

  start(): void {
    this.unsubscribe ??= this.app.subscribe((events) => {
      for (const event of events) {
        if (WAKING.has(event.type)) this.later(event.projectId)
      }
    })
    for (const project of this.app.listProjects()) this.enqueue(project.id)
  }

  async stop(): Promise<void> {
    this.stopped = true
    this.unsubscribe?.()
    this.unsubscribe = null
    await this.queue
  }

  /** Look at one project now, after whatever is queued; for tests and the start. */
  check(projectId: string): Promise<void> {
    this.enqueue(projectId)
    return this.queue
  }

  private later(projectId: string) {
    if (this.waiting.has(projectId)) return
    this.waiting.add(projectId)
    setTimeout(() => {
      this.waiting.delete(projectId)
      this.enqueue(projectId)
    }, this.options.delayMs ?? 1000)
  }

  private enqueue(projectId: string) {
    this.queue = this.queue
      .then(() => (this.stopped ? undefined : this.drive(projectId)))
      .catch(this.options.onError ?? ((error) => console.error('托管推进出错', error)))
  }

  private project(projectId: string): ProjectView | null {
    try {
      const project = this.app.getProject(projectId)
      return project.settings.autopilot.enabled && !project.autopilotPaused ? project : null
    } catch {
      // Deleted meanwhile.
      return null
    }
  }

  private async drive(projectId: string): Promise<void> {
    if (!this.project(projectId)) return
    const t = createTranslator(this.options.locale())
    for (const attentionId of this.app.staleGates(projectId)) {
      this.app.withdrawSystemAttention(projectId, attentionId, t('autopilot.gateGone'))
    }
    for (const thread of this.app.listThreads(projectId)) {
      const round = this.app.passedReview(thread.id)
      if (!round) continue
      // Each merge may take a while; the user may pause meanwhile.
      if (!this.project(projectId)) return
      await this.merge(thread, round)
    }
    const project = this.project(projectId)
    if (!project) return
    const coordinator = this.app.getConversation(project.coordinatorId)
    // Its turn to come may open threads itself; one that failed is not waited for.
    const coming = coordinator.pendingSeq > 0 && coordinator.lastOutcome !== 'failed'
    if (coordinator.turn || coming) return
    this.advance(project, this.app.getPlan(projectId), t)
  }

  /** Bring a thread's reviewed changes in, or say why it left them for the user. */
  private async merge(thread: ThreadView, round: ReviewRoundView): Promise<void> {
    // Not while it works: what is merged is what it committed.
    if (this.app.getConversation(thread.id).turn) return
    const head = round.head
    if (thread.autopilotHeld !== null && thread.autopilotHeld === (head ?? '')) return
    const { method } = this.app.getProject(thread.projectId).settings.autopilot
    const hold = (reason: AutopilotHold, detail = '') =>
      this.app.holdAutopilot(thread.id, { head, reason, detail })
    try {
      if (this.workspaces.hasRepo(thread.projectId)) {
        await this.mergePullRequest(thread, head, hold)
        return
      }
      const branch = thread.workspace?.branch
      const base = thread.workspace?.base
      const branches = await this.workspaces.branches(thread.projectId)
      if (!branch || !base || !branches?.merge) return
      const changes = await branches.changes({ threadId: thread.id, branch }, base)
      if (!changes) return
      if (head && changes.head !== head) return hold('moved')
      const outcome = await branches.merge({
        threadId: thread.id,
        branch,
        base,
        method,
        message: thread.title,
      })
      if (outcome.kind === 'merged') {
        this.app.recordMerge(thread.id, {
          branch,
          base,
          method,
          sha: outcome.sha,
          head: outcome.head,
          auto: true,
        })
      } else if (outcome.kind === 'conflict') {
        this.app.recordMergeConflict(thread.id, { branch, base, files: outcome.files })
      } else {
        hold('refused', createTranslator(this.options.locale())('autopilot.nothingToMerge'))
      }
    } catch (error) {
      if (error instanceof MergeRefused) {
        const t = createTranslator(this.options.locale())
        return hold(
          'refused',
          t(`error.merge.${error.reason}` as Parameters<typeof t>[0], { detail: error.detail }),
        )
      }
      hold('failed', error instanceof Error ? error.message : String(error))
    }
  }

  private async mergePullRequest(
    thread: ThreadView,
    head: string | null,
    hold: (reason: AutopilotHold, detail?: string) => void,
  ): Promise<void> {
    const repo = await this.workspaces.repo(thread.projectId)
    // What the repository says now, not as last polled.
    const pr = await syncThreadPullRequest(this.app, repo, thread.id)
    if (!pr || pr.state === 'closed') return hold('no-pull-request')
    if (pr.state === 'merged') return
    if (head && pr.headSha !== head) return hold('moved')
    // Running checks are waited for, and the repository watcher looks again; a failure
    // wakes the thread to fix it, and its next delivery is reviewed again.
    if (pr.checks.state === 'pending' || pr.checks.state === 'failure') return
    if (!repo.mergePullRequest) {
      const t = createTranslator(this.options.locale())
      return hold('refused', t('autopilot.cannotMergePullRequest'))
    }
    const { method } = this.app.getProject(thread.projectId).settings.autopilot
    const base = thread.workspace?.base ?? (await repo.defaultBranch())
    const outcome = await repo.mergePullRequest(pr.number, { method, sha: pr.headSha })
    if (outcome.kind === 'merged') {
      await syncThreadPullRequest(this.app, repo, thread.id, undefined, {
        method,
        base,
        sha: outcome.sha,
      })
    } else if (outcome.kind === 'conflict') {
      this.app.recordMergeConflict(thread.id, {
        branch: thread.workspace?.branch ?? '',
        base,
        files: [],
        pullRequest: pr.number,
      })
    } else if (outcome.kind === 'moved') {
      hold('moved')
    } else {
      hold('refused', outcome.detail)
    }
  }

  /**
   * Open threads on task nodes whose dependencies are done, while fewer than the cap are at
   * work, and ask at milestones whose dependencies are done.
   */
  private advance(
    project: ProjectView,
    plan: PlanView,
    t: ReturnType<typeof createTranslator>,
  ): void {
    const status = new Map(plan.nodes.map((node) => [node.id, node.status]))
    // Nodes a change waiting on the user would touch: it may remove them.
    const pending = new Set(
      plan.proposals
        .filter((proposal) => proposal.status === 'open')
        .flatMap((proposal) => proposal.ops.flatMap(opNodes)),
    )
    let atWork = this.app
      .listThreads(project.id)
      .filter(
        (thread) => !thread.reviewOf && thread.stage !== 'done' && thread.stage !== 'stopped',
      ).length
    const outOfBudget = overBudget(this.app, project.id, project.coordinatorId) !== null
    for (const node of plan.nodes) {
      if (pending.has(node.id)) continue
      if (!node.dependsOn.every((id) => status.get(id) === 'done')) continue
      if (node.kind === 'milestone') {
        if (
          node.gate === null &&
          node.status !== 'done' &&
          this.app.mayAskGate(project.id, node.id)
        ) {
          this.app.askGate(
            project.id,
            node.id,
            confirmBlock({
              title: t('autopilot.gate.question', { title: node.title }),
              approveText: t('autopilot.gate.go'),
              denyText: t('autopilot.gate.wait'),
            }),
          )
        }
        continue
      }
      // Decisions are the user's and the coordinator's to make.
      if (node.kind !== 'task' || node.threads.length) continue
      if (outOfBudget || atWork >= project.settings.autopilot.threads) continue
      try {
        const thread = this.app.createThread(project.id, {
          title: node.title,
          nodeId: node.id,
          auto: true,
        })
        this.app.postMessage(project.id, {
          conversationId: thread.id,
          author: 'coordinator',
          kind: 'relay',
          content: t('autopilot.brief', { title: node.title }),
        })
        atWork++
      } catch (error) {
        // The node went away meanwhile; the next look sees the plan as it is.
        if (!(error instanceof DomainError)) throw error
      }
    }
  }
}

/** The nodes a plan op touches. */
const opNodes = (op: PlanOp): string[] => ('id' in op ? [op.id] : [op.from, op.to])
