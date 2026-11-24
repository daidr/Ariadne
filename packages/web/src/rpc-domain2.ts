import type {
  ArtifactCommentAction,
  ArtifactCommentAnchor,
  ArtifactCommentThreadView,
  ArtifactFeedbackItem,
  ArtifactFeedbackView,
  ArtifactKind,
  ArtifactPreview,
  ArtifactSummary,
  ArtifactVersionView,
  ArtifactView,
  AskBlock,
  FileSource,
  FileSourceSet,
  FormField,
  FormValue,
  NeedsMeItem,
  PlanNodeView,
  PlanOp,
  PlanPreview,
  PlanProposalView,
  PlanRevisionView,
  PlanUndoPreview,
  PlanView,
  ProjectActivity,
  ProjectDetected,
  ProjectSettings,
  ProjectSource,
  ProjectView,
  SearchResult,
  ThreadView,
  UsageTotals,
} from '@ariadne/core'
import type { LocalizedMessage, MessageKey, MessageParams } from '@ariadne/i18n'
import {
  ArtifactService,
  IssueService,
  PlanService,
  ProjectService,
  type ArtifactCommentAnchor as ProtoArtifactCommentAnchor,
  type ArtifactCommentThreadView as ProtoArtifactCommentThreadView,
  type ArtifactFeedbackView as ProtoArtifactFeedbackView,
  type ArtifactPreview as ProtoArtifactPreview,
  type ArtifactSummary as ProtoArtifactSummary,
  type ArtifactVersionView as ProtoArtifactVersionView,
  type ArtifactView as ProtoArtifactView,
  type ErrorParamValue,
  type FileSource as ProtoFileSource,
  type FileSourceSet as ProtoFileSourceSet,
  type IssueDetail as ProtoIssueDetail,
  type IssueSummary as ProtoIssueSummary,
  type LocalizedError,
  type NeedsMeItem as ProtoNeedsMeItem,
  type PlanNodeView as ProtoPlanNodeView,
  type PlanOp as ProtoPlanOp,
  type PlanPreview as ProtoPlanPreview,
  type PlanProposalView as ProtoPlanProposalView,
  type PlanRevisionView as ProtoPlanRevisionView,
  type PlanUndoPreview as ProtoPlanUndoPreview,
  type PlanView as ProtoPlanView,
  type ProjectAskBlock,
  type ProjectDetected as ProtoProjectDetected,
  type ProjectFormValue,
  type ProjectSettings as ProtoProjectSettings,
  type ProjectSettingsView as ProtoProjectSettingsView,
  type ProjectSource as ProtoProjectSource,
  type ProjectThreadView,
  type ProjectUsageTotals,
  type ProjectView as ProtoProjectView,
} from '@ariadne/proto'
import type { JsonObject, JsonValue } from '@bufbuild/protobuf'
import type { Client, Transport } from '@connectrpc/connect'
import { ApiError, createRpcClient, rpcTransport } from './rpc'

export interface IssueSummaryView {
  number: number
  url: string
  title: string
  state: string
  author: string | null
  labels: string[]
  comments: number
  updatedAt: string
}

export interface IssueCommentView {
  id: string
  author: string | null
  body: string
  createdAt: string
  url: string
}

export interface IssueDetailView extends IssueSummaryView {
  body: string
  createdAt: string
  recent: IssueCommentView[]
}

export interface SourceInputData {
  kind: string
  settings: Record<string, unknown>
  hosting?: { kind: string; settings: Record<string, unknown> } | null
}

export interface ProjectSettingsDataView {
  settings: ProjectSettings
  detected: ProjectDetected
  env: string[]
  tools: ('fetch' | 'search' | 'commands' | 'issueWrites')[]
  hostedMerge: boolean
}

export interface ProjectDetailData {
  project: ProjectView
  threads: ThreadView[]
  needsMe: NeedsMeItem[]
  hasTracker: boolean
  sync: {
    cursor: string
    partial: boolean
    ids?: string[]
  }
}

export interface UpdateProjectPatch {
  name?: string
  settings?: ProjectSettings
  env?: { name: string; value: string }[]
  slots?: number | null
  mcp?: string[]
  autopilotPaused?: boolean
}

export interface PaginatedArtifactsResult {
  artifacts: ArtifactSummary[]
  nextPageToken?: string
  hasMore?: boolean
}

export interface PaginatedArtifactVersionsResult {
  versions: ArtifactVersionView[]
  nextPageToken?: string
  hasMore?: boolean
}

export interface ArtifactDetailResult {
  artifact: ArtifactView
  versions: ArtifactVersionView[]
  comments: ArtifactCommentThreadView[]
  nextVersionPageToken?: string
  hasMoreVersions?: boolean
}

export interface PlanPageResult {
  plan: PlanView
  nextRevisionPageToken?: string
  hasMoreRevisions?: boolean
}

export interface PlanRevisionsPageResult {
  revisions: PlanRevisionView[]
  nextPageToken?: string
  hasMore?: boolean
}

// ---- Shared decoding helpers ----

function toJsonValue(value: unknown): JsonValue {
  if (
    value === null ||
    typeof value === 'string' ||
    typeof value === 'number' ||
    typeof value === 'boolean'
  ) {
    return value
  }
  if (Array.isArray(value)) {
    return value.map((item) => toJsonValue(item))
  }
  if (typeof value === 'object') {
    const out: JsonObject = {}
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      if (v !== undefined) out[k] = toJsonValue(v)
    }
    return out
  }
  return null
}

function toJsonObject(value?: Record<string, unknown> | null): JsonObject {
  const out: JsonObject = {}
  if (!value) return out
  for (const [k, v] of Object.entries(value)) {
    if (v !== undefined) out[k] = toJsonValue(v)
  }
  return out
}

function fromJsonObject(value?: JsonObject): Record<string, unknown> {
  if (!value) return {}
  return { ...value }
}

function decodeLocalizedParams(
  entries: Record<string, ErrorParamValue>,
): MessageParams | undefined {
  const keys = Object.keys(entries)
  if (keys.length === 0) return undefined
  const params: MessageParams = {}
  for (const key of keys) {
    const entry = entries[key]
    if (!entry) continue
    switch (entry.value.case) {
      case 'stringValue':
      case 'numberValue':
        params[key] = entry.value.value
        break
      case 'boolValue':
        params[key] = String(entry.value.value)
        break
      default:
        break
    }
  }
  return Object.keys(params).length > 0 ? params : undefined
}

function fromProtoLocalizedMessage(error: LocalizedError): LocalizedMessage {
  const params = decodeLocalizedParams(error.params)
  return {
    key: error.key as MessageKey,
    ...(params ? { params } : {}),
  }
}

// ---- Project mappers ----

function fromProtoUsageTotals(usage?: ProjectUsageTotals): UsageTotals {
  const cost = { USD: 0, CNY: 0 } as Record<'USD' | 'CNY', number>
  if (usage) {
    for (const [currency, amount] of Object.entries(usage.cost)) {
      if (currency === 'USD' || currency === 'CNY') {
        cost[currency] = amount
      }
    }
  }
  return {
    input: usage ? Number(usage.input) : 0,
    output: usage ? Number(usage.output) : 0,
    cached: usage ? Number(usage.cached) : 0,
    cacheWrite: usage ? Number(usage.cacheWrite) : 0,
    reasoning: usage ? Number(usage.reasoning) : 0,
    cost,
  }
}

function fromProtoProjectSource(source?: ProtoProjectSource): ProjectSource | null {
  if (!source) return null
  return {
    kind: source.kind,
    settings: fromJsonObject(source.settings),
    ...(source.hosting
      ? {
          hosting: {
            kind: source.hosting.kind,
            settings: fromJsonObject(source.hosting.settings),
          },
        }
      : {}),
  }
}

function fromProtoFileSource(source: ProtoFileSource): FileSource {
  return {
    id: source.id,
    name: source.name,
    kind: source.kind,
    settings: fromJsonObject(source.settings),
    ...(source.hosting
      ? {
          hosting: {
            kind: source.hosting.kind,
            settings: fromJsonObject(source.hosting.settings),
          },
        }
      : {}),
  }
}

function fromProtoFileSourceSet(set?: ProtoFileSourceSet): FileSourceSet | undefined {
  if (!set) return undefined
  return {
    primaryId: set.primaryId,
    sources: set.sources.map(fromProtoFileSource),
  }
}

function fromProtoProjectSettings(settings?: ProtoProjectSettings): ProjectSettings {
  return {
    instructions: settings?.instructions ?? '',
    delivery: {
      mode: (settings?.delivery?.mode || 'merge') as ProjectSettings['delivery']['mode'],
      instructions: settings?.delivery?.instructions ?? '',
    },
    tools: [...(settings?.tools ?? [])],
    budget: {
      thread: settings?.budget?.thread ?? null,
      project: settings?.budget?.project ?? null,
    },
    workspace: {
      retention: (settings?.workspace?.retention ||
        'auto') as ProjectSettings['workspace']['retention'],
      auto: {
        setup: settings?.workspace?.auto?.setup ?? false,
        check: settings?.workspace?.auto?.check ?? false,
      },
      setup: settings?.workspace?.setup ?? '',
      shell: (settings?.workspace?.shell || 'auto') as ProjectSettings['workspace']['shell'],
      check: settings?.workspace?.check ?? '',
      branchPrefix: settings?.workspace?.branchPrefix ?? '',
    },
    routines: (settings?.routines ?? []).map((routine) => ({
      id: routine.id,
      prompt: routine.prompt,
      days: [...routine.days],
      time: routine.time,
      enabled: routine.enabled,
    })),
    review: {
      enabled: settings?.review?.enabled ?? false,
    },
    autopilot: {
      enabled: settings?.autopilot?.enabled ?? false,
      method: (settings?.autopilot?.method || 'merge') as ProjectSettings['autopilot']['method'],
    },
  }
}

function toProtoProjectSettingsInput(settings: ProjectSettings) {
  return {
    instructions: settings.instructions,
    delivery: {
      mode: settings.delivery.mode,
      instructions: settings.delivery.instructions,
    },
    tools: [...settings.tools],
    budget: {
      ...(settings.budget.thread === null ? {} : { thread: settings.budget.thread }),
      ...(settings.budget.project === null ? {} : { project: settings.budget.project }),
    },
    workspace: {
      retention: settings.workspace.retention,
      auto: {
        setup: settings.workspace.auto.setup,
        check: settings.workspace.auto.check,
      },
      setup: settings.workspace.setup,
      shell: settings.workspace.shell,
      check: settings.workspace.check,
      branchPrefix: settings.workspace.branchPrefix,
    },
    routines: settings.routines.map((routine) => ({
      id: routine.id,
      prompt: routine.prompt,
      days: [...routine.days],
      time: routine.time,
      enabled: routine.enabled,
    })),
    review: {
      enabled: settings.review.enabled,
    },
    autopilot: {
      enabled: settings.autopilot.enabled,
      method: settings.autopilot.method,
    },
  }
}

function fromProtoProjectDetected(detected?: ProtoProjectDetected): ProjectDetected {
  return {
    setup: detected?.setup
      ? { command: detected.setup.command, threadId: detected.setup.threadId }
      : null,
    check: detected?.check
      ? { command: detected.check.command, threadId: detected.check.threadId }
      : null,
  }
}

function fromProtoProjectSettingsView(view?: ProtoProjectSettingsView): ProjectSettingsDataView {
  return {
    settings: fromProtoProjectSettings(view?.settings),
    detected: fromProtoProjectDetected(view?.detected),
    env: [...(view?.env ?? [])],
    tools: [...(view?.tools ?? [])] as ProjectSettingsDataView['tools'],
    hostedMerge: view?.hostedMerge ?? false,
  }
}

export function fromProtoProjectView(project: ProtoProjectView): ProjectView {
  const fileSources = fromProtoFileSourceSet(project.fileSources)
  return {
    id: project.id,
    name: project.name,
    unnamed: project.unnamed,
    coordinatorId: project.coordinatorId,
    createdAt: Number(project.createdAt),
    updatedAt: Number(project.updatedAt),
    needsMe: project.needsMe,
    ...(project.runningTurns === undefined ? {} : { runningTurns: project.runningTurns }),
    source: fromProtoProjectSource(project.source),
    ...(fileSources ? { fileSources } : {}),
    references: project.references.map((ref) => ({
      id: ref.id,
      name: ref.name,
      kind: ref.kind,
      settings: fromJsonObject(ref.settings),
    })),
    slots: project.slots ?? null,
    mcp: [...project.mcp],
    settings: fromProtoProjectSettings(project.settings),
    detected: fromProtoProjectDetected(project.detected),
    autopilotPaused: project.autopilotPaused,
    usage: fromProtoUsageTotals(project.usage),
  }
}

function fromProtoProjectThreadView(thread: ProjectThreadView): ThreadView {
  return {
    id: thread.id,
    projectId: thread.projectId,
    title: thread.title,
    nodeId: thread.nodeId ?? null,
    stage: thread.stage as ThreadView['stage'],
    lastReport: (thread.lastReport ?? null) as ThreadView['lastReport'],
    lastSummary: thread.lastSummary ?? null,
    lastHeadline: thread.lastHeadline ?? null,
    lastArtifacts: thread.lastArtifacts.map((art) => ({
      id: art.id,
      kind: art.kind as ArtifactKind,
      title: art.title,
    })),
    workspace: thread.workspace
      ? {
          ...(thread.workspace.sourceId === undefined
            ? {}
            : { sourceId: thread.workspace.sourceId }),
          label: thread.workspace.label,
          branch: thread.workspace.branch ?? null,
          base: thread.workspace.base ?? null,
          removed: thread.workspace.removed,
          parked: thread.workspace.parked,
        }
      : null,
    merged: thread.merged
      ? {
          branch: thread.merged.branch,
          base: thread.merged.base,
          method: thread.merged.method as NonNullable<ThreadView['merged']>['method'],
          sha: thread.merged.sha,
          head: thread.merged.head,
          ...(thread.merged.auto ? { auto: true } : {}),
          at: Number(thread.merged.at),
        }
      : null,
    archived: thread.archived,
    changes: thread.changes
      ? {
          files: thread.changes.files,
          added: thread.changes.added,
          removed: thread.changes.removed,
        }
      : null,
    pullRequest: thread.pullRequest
      ? {
          number: Number(thread.pullRequest.number),
          ...(thread.pullRequest.reference === undefined
            ? {}
            : { reference: thread.pullRequest.reference }),
          url: thread.pullRequest.url,
          title: thread.pullRequest.title,
          state: thread.pullRequest.state as NonNullable<ThreadView['pullRequest']>['state'],
          ...(thread.pullRequest.conflicted ? { conflicted: true } : {}),
          headSha: thread.pullRequest.headSha,
          checks: {
            state: (thread.pullRequest.checks?.state || 'none') as NonNullable<
              ThreadView['pullRequest']
            >['checks']['state'],
            failed: [...(thread.pullRequest.checks?.failed ?? [])],
          },
        }
      : null,
    aside: thread.aside
      ? {
          until: Number(thread.aside.until),
          reason: thread.aside.reason,
          by: thread.aside.by,
          due: thread.aside.due,
        }
      : null,
    stopped: thread.stopped
      ? {
          reason: thread.stopped.reason,
          by: thread.stopped.by,
          at: Number(thread.stopped.at),
        }
      : null,
    references: thread.references.map((ref) => ({
      id: ref.id,
      name: ref.name,
      kind: ref.kind,
      settings: fromJsonObject(ref.settings),
      writable: ref.writable,
      attentionId: ref.attentionId,
    })),
    endedReferences: thread.endedReferences.map((ref) => ({
      attentionId: ref.attentionId,
      kept: ref.kept,
    })),
    model: thread.model ?? null,
    effort: thread.effort ?? null,
    branchSlug: thread.branchSlug ?? null,
    usage: fromProtoUsageTotals(thread.usage),
    progress: thread.progress ? { done: thread.progress.done, total: thread.progress.total } : null,
    doing: thread.doing ?? null,
    createdAt: Number(thread.createdAt),
    updatedAt: Number(thread.updatedAt),
    activeAt: Number(thread.activeAt),
    reviewOf: thread.reviewOf ?? null,
    review: thread.review
      ? {
          reviewerId: thread.review.reviewerId,
          rounds: Number(thread.review.rounds),
          reviewing: thread.review.reviewing,
        }
      : null,
    auto: thread.auto,
    autopilotHeld: thread.autopilotHeld ?? null,
  }
}

function fromProtoFormValue(value?: ProjectFormValue): FormValue {
  if (!value) return null
  switch (value.value.case) {
    case 'stringValue':
    case 'numberValue':
    case 'boolValue':
      return value.value.value
    case 'stringList':
      return [...value.value.value.values]
    default:
      return null
  }
}

function fromProtoAskBlock(block?: ProjectAskBlock): AskBlock {
  if (!block) {
    return {
      type: 'confirm',
      title: '',
      description: null,
      detail: null,
      tone: 'neutral',
      approveText: null,
      denyText: null,
    }
  }
  switch (block.block.case) {
    case 'choice':
      return {
        type: 'choice',
        question: block.block.value.question,
        options: block.block.value.options.map((opt) => ({
          label: opt.label,
          consequence: opt.consequence,
        })),
        recommended: block.block.value.recommended ?? null,
      }
    case 'confirm':
      return {
        type: 'confirm',
        title: block.block.value.title,
        description: block.block.value.description ?? null,
        detail: block.block.value.detail ?? null,
        tone: (block.block.value.tone || 'neutral') as 'neutral' | 'warning' | 'danger',
        approveText: block.block.value.approveText ?? null,
        denyText: block.block.value.denyText ?? null,
        ...(block.block.value.allowProjectReference ? { allowProjectReference: true } : {}),
      }
    case 'form':
      return {
        type: 'form',
        title: block.block.value.title,
        description: block.block.value.description ?? null,
        fields: block.block.value.fields.map((field): FormField => ({
          key: field.key,
          label: field.label,
          kind: field.kind as FormField['kind'],
          required: field.required,
          placeholder: field.placeholder ?? null,
          options: field.options.map((opt) => ({ label: opt.label, value: opt.value })),
          default: fromProtoFormValue(field.defaultValue),
          min: field.min ?? null,
          max: field.max ?? null,
        })),
        submitText: block.block.value.submitText ?? null,
      }
    default:
      return {
        type: 'confirm',
        title: '',
        description: null,
        detail: null,
        tone: 'neutral',
        approveText: null,
        denyText: null,
      }
  }
}

function fromProtoNeedsMeItem(item: ProtoNeedsMeItem): NeedsMeItem | null {
  switch (item.item.case) {
    case 'attention': {
      const att = item.item.value.attention
      if (!att) return null
      return {
        kind: 'attention',
        attention: {
          id: att.id,
          projectId: att.projectId,
          threadId: att.threadId ?? null,
          joinedBy: [...att.joinedBy],
          block: fromProtoAskBlock(att.block),
          messageId: att.messageId ?? null,
          conversationId: att.conversationId ?? null,
          createdAt: Number(att.createdAt),
        },
        threadId: item.item.value.threadId ?? null,
        joinedIds: [...item.item.value.joinedIds],
      }
    }
    case 'thread':
      return { kind: 'thread', threadId: item.item.value.threadId }
    default:
      return null
  }
}

// ---- Plan mappers ----

function fromProtoPlanOp(op: ProtoPlanOp): PlanOp | null {
  switch (op.op.case) {
    case 'add':
      return {
        op: 'add',
        id: op.op.value.id,
        kind: op.op.value.kind as 'task' | 'milestone',
        title: op.op.value.title,
        description: op.op.value.description,
        acceptance: op.op.value.acceptance,
        ...(op.op.value.group ? { group: op.op.value.group } : {}),
      }
    case 'update':
      return {
        op: 'update',
        id: op.op.value.id,
        ...(op.op.value.title === undefined ? {} : { title: op.op.value.title }),
        ...(op.op.value.description === undefined ? {} : { description: op.op.value.description }),
        ...(op.op.value.acceptance === undefined ? {} : { acceptance: op.op.value.acceptance }),
        ...(op.op.value.group === undefined ? {} : { group: op.op.value.group }),
        ...(op.op.value.expect
          ? {
              expect: {
                ...(op.op.value.expect.title === undefined
                  ? {}
                  : { title: op.op.value.expect.title }),
                ...(op.op.value.expect.description === undefined
                  ? {}
                  : { description: op.op.value.expect.description }),
                ...(op.op.value.expect.acceptance === undefined
                  ? {}
                  : { acceptance: op.op.value.expect.acceptance }),
                ...(op.op.value.expect.group === undefined
                  ? {}
                  : { group: op.op.value.expect.group }),
              },
            }
          : {}),
      }
    case 'remove':
      return { op: 'remove', id: op.op.value.id }
    case 'link':
      return { op: 'link', from: op.op.value.from, to: op.op.value.to }
    case 'unlink':
      return { op: 'unlink', from: op.op.value.from, to: op.op.value.to }
    default:
      return null
  }
}

function toProtoPlanOpInput(op: PlanOp) {
  switch (op.op) {
    case 'add':
      return {
        op: {
          case: 'add' as const,
          value: {
            id: op.id,
            kind: op.kind,
            title: op.title,
            description: op.description,
            acceptance: op.acceptance,
            ...(op.group === undefined ? {} : { group: op.group }),
          },
        },
      }
    case 'update':
      return {
        op: {
          case: 'update' as const,
          value: {
            id: op.id,
            ...(op.title === undefined ? {} : { title: op.title }),
            ...(op.description === undefined ? {} : { description: op.description }),
            ...(op.acceptance === undefined ? {} : { acceptance: op.acceptance }),
            ...(op.group === undefined ? {} : { group: op.group }),
            ...(op.expect
              ? {
                  expect: {
                    ...(op.expect.title === undefined ? {} : { title: op.expect.title }),
                    ...(op.expect.description === undefined
                      ? {}
                      : { description: op.expect.description }),
                    ...(op.expect.acceptance === undefined
                      ? {}
                      : { acceptance: op.expect.acceptance }),
                    ...(op.expect.group === undefined ? {} : { group: op.expect.group }),
                  },
                }
              : {}),
          },
        },
      }
    case 'remove':
      return {
        op: {
          case: 'remove' as const,
          value: { id: op.id },
        },
      }
    case 'link':
      return {
        op: {
          case: 'link' as const,
          value: { from: op.from, to: op.to },
        },
      }
    case 'unlink':
      return {
        op: {
          case: 'unlink' as const,
          value: { from: op.from, to: op.to },
        },
      }
  }
}

function fromProtoPlanRevision(rev: ProtoPlanRevisionView): PlanRevisionView {
  return {
    id: rev.id,
    author: rev.author as PlanRevisionView['author'],
    reason: rev.reason,
    ops: rev.ops.map(fromProtoPlanOp).filter((op): op is PlanOp => op !== null),
    revertOf: rev.revertOf ?? null,
    revertedBy: rev.revertedBy ?? null,
    createdAt: Number(rev.createdAt),
  }
}

function fromProtoPlanPreview(preview?: ProtoPlanPreview): PlanPreview | null {
  if (!preview) return null
  return {
    nodes: preview.nodes.map((node) => ({
      id: node.id,
      kind: node.kind as 'task' | 'milestone',
      title: node.title,
      description: node.description,
      acceptance: node.acceptance,
      ...(node.group ? { group: node.group } : {}),
      mark: (node.mark ?? null) as PlanPreview['nodes'][number]['mark'],
      dependsOn: [...node.dependsOn],
    })),
    edges: preview.edges.map((edge) => ({
      from: edge.from,
      to: edge.to,
      mark: (edge.mark ?? null) as PlanPreview['edges'][number]['mark'],
    })),
  }
}

function fromProtoPlanProposal(proposal: ProtoPlanProposalView): PlanProposalView {
  return {
    id: proposal.id,
    attentionId: proposal.attentionId,
    reason: proposal.reason,
    ops: proposal.ops.map(fromProtoPlanOp).filter((op): op is PlanOp => op !== null),
    changes: proposal.changes.map(fromProtoLocalizedMessage),
    preview: fromProtoPlanPreview(proposal.preview),
    status: proposal.status as PlanProposalView['status'],
    staleReason: proposal.staleReason ?? null,
    createdAt: Number(proposal.createdAt),
  }
}

function fromProtoPlanNode(node: ProtoPlanNodeView): PlanNodeView {
  return {
    id: node.id,
    kind: node.kind as PlanNodeView['kind'],
    title: node.title,
    description: node.description,
    acceptance: node.acceptance,
    ...(node.group ? { group: node.group } : {}),
    dependsOn: [...node.dependsOn],
    status: node.status as PlanNodeView['status'],
    threads: node.threads.map((thread) => ({
      id: thread.id,
      title: thread.title,
      stage: thread.stage as PlanNodeView['threads'][number]['stage'],
      stoppedBy: thread.stoppedBy ?? null,
      lastHeadline: thread.lastHeadline ?? null,
      pullRequest: thread.pullRequest
        ? {
            number: Number(thread.pullRequest.number),
            ...(thread.pullRequest.reference === undefined
              ? {}
              : { reference: thread.pullRequest.reference }),
            url: thread.pullRequest.url,
            state: thread.pullRequest.state as NonNullable<
              PlanNodeView['threads'][number]['pullRequest']
            >['state'],
            ...(thread.pullRequest.conflicted ? { conflicted: true } : {}),
            checks: {
              state: (thread.pullRequest.checks?.state || 'none') as NonNullable<
                PlanNodeView['threads'][number]['pullRequest']
              >['checks']['state'],
              failed: [...(thread.pullRequest.checks?.failed ?? [])],
            },
          }
        : null,
    })),
    changedIn: node.changedIn ?? null,
    issues: node.issues.map((issue) => ({
      tracker: issue.tracker,
      number: Number(issue.number),
      title: issue.title,
      url: issue.url,
      linkedBy: issue.linkedBy,
    })),
    gate: (node.gate ?? null) as PlanNodeView['gate'],
  }
}

export function fromProtoPlanView(plan?: ProtoPlanView): PlanView {
  return {
    projectId: plan?.projectId ?? '',
    nodes: (plan?.nodes ?? []).map(fromProtoPlanNode),
    revisions: (plan?.revisions ?? []).map(fromProtoPlanRevision),
    proposals: (plan?.proposals ?? []).map(fromProtoPlanProposal),
  }
}

function fromProtoPlanUndoPreview(preview?: ProtoPlanUndoPreview): PlanUndoPreview {
  return {
    changes: (preview?.changes ?? []).map(fromProtoLocalizedMessage),
    threads: (preview?.threads ?? []).map((t) => ({ id: t.id, title: t.title })),
    blocked: preview?.blocked ? fromProtoLocalizedMessage(preview.blocked) : null,
  }
}

// ---- Artifact mappers ----

function fromProtoArtifactPreview(preview?: ProtoArtifactPreview): ArtifactPreview {
  if (!preview) {
    return { kind: 'document', excerpt: '', lead: '' }
  }
  switch (preview.preview.case) {
    case 'document':
      return {
        kind: 'document',
        excerpt: preview.preview.value.excerpt,
        lead: preview.preview.value.lead,
      }
    case 'whiteboard':
      return {
        kind: 'whiteboard',
        items: preview.preview.value.items,
        connectors: preview.preview.value.connectors,
        texts: [...preview.preview.value.texts],
      }
    case 'dashboard':
      return {
        kind: 'dashboard',
        widgets: preview.preview.value.widgets,
        takenAt:
          preview.preview.value.takenAt === undefined
            ? null
            : Number(preview.preview.value.takenAt),
        titles: [...preview.preview.value.titles],
        tiles: preview.preview.value.tiles.map((tile) => ({
          type: tile.type,
          x: tile.x,
          y: tile.y,
          w: tile.w,
          h: tile.h,
          title: tile.title ?? null,
        })),
      }
    default:
      return { kind: 'document', excerpt: '', lead: '' }
  }
}

function fromProtoArtifactSummary(item: ProtoArtifactSummary): ArtifactSummary {
  return {
    id: item.id,
    projectId: item.projectId,
    kind: item.kind as ArtifactKind,
    title: item.title,
    nodeId: item.nodeId ?? null,
    version: Number(item.version),
    createdBy: item.createdBy,
    updatedBy: item.updatedBy,
    createdAt: Number(item.createdAt),
    updatedAt: Number(item.updatedAt),
    preview: fromProtoArtifactPreview(item.preview),
    openComments: Number(item.openComments),
    commentedAt: Number(item.commentedAt),
  }
}

export function fromProtoArtifactView(item: ProtoArtifactView): ArtifactView {
  return {
    id: item.id,
    projectId: item.projectId,
    kind: item.kind as ArtifactKind,
    title: item.title,
    nodeId: item.nodeId ?? null,
    version: Number(item.version),
    createdBy: item.createdBy,
    updatedBy: item.updatedBy,
    createdAt: Number(item.createdAt),
    updatedAt: Number(item.updatedAt),
    preview: fromProtoArtifactPreview(item.preview),
    openComments: Number(item.openComments),
    commentedAt: Number(item.commentedAt),
    content: item.content,
  }
}

function fromProtoArtifactVersion(version: ProtoArtifactVersionView): ArtifactVersionView {
  return {
    version: Number(version.version),
    title: version.title,
    author: version.author,
    reason: version.reason,
    createdAt: Number(version.createdAt),
  }
}

function fromProtoArtifactCommentAnchor(
  anchor?: ProtoArtifactCommentAnchor,
): ArtifactCommentAnchor | null {
  if (!anchor) return null
  const element = anchor.element
    ? {
        ...(anchor.element.label ? { label: anchor.element.label } : {}),
        ...(anchor.element.role ? { role: anchor.element.role } : {}),
        ...(anchor.element.tag ? { tag: anchor.element.tag } : {}),
        ...(anchor.element.selector ? { selector: anchor.element.selector } : {}),
        ...(anchor.element.rect
          ? {
              rect: {
                x: anchor.element.rect.x,
                y: anchor.element.rect.y,
                w: anchor.element.rect.w,
                h: anchor.element.rect.h,
              },
            }
          : {}),
      }
    : undefined
  const extra = {
    ...(anchor.block === undefined ? {} : { block: anchor.block }),
    ...(element && Object.keys(element).length > 0 ? { element } : {}),
  }
  switch (anchor.target.case) {
    case 'node':
      return { node: anchor.target.value, ...extra }
    case 'edge':
      return { edge: anchor.target.value, ...extra }
    default:
      return null
  }
}

function toProtoArtifactCommentAnchorInput(anchor: ArtifactCommentAnchor) {
  const element = anchor.element
    ? {
        ...(anchor.element.label ? { label: anchor.element.label } : {}),
        ...(anchor.element.role ? { role: anchor.element.role } : {}),
        ...(anchor.element.tag ? { tag: anchor.element.tag } : {}),
        ...(anchor.element.selector ? { selector: anchor.element.selector } : {}),
        ...(anchor.element.rect ? { rect: anchor.element.rect } : {}),
      }
    : undefined
  const extra = {
    ...(anchor.block === undefined ? {} : { block: anchor.block }),
    ...(element && Object.keys(element).length > 0 ? { element } : {}),
  }
  if ('node' in anchor) {
    return {
      target: { case: 'node' as const, value: anchor.node },
      ...extra,
    }
  }
  return {
    target: { case: 'edge' as const, value: anchor.edge },
    ...extra,
  }
}

function fromProtoArtifactCommentThread(
  thread: ProtoArtifactCommentThreadView,
): ArtifactCommentThreadView {
  return {
    id: thread.id,
    artifactId: thread.artifactId,
    quote: thread.quote,
    anchor: fromProtoArtifactCommentAnchor(thread.anchor),
    comments: thread.comments.map((comment) => ({
      id: comment.id,
      author: comment.author,
      body: comment.body,
      createdAt: Number(comment.createdAt),
      editedAt: comment.editedAt === undefined ? null : Number(comment.editedAt),
    })),
    resolved: thread.resolved ? { by: thread.resolved.by, at: Number(thread.resolved.at) } : null,
    createdAt: Number(thread.createdAt),
  }
}

function fromProtoArtifactFeedback(
  feedback?: ProtoArtifactFeedbackView,
): ArtifactFeedbackView | null {
  if (!feedback) return null
  const items: ArtifactFeedbackItem[] = []
  for (const item of feedback.items) {
    switch (item.item.case) {
      case 'edit':
        items.push({ kind: 'edit', diff: item.item.value.diff })
        break
      case 'choice':
        items.push({
          kind: 'choice',
          chosen: item.item.value.chosen.map((opt) => ({
            id: opt.id,
            title: opt.title,
            ...(opt.group ? { group: opt.group } : {}),
            ...(opt.groupTitle ? { groupTitle: opt.groupTitle } : {}),
          })),
        })
        break
      case 'comment':
        items.push({
          kind: 'comment',
          threadId: item.item.value.threadId,
          action: item.item.value.action as 'create' | 'reply' | 'edit' | 'reopen',
          body: item.item.value.body,
          where: item.item.value.where,
        })
        break
      case 'mark':
        items.push({
          kind: 'mark',
          nodeId: item.item.value.nodeId,
          type: item.item.value.type as 'highlight' | 'draw' | 'note',
          text: item.item.value.text,
          where: item.item.value.where,
        })
        break
      case 'board':
        items.push({ kind: 'board', changes: [...item.item.value.changes] })
        break
      case 'answer':
        items.push({
          kind: 'answer',
          answers: item.item.value.answers.map((a) => ({
            id: a.id,
            question: a.question,
            values: [...a.values],
            ...(a.note ? { note: a.note } : {}),
          })),
        })
        break
      case 'tweak':
        items.push({
          kind: 'tweak',
          tweaks: item.item.value.tweaks.map((entry) => ({
            nodeId: entry.nodeId,
            title: entry.title,
            ...(entry.optionId ? { optionId: entry.optionId } : {}),
            ...(entry.optionTitle ? { optionTitle: entry.optionTitle } : {}),
            changes: entry.changes.map((c) => ({
              key: c.key,
              label: c.label,
              value: c.value,
              ...(c.previous !== undefined ? { previous: c.previous } : {}),
              ...(c.unit ? { unit: c.unit } : {}),
            })),
          })),
        })
        break
      default:
        break
    }
  }
  return {
    artifactId: feedback.artifactId,
    round: Number(feedback.round),
    version: Number(feedback.version),
    notify: feedback.notify,
    items,
    images: feedback.images.map((img) => ({
      fileId: img.fileId,
      name: img.name,
      mime: img.mime,
      size: Number(img.size),
    })),
    doneBy: feedback.doneBy ?? null,
    doneAt: feedback.doneAt === undefined ? null : Number(feedback.doneAt),
    summary: feedback.summary ?? null,
    createdAt: Number(feedback.createdAt),
  }
}

// ---- Issue mappers ----

function fromProtoIssueSummary(issue: ProtoIssueSummary): IssueSummaryView {
  return {
    number: Number(issue.number),
    url: issue.url,
    title: issue.title,
    state: issue.state,
    author: issue.author ?? null,
    labels: [...issue.labels],
    comments: issue.comments,
    updatedAt: issue.updatedAt,
  }
}

function fromProtoIssueDetail(detail: ProtoIssueDetail): IssueDetailView {
  const summary = detail.summary
    ? fromProtoIssueSummary(detail.summary)
    : {
        number: 0,
        url: '',
        title: '',
        state: '',
        author: null,
        labels: [],
        comments: 0,
        updatedAt: '',
      }
  return {
    ...summary,
    body: detail.body,
    createdAt: detail.createdAt,
    recent: detail.recent.map((comment) => ({
      id: comment.id,
      author: comment.author ?? null,
      body: comment.body,
      createdAt: comment.createdAt,
      url: comment.url,
    })),
  }
}

// ---- Service client factory (reusable for tests with custom transport) ----

export interface Domain2Clients {
  projects: Client<typeof ProjectService>
  plans: Client<typeof PlanService>
  artifacts: Client<typeof ArtifactService>
  issues: Client<typeof IssueService>
}

function requireField<T>(value: T | undefined, reason: string): T {
  if (value === undefined) {
    throw new ApiError(500, { key: 'error.invalidBody', params: { reason } })
  }
  return value
}

export function createDomain2Api(transport: Transport = rpcTransport) {
  const clients: Domain2Clients = {
    projects: createRpcClient(ProjectService, transport),
    plans: createRpcClient(PlanService, transport),
    artifacts: createRpcClient(ArtifactService, transport),
    issues: createRpcClient(IssueService, transport),
  }

  return {
    // ---- Projects ----
    async listProjects(
      ids: readonly string[] = [],
    ): Promise<{ projects: ProjectView[]; lastSeq: number }> {
      const response = await clients.projects.listProjects({ ids: [...ids] })
      return {
        projects: response.projects.map(fromProtoProjectView),
        lastSeq: Number(response.lastSeq),
      }
    },

    async getProject(id: string, since?: string): Promise<ProjectDetailData> {
      const response = await clients.projects.getProject({
        projectId: id,
        since: since ?? '',
      })
      const project = fromProtoProjectView(requireField(response.project, 'project'))
      return {
        project,
        threads: response.threads.map(fromProtoProjectThreadView),
        needsMe: response.needsMe
          .map(fromProtoNeedsMeItem)
          .filter((item): item is NeedsMeItem => item !== null),
        hasTracker: response.hasTracker,
        sync: {
          cursor: response.sync?.cursor ?? '',
          partial: response.sync?.partial ?? false,
          ...(response.sync?.ids ? { ids: [...response.sync.ids.values] } : {}),
        },
      }
    },

    async createProject(input: {
      name: string
      source?: SourceInputData
      references?: { kind: string; settings: Record<string, unknown> }[]
      sources?: (SourceInputData & { id?: string })[]
      primaryIndex?: number
    }): Promise<{ project: ProjectView }> {
      const response = await clients.projects.createProject({
        name: input.name,
        message: '',
        ...(input.source
          ? {
              source: {
                kind: input.source.kind,
                settings: toJsonObject(input.source.settings),
                ...(input.source.hosting
                  ? {
                      hosting: {
                        kind: input.source.hosting.kind,
                        settings: toJsonObject(input.source.hosting.settings),
                      },
                    }
                  : {}),
              },
            }
          : {}),
        references: (input.references ?? []).map((ref) => ({
          kind: ref.kind,
          settings: toJsonObject(ref.settings),
        })),
        sources: (input.sources ?? []).map((src) => ({
          ...(src.id === undefined ? {} : { id: src.id }),
          kind: src.kind,
          settings: toJsonObject(src.settings),
          ...(src.hosting
            ? {
                hosting: {
                  kind: src.hosting.kind,
                  settings: toJsonObject(src.hosting.settings),
                },
              }
            : {}),
        })),
        hasSources: input.sources !== undefined,
        ...(input.primaryIndex === undefined ? {} : { primaryIndex: input.primaryIndex }),
      })
      return { project: fromProtoProjectView(requireField(response.project, 'project')) }
    },

    async updateProject(
      projectId: string,
      patch: UpdateProjectPatch,
    ): Promise<ProjectSettingsDataView & { project: ProjectView }> {
      const response = await clients.projects.updateProject({
        projectId,
        ...(patch.name === undefined ? {} : { name: patch.name }),
        ...(patch.settings === undefined
          ? {}
          : { settings: toProtoProjectSettingsInput(patch.settings) }),
        ...(patch.env === undefined ? {} : { env: { entries: patch.env } }),
        ...(patch.slots === undefined
          ? {}
          : patch.slots === null
            ? { slotsAction: { case: 'clearSlots' as const, value: true } }
            : { slotsAction: { case: 'setSlots' as const, value: patch.slots } }),
        ...(patch.autopilotPaused === undefined ? {} : { autopilotPaused: patch.autopilotPaused }),
        ...(patch.mcp === undefined ? {} : { mcpServers: { values: [...patch.mcp] } }),
      })
      return {
        ...fromProtoProjectSettingsView(response.settingsView),
        project: fromProtoProjectView(requireField(response.project, 'project')),
      }
    },

    async deleteProject(id: string): Promise<void> {
      await clients.projects.deleteProject({ projectId: id })
    },

    async getProjectActivity(id: string, signal?: AbortSignal): Promise<ProjectActivity> {
      const response = await clients.projects.getProjectActivity(
        { projectId: id },
        signal ? { signal } : undefined,
      )
      return {
        coordinator: {
          status: (response.coordinator?.status ||
            'idle') as ProjectActivity['coordinator']['status'],
          ...(response.coordinator?.progress
            ? {
                progress: {
                  done: response.coordinator.progress.done,
                  total: response.coordinator.progress.total,
                },
              }
            : {}),
        },
        threads: response.threads.map((thread) => ({
          id: thread.id,
          title: thread.title,
          status: thread.status as ProjectActivity['threads'][number]['status'],
          ...(thread.progress
            ? { progress: { done: thread.progress.done, total: thread.progress.total } }
            : {}),
        })),
      }
    },

    async searchProject(projectId: string, q: string): Promise<SearchResult> {
      const response = await clients.projects.searchProject({ projectId, query: q })
      return {
        terms: [...response.terms],
        threads: response.threads.map((hit) => ({
          kind: 'thread' as const,
          id: hit.id,
          title: hit.title,
          snippet: hit.snippet,
          at: Number(hit.at),
        })),
        artifacts: response.artifacts.map((hit) => ({
          kind: 'artifact' as const,
          id: hit.id,
          artifactKind: hit.artifactKind as ArtifactKind,
          title: hit.title,
          snippet: hit.snippet,
          at: Number(hit.at),
        })),
        messages: response.messages.map((hit) => ({
          kind: 'message' as const,
          id: hit.id,
          conversationId: hit.conversationId,
          where: hit.where ?? null,
          snippet: hit.snippet,
          at: Number(hit.at),
        })),
      }
    },

    async getProjectSettings(projectId: string): Promise<ProjectSettingsDataView> {
      const response = await clients.projects.getProjectSettings({ projectId })
      return fromProtoProjectSettingsView(response.settingsView)
    },

    async getFileSources(
      projectId: string,
    ): Promise<FileSourceSet & { secretsSet: Record<string, string[]> }> {
      const response = await clients.projects.getFileSources({ projectId })
      const secretsSet: Record<string, string[]> = {}
      for (const [id, list] of Object.entries(response.secretsSet)) {
        secretsSet[id] = [...list.values]
      }
      return {
        primaryId: response.primaryId,
        sources: response.sources.map(fromProtoFileSource),
        secretsSet,
      }
    },

    async setFileSources(
      projectId: string,
      body: { sources: (SourceInputData & { id?: string })[]; primaryIndex?: number },
    ): Promise<{ project: ProjectView }> {
      const response = await clients.projects.setFileSources({
        projectId,
        sources: body.sources.map((src) => ({
          ...(src.id === undefined ? {} : { id: src.id }),
          kind: src.kind,
          settings: toJsonObject(src.settings),
          ...(src.hosting
            ? {
                hosting: {
                  kind: src.hosting.kind,
                  settings: toJsonObject(src.hosting.settings),
                },
              }
            : {}),
        })),
        ...(body.primaryIndex === undefined ? {} : { primaryIndex: body.primaryIndex }),
      })
      return { project: fromProtoProjectView(requireField(response.project, 'project')) }
    },

    async addReference(
      projectId: string,
      reference: { kind: string; settings: Record<string, unknown> },
    ): Promise<{ project: ProjectView }> {
      const response = await clients.projects.addProjectReference({
        projectId,
        reference: {
          kind: reference.kind,
          settings: toJsonObject(reference.settings),
        },
      })
      return { project: fromProtoProjectView(requireField(response.project, 'project')) }
    },

    async removeReference(
      projectId: string,
      referenceId: string,
    ): Promise<{ project: ProjectView }> {
      const response = await clients.projects.removeProjectReference({
        projectId,
        referenceId,
      })
      return { project: fromProtoProjectView(requireField(response.project, 'project')) }
    },

    // ---- Plans ----
    async getPlan(
      projectId: string,
      options: { revisionPageSize?: number; revisionPageToken?: string } = {},
    ): Promise<PlanPageResult> {
      const response = await clients.plans.getPlan({
        projectId,
        revisionPageSize: options.revisionPageSize ?? 0,
        revisionPageToken: options.revisionPageToken ?? '',
      })
      return {
        plan: fromProtoPlanView(response.plan),
        nextRevisionPageToken: response.nextRevisionPageToken,
        hasMoreRevisions: response.hasMoreRevisions,
      }
    },

    async listPlanRevisions(
      projectId: string,
      options: { pageSize?: number; pageToken?: string; signal?: AbortSignal } = {},
    ): Promise<PlanRevisionsPageResult> {
      const response = await clients.plans.listPlanRevisions(
        {
          projectId,
          pageSize: options.pageSize ?? 0,
          pageToken: options.pageToken ?? '',
        },
        options.signal ? { signal: options.signal } : undefined,
      )
      return {
        revisions: response.revisions.map(fromProtoPlanRevision),
        nextPageToken: response.nextPageToken,
        hasMore: response.hasMore,
      }
    },

    async revisePlan(
      projectId: string,
      input: { reason: string; ops: PlanOp[] },
    ): Promise<{ plan: PlanView }> {
      const response = await clients.plans.revisePlan({
        projectId,
        reason: input.reason,
        ops: input.ops.map(toProtoPlanOpInput),
      })
      return { plan: fromProtoPlanView(response.plan) }
    },

    async dismissProposal(projectId: string, proposalId: string): Promise<{ plan: PlanView }> {
      const response = await clients.plans.dismissPlanProposal({ projectId, proposalId })
      return { plan: fromProtoPlanView(response.plan) }
    },

    async previewPlanUndo(
      projectId: string,
      revisionId: string,
    ): Promise<{ preview: PlanUndoPreview }> {
      const response = await clients.plans.previewPlanUndo({ projectId, revisionId })
      return { preview: fromProtoPlanUndoPreview(response.preview) }
    },

    async revertPlan(
      projectId: string,
      revisionId: string,
      reason: string,
    ): Promise<{ plan: PlanView }> {
      const response = await clients.plans.revertPlanRevision({ projectId, revisionId, reason })
      return { plan: fromProtoPlanView(response.plan) }
    },

    async linkIssue(
      projectId: string,
      nodeId: string,
      number: number,
    ): Promise<{ plan: PlanView }> {
      const response = await clients.plans.linkPlanNodeIssue({
        projectId,
        nodeId,
        number: BigInt(number),
      })
      return { plan: fromProtoPlanView(response.plan) }
    },

    async unlinkIssue(
      projectId: string,
      nodeId: string,
      number: number,
    ): Promise<{ plan: PlanView }> {
      const response = await clients.plans.unlinkPlanNodeIssue({
        projectId,
        nodeId,
        number: BigInt(number),
      })
      return { plan: fromProtoPlanView(response.plan) }
    },

    // ---- Artifacts ----
    async listArtifacts(
      projectId: string,
      options: { pageSize?: number; pageToken?: string; signal?: AbortSignal } = {},
    ): Promise<PaginatedArtifactsResult> {
      const response = await clients.artifacts.listArtifacts(
        {
          projectId,
          pageSize: options.pageSize ?? 0,
          pageToken: options.pageToken ?? '',
        },
        options.signal ? { signal: options.signal } : undefined,
      )
      return {
        artifacts: response.artifacts.map(fromProtoArtifactSummary),
        nextPageToken: response.nextPageToken,
        hasMore: response.hasMore,
      }
    },

    async createArtifact(
      projectId: string,
      input: { kind: ArtifactKind; title: string },
    ): Promise<ArtifactDetailResult> {
      const response = await clients.artifacts.createArtifact({
        projectId,
        kind: input.kind,
        title: input.title,
      })
      return {
        artifact: fromProtoArtifactView(requireField(response.artifact, 'artifact')),
        versions: response.versions.map(fromProtoArtifactVersion),
        comments: response.comments.map(fromProtoArtifactCommentThread),
      }
    },

    async getArtifact(
      id: string,
      options: {
        version?: number
        signal?: AbortSignal
        versions?: boolean
        versionPageSize?: number
        versionPageToken?: string
      } = {},
    ): Promise<ArtifactDetailResult> {
      const response = await clients.artifacts.getArtifact(
        {
          artifactId: id,
          ...(options.version === undefined ? {} : { version: BigInt(options.version) }),
          includeVersions: options.versions !== false,
          versionPageSize: options.versionPageSize ?? 0,
          versionPageToken: options.versionPageToken ?? '',
        },
        options.signal ? { signal: options.signal } : undefined,
      )
      return {
        artifact: fromProtoArtifactView(requireField(response.artifact, 'artifact')),
        versions: response.versions.map(fromProtoArtifactVersion),
        comments: response.comments.map(fromProtoArtifactCommentThread),
        nextVersionPageToken: response.nextVersionPageToken,
        hasMoreVersions: response.hasMoreVersions,
      }
    },

    async listArtifactVersions(
      id: string,
      options: { pageSize?: number; pageToken?: string; signal?: AbortSignal } = {},
    ): Promise<PaginatedArtifactVersionsResult> {
      const response = await clients.artifacts.listArtifactVersions(
        {
          artifactId: id,
          pageSize: options.pageSize ?? 0,
          pageToken: options.pageToken ?? '',
        },
        options.signal ? { signal: options.signal } : undefined,
      )
      return {
        versions: response.versions.map(fromProtoArtifactVersion),
        nextPageToken: response.nextPageToken,
        hasMore: response.hasMore,
      }
    },

    async saveArtifact(
      id: string,
      input: {
        content: string | null
        baseVersion: number
        reason: string
        title?: string
        quotes?: Record<string, string>
        includeVersions?: boolean
      },
    ): Promise<ArtifactDetailResult> {
      const response = await clients.artifacts.saveArtifact({
        artifactId: id,
        ...(input.content === null ? {} : { content: input.content }),
        baseVersion: BigInt(input.baseVersion),
        reason: input.reason,
        ...(input.title === undefined ? {} : { title: input.title }),
        quotes: input.quotes ?? {},
        includeVersions: input.includeVersions ?? true,
      })
      return {
        artifact: fromProtoArtifactView(requireField(response.artifact, 'artifact')),
        versions: response.versions.map(fromProtoArtifactVersion),
        comments: response.comments.map(fromProtoArtifactCommentThread),
      }
    },

    async refreshDashboard(artifactId: string): Promise<{ artifact: ArtifactView }> {
      const response = await clients.artifacts.refreshDashboard({ artifactId })
      return { artifact: fromProtoArtifactView(requireField(response.artifact, 'artifact')) }
    },

    async sendArtifactFeedback(
      id: string,
      images: string[],
    ): Promise<{ feedback: ArtifactFeedbackView | null }> {
      const response = await clients.artifacts.sendArtifactFeedback({
        artifactId: id,
        images: [...images],
      })
      return { feedback: fromProtoArtifactFeedback(response.feedback) }
    },

    async withdrawArtifactFeedback(id: string, round: number): Promise<{ withdrawn: boolean }> {
      const response = await clients.artifacts.withdrawArtifactFeedback({
        artifactId: id,
        round: BigInt(round),
      })
      return { withdrawn: response.withdrawn }
    },

    // D-5: Dedicated comment RPCs
    async createArtifactComment(
      artifactId: string,
      input: {
        threadId: string
        commentId: string
        quote: string
        body: string
        anchor?: ArtifactCommentAnchor
      },
    ): Promise<{ comments: ArtifactCommentThreadView[] }> {
      const response = await clients.artifacts.createArtifactComment({
        artifactId,
        threadId: input.threadId,
        commentId: input.commentId,
        quote: input.quote,
        body: input.body,
        ...(input.anchor ? { anchor: toProtoArtifactCommentAnchorInput(input.anchor) } : {}),
      })
      return { comments: response.comments.map(fromProtoArtifactCommentThread) }
    },

    async replyArtifactComment(
      artifactId: string,
      input: { threadId: string; commentId: string; body: string },
    ): Promise<{ comments: ArtifactCommentThreadView[] }> {
      const response = await clients.artifacts.replyArtifactComment({
        artifactId,
        threadId: input.threadId,
        commentId: input.commentId,
        body: input.body,
      })
      return { comments: response.comments.map(fromProtoArtifactCommentThread) }
    },

    async editArtifactComment(
      artifactId: string,
      input: { threadId: string; commentId: string; body: string },
    ): Promise<{ comments: ArtifactCommentThreadView[] }> {
      const response = await clients.artifacts.editArtifactComment({
        artifactId,
        threadId: input.threadId,
        commentId: input.commentId,
        body: input.body,
      })
      return { comments: response.comments.map(fromProtoArtifactCommentThread) }
    },

    async deleteArtifactComment(
      artifactId: string,
      input: { threadId: string; commentId: string },
    ): Promise<{ comments: ArtifactCommentThreadView[] }> {
      const response = await clients.artifacts.deleteArtifactComment({
        artifactId,
        threadId: input.threadId,
        commentId: input.commentId,
      })
      return { comments: response.comments.map(fromProtoArtifactCommentThread) }
    },

    async resolveArtifactCommentThread(
      artifactId: string,
      threadId: string,
    ): Promise<{ comments: ArtifactCommentThreadView[] }> {
      const response = await clients.artifacts.resolveArtifactCommentThread({
        artifactId,
        threadId,
      })
      return { comments: response.comments.map(fromProtoArtifactCommentThread) }
    },

    async reopenArtifactCommentThread(
      artifactId: string,
      threadId: string,
    ): Promise<{ comments: ArtifactCommentThreadView[] }> {
      const response = await clients.artifacts.reopenArtifactCommentThread({
        artifactId,
        threadId,
      })
      return { comments: response.comments.map(fromProtoArtifactCommentThread) }
    },

    async removeArtifactCommentThread(
      artifactId: string,
      threadId: string,
    ): Promise<{ comments: ArtifactCommentThreadView[] }> {
      const response = await clients.artifacts.removeArtifactCommentThread({
        artifactId,
        threadId,
      })
      return { comments: response.comments.map(fromProtoArtifactCommentThread) }
    },

    async commentArtifact(
      artifactId: string,
      action: ArtifactCommentAction,
    ): Promise<{ comments: ArtifactCommentThreadView[] }> {
      switch (action.type) {
        case 'create':
          return this.createArtifactComment(artifactId, {
            threadId: action.threadId,
            commentId: action.commentId,
            quote: action.quote,
            body: action.body,
            ...(action.anchor ? { anchor: action.anchor } : {}),
          })
        case 'reply':
          return this.replyArtifactComment(artifactId, {
            threadId: action.threadId,
            commentId: action.commentId,
            body: action.body,
          })
        case 'edit':
          return this.editArtifactComment(artifactId, {
            threadId: action.threadId,
            commentId: action.commentId,
            body: action.body,
          })
        case 'delete':
          return this.deleteArtifactComment(artifactId, {
            threadId: action.threadId,
            commentId: action.commentId,
          })
        case 'resolve':
          return this.resolveArtifactCommentThread(artifactId, action.threadId)
        case 'reopen':
          return this.reopenArtifactCommentThread(artifactId, action.threadId)
        case 'remove':
          return this.removeArtifactCommentThread(artifactId, action.threadId)
      }
    },

    // ---- Issues ----
    async listIssues(
      projectId: string,
      query: { state?: string; labels?: string[]; search?: string; limit?: number } = {},
      signal?: AbortSignal,
    ): Promise<{ tracker: string; webUrl: string; issues: IssueSummaryView[] }> {
      const response = await clients.issues.listIssues(
        {
          projectId,
          state: query.state ?? '',
          labels: query.labels ? [...query.labels] : [],
          search: query.search ?? '',
          limit: query.limit ?? 0,
        },
        signal ? { signal } : undefined,
      )
      return {
        tracker: response.tracker,
        webUrl: response.webUrl,
        issues: response.issues.map(fromProtoIssueSummary),
      }
    },

    async getIssue(
      projectId: string,
      number: number,
      signal?: AbortSignal,
    ): Promise<{ tracker: string; webUrl: string; issue: IssueDetailView }> {
      const response = await clients.issues.getIssue(
        { projectId, number: BigInt(number) },
        signal ? { signal } : undefined,
      )
      return {
        tracker: response.tracker,
        webUrl: response.webUrl,
        issue: fromProtoIssueDetail(requireField(response.issue, 'issue')),
      }
    },
  }
}

export const domain2Api = createDomain2Api()
