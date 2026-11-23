import type {
  ArtifactFeedbackItem,
  ArtifactKind,
  AskBlock,
  Attachment,
  Author,
  AutopilotHold,
  BackgroundView,
  BlockAnswer,
  ConfirmTone,
  ConversationRole,
  ConversationView,
  Currency,
  FormField,
  FormValue,
  IssueWriteAction,
  LineComment,
  MemoryView,
  MergeMethod,
  MessageKind,
  MessageMention,
  MessageView,
  NoticeView,
  ReportKind,
  ReviewComment,
  ReviewReply,
  ReviewRoundView,
  ReviewVerdict,
  SentLineComment,
  ShowBlock,
  StepStatus,
  ThreadPullRequest,
  ThreadReference,
  ThreadStage,
  ThreadView,
  TodoItem,
  TodoView,
  TokenUsage,
  TraceItem,
  TraceMessage,
  TraceRequest,
  TraceRequestDetail,
  TraceTurn,
  TurnOutcome,
  UsageTotals,
  WorktreeRemoval,
} from '@ariadne/core'
import type {
  BranchChanges,
  ChangedFileStatus,
  FileVersions,
  MergeOutcome,
} from '@ariadne/plugin-sdk'
import type {
  ArtifactFeedbackItem as ProtoArtifactFeedbackItem,
  AskBlock as ProtoAskBlock,
  BackgroundCommandView as ProtoBackgroundCommandView,
  BlockAnswer as ProtoBlockAnswer,
  BranchChanges as ProtoBranchChanges,
  ConversationView as ProtoConversationView,
  FileVersions as ProtoFileVersions,
  FormFieldValue as ProtoFormFieldValue,
  LineComment as ProtoLineComment,
  MemoryView as ProtoMemoryView,
  MergeOutcome as ProtoMergeOutcome,
  MessageAttachment as ProtoMessageAttachment,
  MessageMention as ProtoMessageMention,
  MessageView as ProtoMessageView,
  NoticeView as ProtoNoticeView,
  ProjectWorktrees as ProtoProjectWorktrees,
  ReviewRoundView as ProtoReviewRoundView,
  SentLineComment as ProtoSentLineComment,
  ShowBlock as ProtoShowBlock,
  SkillView as ProtoSkillView,
  ThreadView as ProtoThreadView,
  TodoView as ProtoTodoView,
  TokenUsageTotals as ProtoTokenUsageTotals,
  TraceRequest as ProtoTraceRequest,
  TraceRequestDetail as ProtoTraceRequestDetail,
  TraceTokenUsage as ProtoTraceTokenUsage,
  TraceTurn as ProtoTraceTurn,
} from '@ariadne/proto'
import type { MessageInitShape } from '@bufbuild/protobuf'
import type { Value } from '@bufbuild/protobuf/wkt'
import type {
  BlockAnswerSchema,
  FormFieldValueSchema,
  LineCommentSchema,
  MentionInputSchema,
} from '@ariadne/proto'
import type { MentionInput, ProjectWorktrees, SkillView } from './api'
import { decodeLocalizedError } from './rpc'

type Effort = 'low' | 'medium' | 'high' | 'xhigh' | 'max'

function protoValueToUnknown(value?: Value): unknown {
  if (!value) return undefined
  switch (value.kind.case) {
    case 'nullValue':
      return null
    case 'numberValue':
    case 'stringValue':
    case 'boolValue':
      return value.kind.value
    case 'structValue': {
      const out: Record<string, unknown> = {}
      for (const [k, v] of Object.entries(value.kind.value.fields)) {
        out[k] = protoValueToUnknown(v)
      }
      return out
    }
    case 'listValue':
      return value.kind.value.values.map((item) => protoValueToUnknown(item))
    default:
      return undefined
  }
}

export function fromProtoUsageTotals(usage?: ProtoTokenUsageTotals): UsageTotals {
  const cost = {} as Record<Currency, number>
  if (usage?.cost) {
    for (const [currency, amount] of Object.entries(usage.cost)) {
      cost[currency as Currency] = amount
    }
  }
  return {
    input: Number(usage?.input ?? 0),
    output: Number(usage?.output ?? 0),
    cached: Number(usage?.cached ?? 0),
    cacheWrite: Number(usage?.cacheWrite ?? 0),
    reasoning: Number(usage?.reasoning ?? 0),
    cost,
  }
}

export function fromProtoThreadView(t: ProtoThreadView): ThreadView {
  return {
    id: t.id,
    projectId: t.projectId,
    title: t.title,
    nodeId: t.nodeId ?? null,
    stage: t.stage as ThreadStage,
    lastReport: (t.lastReport ?? null) as ReportKind | null,
    lastSummary: t.lastSummary ?? null,
    lastHeadline: t.lastHeadline ?? null,
    lastArtifacts: t.lastArtifacts.map((a) => ({
      id: a.id,
      kind: a.kind as ArtifactKind,
      title: a.title,
    })),
    workspace: t.workspace
      ? {
          ...(t.workspace.sourceId ? { sourceId: t.workspace.sourceId } : {}),
          label: t.workspace.label,
          branch: t.workspace.branch ?? null,
          base: t.workspace.base ?? null,
          removed: t.workspace.removed,
          parked: t.workspace.parked,
        }
      : null,
    merged: t.merged
      ? {
          branch: t.merged.branch,
          base: t.merged.base,
          method: t.merged.method as MergeMethod,
          sha: t.merged.sha,
          head: t.merged.head,
          ...(t.merged.auto ? { auto: true } : {}),
          at: Number(t.merged.at),
        }
      : null,
    archived: t.archived,
    changes: t.changes
      ? {
          files: t.changes.files,
          added: t.changes.added,
          removed: t.changes.removed,
        }
      : null,
    pullRequest: t.pullRequest
      ? {
          number: t.pullRequest.number,
          ...(t.pullRequest.reference !== undefined ? { reference: t.pullRequest.reference } : {}),
          url: t.pullRequest.url,
          title: t.pullRequest.title,
          state: t.pullRequest.state as 'open' | 'merged' | 'closed',
          conflicted: t.pullRequest.conflicted,
          headSha: t.pullRequest.headSha,
          checks: {
            state: (t.pullRequest.checks?.state || 'none') as ThreadPullRequest['checks']['state'],
            failed: [...(t.pullRequest.checks?.failed ?? [])],
          },
        }
      : null,
    aside: t.aside
      ? {
          until: Number(t.aside.until),
          reason: t.aside.reason,
          by: t.aside.by,
          due: t.aside.due,
        }
      : null,
    stopped: t.stopped
      ? {
          reason: t.stopped.reason,
          by: t.stopped.by,
          at: Number(t.stopped.at),
        }
      : null,
    references: t.references.map((ref): ThreadReference => {
      const item: ThreadReference = {
        id: ref.id,
        name: ref.name,
        kind: ref.kind,
        settings: (ref.settings ?? {}) as Record<string, unknown>,
        attentionId: ref.attentionId,
      }
      if (ref.writable !== undefined) {
        item.writable = ref.writable
      }
      return item
    }),
    endedReferences: t.endedReferences.map((e) => ({
      attentionId: e.attentionId,
      kept: e.kept,
    })),
    model: t.model ?? null,
    effort: t.effort ?? null,
    branchSlug: t.branchSlug ?? null,
    usage: fromProtoUsageTotals(t.usage),
    progress: t.progress ? { done: t.progress.done, total: t.progress.total } : null,
    doing: t.doing ?? null,
    createdAt: Number(t.createdAt),
    updatedAt: Number(t.updatedAt),
    activeAt: Number(t.activeAt),
    reviewOf: t.reviewOf ?? null,
    review: t.review
      ? {
          reviewerId: t.review.reviewerId,
          rounds: t.review.rounds,
          reviewing: t.review.reviewing,
        }
      : null,
    auto: t.auto,
    autopilotHeld: t.autopilotHeld ?? null,
  }
}

export function fromProtoBackgroundCommand(cmd: ProtoBackgroundCommandView): BackgroundView {
  return {
    id: cmd.id,
    command: cmd.command,
    startedAt: Number(cmd.startedAt),
    url: cmd.url ?? null,
    result: cmd.result
      ? {
          exitCode: cmd.result.exitCode,
          endedAt: Number(cmd.result.endedAt),
          stopped: cmd.result.stopped,
        }
      : null,
  }
}

export function fromProtoReviewRound(r: ProtoReviewRoundView): ReviewRoundView {
  return {
    threadId: r.threadId,
    reviewerId: r.reviewerId,
    round: r.round,
    head: r.head ?? null,
    requestedAt: Number(r.requestedAt),
    verdict: (r.verdict ?? null) as ReviewVerdict | null,
    abandoned: r.abandoned,
    summary: r.summary ?? null,
    comments: r.comments.map((c): ReviewComment => {
      const item: ReviewComment = {
        path: c.path,
        line: c.line,
        quote: c.quote,
        body: c.body,
        kind: c.kind as ReviewComment['kind'],
      }
      if (c.side === 'original') {
        item.side = c.side
      }
      return item
    }),
    replies: r.replies
      ? r.replies.items.map((rep): ReviewReply => ({
          comment: rep.comment,
          outcome: rep.outcome as ReviewReply['outcome'],
          note: rep.note,
        }))
      : null,
    submittedAt: r.submittedAt === undefined ? null : Number(r.submittedAt),
  }
}

export function fromProtoSentLineComment(c: ProtoSentLineComment): SentLineComment {
  const out: SentLineComment = {
    id: c.id,
    messageId: c.messageId,
    sentAt: Number(c.sentAt),
    path: c.path,
    line: c.line,
    quote: c.quote,
    body: c.body,
  }
  if (c.side === 'original') {
    out.side = c.side
  }
  return out
}

export function fromProtoConversationView(c: ProtoConversationView): ConversationView {
  return {
    id: c.id,
    projectId: c.projectId,
    role: c.role as ConversationRole,
    pendingSeq: Number(c.pendingSeq),
    turn: c.turn
      ? {
          id: c.turn.id,
          startedAt: Number(c.turn.startedAt),
          model: c.turn.model ?? null,
          activity: c.turn.activity ?? null,
        }
      : null,
    lastOutcome: (c.lastOutcome ?? null) as TurnOutcome | null,
    lastError: decodeLocalizedError(c.lastError),
    usage: fromProtoUsageTotals(c.usage),
    contextTokens: Number(c.contextTokens),
    compactions: c.compactions,
    wait: c.wait
      ? {
          id: c.wait.id,
          until: Number(c.wait.until),
          note: c.wait.note,
        }
      : null,
  }
}

export function fromProtoTodoView(t?: ProtoTodoView): TodoView {
  if (!t) {
    return { conversationId: '', items: [], updatedAt: 0 }
  }
  const out: TodoView = {
    conversationId: t.conversationId,
    items: t.items.map((item): TodoItem => {
      const mapped: TodoItem = {
        label: item.label,
        status: item.status as TodoItem['status'],
      }
      if (item.detail !== undefined) {
        mapped.detail = item.detail
      }
      return mapped
    }),
    updatedAt: Number(t.updatedAt),
  }
  if (t.position) {
    out.position = {
      at: Number(t.position.at),
      afterMessageId: t.position.anchor.case === 'afterMessageId' ? t.position.anchor.value : null,
    }
  }
  return out
}

function fromProtoFormFieldValue(v?: ProtoFormFieldValue): FormValue {
  if (!v) return null
  switch (v.value.case) {
    case 'stringValue':
    case 'numberValue':
    case 'boolValue':
      return v.value.value
    case 'stringListValue':
      return [...v.value.value.values]
    case 'nullValue':
      return null
    default:
      return null
  }
}

function toProtoFormFieldValue(value: FormValue): MessageInitShape<typeof FormFieldValueSchema> {
  if (value === null) {
    return { value: { case: 'nullValue', value: true } }
  }
  if (typeof value === 'string') {
    return { value: { case: 'stringValue', value } }
  }
  if (typeof value === 'number') {
    return { value: { case: 'numberValue', value } }
  }
  if (typeof value === 'boolean') {
    return { value: { case: 'boolValue', value } }
  }
  return { value: { case: 'stringListValue', value: { values: [...value] } } }
}

function fromProtoAskBlock(b?: ProtoAskBlock): AskBlock | null {
  if (!b) return null
  switch (b.block.case) {
    case 'choice': {
      const c = b.block.value
      return {
        type: 'choice',
        question: c.question,
        options: c.options.map((o) => ({ label: o.label, consequence: o.consequence })),
        recommended: c.recommended ?? null,
      }
    }
    case 'confirm': {
      const c = b.block.value
      return {
        type: 'confirm',
        title: c.title,
        description: c.description ?? null,
        detail: c.detail ?? null,
        tone: (c.tone || 'neutral') as ConfirmTone,
        approveText: c.approveText ?? null,
        denyText: c.denyText ?? null,
        ...(c.allowProjectReference ? { allowProjectReference: true as const } : {}),
      }
    }
    case 'form': {
      const f = b.block.value
      return {
        type: 'form',
        title: f.title,
        description: f.description ?? null,
        fields: f.fields.map((field): FormField => ({
          key: field.key,
          label: field.label,
          kind: field.kind as FormField['kind'],
          required: field.required,
          placeholder: field.placeholder ?? null,
          options: field.options.map((o) => ({ label: o.label, value: o.value })),
          default: fromProtoFormFieldValue(field.defaultValue),
          min: field.min ?? null,
          max: field.max ?? null,
        })),
        submitText: f.submitText ?? null,
      }
    }
    default:
      return null
  }
}

function fromProtoBlockAnswer(a?: ProtoBlockAnswer): BlockAnswer | null {
  if (!a) return null
  switch (a.answer.case) {
    case 'choice':
      return { type: 'choice', choice: a.answer.value.choice }
    case 'confirm':
      return {
        type: 'confirm',
        approved: a.answer.value.approved,
        ...(a.answer.value.scope !== undefined
          ? { scope: a.answer.value.scope as 'thread' | 'project' }
          : {}),
      }
    case 'form': {
      const values: Record<string, FormValue> = {}
      for (const [k, v] of Object.entries(a.answer.value.values)) {
        values[k] = fromProtoFormFieldValue(v)
      }
      return { type: 'form', values }
    }
    default:
      return null
  }
}

export function toProtoBlockAnswer(
  answer: BlockAnswer,
): MessageInitShape<typeof BlockAnswerSchema> {
  switch (answer.type) {
    case 'choice':
      return { answer: { case: 'choice', value: { choice: answer.choice } } }
    case 'confirm':
      return {
        answer: {
          case: 'confirm',
          value: {
            approved: answer.approved,
            ...(answer.scope !== undefined ? { scope: answer.scope } : {}),
          },
        },
      }
    case 'form': {
      const values: Record<string, MessageInitShape<typeof FormFieldValueSchema>> = {}
      for (const [k, v] of Object.entries(answer.values)) {
        values[k] = toProtoFormFieldValue(v)
      }
      return { answer: { case: 'form', value: { values } } }
    }
  }
}

function fromProtoShowBlock(b: ProtoShowBlock): ShowBlock | null {
  switch (b.block.case) {
    case 'table': {
      const t = b.block.value
      return {
        type: 'table',
        title: t.title ?? null,
        columns: t.columns.map((c) => ({
          key: c.key,
          title: c.title,
          align: (c.align === 'end' ? 'end' : 'start') as 'start' | 'end',
        })),
        rows: t.rows.map((row) => {
          const out: Record<string, string | number | boolean | null> = {}
          for (const [k, cell] of Object.entries(row.cells)) {
            switch (cell.value.case) {
              case 'stringValue':
              case 'numberValue':
              case 'boolValue':
                out[k] = cell.value.value
                break
              default:
                out[k] = null
                break
            }
          }
          return out
        }),
      }
    }
    case 'chart': {
      const c = b.block.value
      return {
        type: 'chart',
        title: c.title ?? null,
        kind: c.kind as 'bar' | 'line' | 'area' | 'pie',
        x: c.x,
        y: [...c.y],
        labels: { ...c.labels },
        rows: c.rows.map((row) => {
          const out: Record<string, string | number | null> = {}
          for (const [k, cell] of Object.entries(row.cells)) {
            switch (cell.value.case) {
              case 'stringValue':
              case 'numberValue':
                out[k] = cell.value.value
                break
              default:
                out[k] = null
                break
            }
          }
          return out
        }),
        stacked: c.stacked,
        horizontal: c.horizontal,
      }
    }
    case 'steps': {
      const s = b.block.value
      return {
        type: 'steps',
        title: s.title ?? null,
        items: s.items.map((item) => ({
          title: item.title,
          description: item.description ?? null,
          status: item.status as StepStatus,
        })),
      }
    }
    case 'thread':
      return { type: 'thread', threadId: b.block.value.threadId }
    case 'connector': {
      const c = b.block.value
      return {
        type: 'connector',
        name: c.name,
        why: c.why ?? null,
        transport: c.transport as 'stdio' | 'http',
        command: c.command ?? null,
        args: [...c.args],
        url: c.url ?? null,
        secrets: [...c.secrets],
      }
    }
    default:
      return null
  }
}

function fromProtoAttachments(list: readonly ProtoMessageAttachment[]): Attachment[] {
  return list.map((a) => ({
    fileId: a.fileId,
    name: a.name,
    mime: a.mime,
    size: Number(a.size),
  }))
}

function fromProtoMentions(list: readonly ProtoMessageMention[]): MessageMention[] {
  const out: MessageMention[] = []
  for (const m of list) {
    switch (m.mention.case) {
      case 'thread':
        out.push({ kind: 'thread', id: m.mention.value.id, title: m.mention.value.title })
        break
      case 'artifact':
        out.push({ kind: 'artifact', id: m.mention.value.id, title: m.mention.value.title })
        break
      case 'file':
        out.push({ kind: 'file', path: m.mention.value.path })
        break
      case 'skill':
        out.push({ kind: 'skill', name: m.mention.value.name })
        break
      default:
        break
    }
  }
  return out
}

export function toProtoMentionInput(m: MentionInput): MessageInitShape<typeof MentionInputSchema> {
  switch (m.kind) {
    case 'thread':
      return { mention: { case: 'threadId', value: m.id } }
    case 'artifact':
      return { mention: { case: 'artifactId', value: m.id } }
    case 'file':
      return { mention: { case: 'filePath', value: m.path } }
    case 'skill':
      return { mention: { case: 'skillName', value: m.name } }
  }
}

export function toProtoLineComment(c: LineComment): MessageInitShape<typeof LineCommentSchema> {
  return {
    path: c.path,
    line: c.line,
    quote: c.quote,
    body: c.body,
    ...(c.side !== undefined ? { side: c.side } : {}),
  }
}

function fromProtoLineComment(c: ProtoLineComment): LineComment {
  const out: LineComment = {
    path: c.path,
    line: c.line,
    quote: c.quote,
    body: c.body,
  }
  if (c.side === 'original') {
    out.side = c.side
  }
  return out
}

export function fromProtoMessageView(m: ProtoMessageView): MessageView {
  const blocks: ShowBlock[] = []
  for (const b of m.blocks) {
    const converted = fromProtoShowBlock(b)
    if (converted) blocks.push(converted)
  }
  const askBlock = m.ask ? fromProtoAskBlock(m.ask.block) : null
  return {
    id: m.id,
    conversationId: m.conversationId,
    author: m.author as Author,
    kind: m.kind as MessageKind,
    content: m.content,
    blocks,
    ask:
      m.ask && askBlock
        ? {
            attentionId: m.ask.attentionId,
            block: askBlock,
            answer: fromProtoBlockAnswer(m.ask.answer),
            withdrawn: m.ask.withdrawn ?? null,
          }
        : null,
    attachments: fromProtoAttachments(m.attachments),
    mentions: fromProtoMentions(m.mentions),
    comments: m.comments.map(fromProtoLineComment),
    cites: m.cites.map((c) => ({
      messageId: c.messageId,
      conversationId: c.conversationId,
      content: c.content,
      at: Number(c.at),
      ...(c.attachments.length > 0 ? { attachments: fromProtoAttachments(c.attachments) } : {}),
      ...(c.mentions.length > 0 ? { mentions: fromProtoMentions(c.mentions) } : {}),
    })),
    relayedTo: m.relayedTo.map((r) => ({ threadId: r.threadId, messageId: r.messageId })),
    replyTo: m.replyTo
      ? {
          messageId: m.replyTo.messageId,
          author: m.replyTo.author as Author,
          text: m.replyTo.text,
        }
      : null,
    reactions: m.reactions.map((r) => ({ emoji: r.emoji, by: r.by as Author })),
    createdAt: Number(m.createdAt),
    editedAt: m.editedAt === undefined ? null : Number(m.editedAt),
    editReason: m.editReason ?? null,
    originalContent: m.originalContent ?? null,
    editedSinceRead: m.editedSinceRead,
  }
}

function fromProtoFeedbackItem(item: ProtoArtifactFeedbackItem): ArtifactFeedbackItem | null {
  switch (item.item.case) {
    case 'edit':
      return { kind: 'edit', diff: item.item.value.diff }
    case 'choice':
      return {
        kind: 'choice',
        chosen: item.item.value.chosen.map((c) => ({
          id: c.id,
          title: c.title,
          ...(c.group ? { group: c.group } : {}),
          ...(c.groupTitle ? { groupTitle: c.groupTitle } : {}),
        })),
      }
    case 'comment':
      return {
        kind: 'comment',
        threadId: item.item.value.threadId,
        action: item.item.value.action as 'create' | 'reply' | 'edit' | 'reopen',
        body: item.item.value.body,
        where: item.item.value.where,
      }
    case 'mark':
      return {
        kind: 'mark',
        nodeId: item.item.value.nodeId,
        type: item.item.value.type,
        text: item.item.value.text,
        where: item.item.value.where,
      }
    case 'board':
      return { kind: 'board', changes: [...item.item.value.changes] }
    default:
      return null
  }
}

export function fromProtoNoticeView(n: ProtoNoticeView): NoticeView | null {
  const base = {
    id: n.id,
    conversationId: n.conversationId,
    createdAt: Number(n.createdAt),
  }
  switch (n.payload.case) {
    case 'approval': {
      const p = n.payload.value
      const block = fromProtoAskBlock(p.block)
      if (!block) return null
      return {
        ...base,
        kind: 'approval',
        block,
        answer: fromProtoBlockAnswer(p.answer),
        withdrawn: p.withdrawn ?? null,
      }
    }
    case 'memory': {
      const p = n.payload.value
      return {
        ...base,
        kind: 'memory',
        action: p.action as 'saved' | 'removed',
        memoryId: p.memoryId,
        text: p.text,
        previous: p.previous ?? null,
        undone: p.undone,
      }
    }
    case 'memoryEverywhere': {
      const p = n.payload.value
      return {
        ...base,
        kind: 'memory-everywhere',
        action: p.action as 'saved' | 'removed',
        text: p.text,
        undone: p.undone,
      }
    }
    case 'compactionFailed':
      return { ...base, kind: 'compaction-failed', detail: n.payload.value.detail }
    case 'routineChange':
      return {
        ...base,
        kind: 'routine-change',
        action: n.payload.value.action as 'added' | 'removed',
        prompt: n.payload.value.prompt,
      }
    case 'recap': {
      const p = n.payload.value
      return {
        ...base,
        kind: 'recap',
        since: Number(p.since),
        done: [...p.done],
        merged: [...p.merged],
        waiting: [...p.waiting],
        asks: [...p.asks],
      }
    }
    case 'modelRetry': {
      const p = n.payload.value
      return {
        ...base,
        kind: 'model-retry',
        attempt: p.attempt,
        delayMs: Number(p.delayMs),
        detail: p.detail,
      }
    }
    case 'modelFallback': {
      const p = n.payload.value
      return {
        ...base,
        kind: 'model-fallback',
        from: p.from,
        to: p.to,
        detail: p.detail,
      }
    }
    case 'woken':
      return {
        ...base,
        kind: 'woken',
        how: n.payload.value.how as 'due' | 'now',
        note: n.payload.value.note,
      }
    case 'routine': {
      const p = n.payload.value
      return {
        ...base,
        kind: 'routine',
        routineId: p.routineId,
        prompt: p.prompt,
        dueAt: Number(p.dueAt),
      }
    }
    case 'issueLinked': {
      const p = n.payload.value
      return {
        ...base,
        kind: 'issue-linked',
        nodeId: p.nodeId,
        nodeTitle: p.nodeTitle,
        tracker: p.tracker,
        number: p.number,
        linked: p.linked,
      }
    }
    case 'issueWritten': {
      const p = n.payload.value
      return {
        ...base,
        kind: 'issue-written',
        tracker: p.tracker,
        number: p.number,
        action: p.action as IssueWriteAction,
        text: p.text,
        url: p.url ?? null,
      }
    }
    case 'merged': {
      const p = n.payload.value
      return {
        ...base,
        kind: 'merged',
        base: p.base,
        method: p.method as MergeMethod,
        sha: p.sha,
        ...(p.auto ? { auto: true } : {}),
        ...(p.pullRequest !== undefined ? { pullRequest: p.pullRequest } : {}),
        ...(p.undone ? { undone: true } : {}),
      }
    }
    case 'autopilotHeld':
      return {
        ...base,
        kind: 'autopilot-held',
        reason: n.payload.value.reason as AutopilotHold,
        detail: n.payload.value.detail,
      }
    case 'artifactEdited': {
      const p = n.payload.value
      return {
        ...base,
        kind: 'artifact-edited',
        artifactId: p.artifactId,
        title: p.title,
        version: p.version,
      }
    }
    case 'artifactFeedback': {
      const p = n.payload.value
      const items: ArtifactFeedbackItem[] = []
      for (const item of p.items) {
        const mapped = fromProtoFeedbackItem(item)
        if (mapped) items.push(mapped)
      }
      return {
        ...base,
        kind: 'artifact-feedback',
        artifactId: p.artifactId,
        title: p.title,
        round: p.round,
        items,
        done: p.done,
        summary: p.summary ?? null,
        ...(p.withdrawn ? { withdrawn: true } : {}),
      }
    }
    case 'artifactComment': {
      const p = n.payload.value
      return {
        ...base,
        kind: 'artifact-comment',
        artifactId: p.artifactId,
        title: p.title,
        by: p.by,
        action: p.action as 'create' | 'reply',
        body: p.body,
      }
    }
    case 'mergeConflict': {
      const p = n.payload.value
      return {
        ...base,
        kind: 'merge-conflict',
        base: p.base,
        files: [...p.files],
        ...(p.pullRequest !== undefined ? { pullRequest: p.pullRequest } : {}),
      }
    }
    case 'worktreeRemoved':
      return {
        ...base,
        kind: 'worktree-removed',
        reason: n.payload.value.reason as WorktreeRemoval,
      }
    case 'planChanged': {
      const p = n.payload.value
      return {
        ...base,
        kind: 'plan-changed',
        revisionId: p.revisionId,
        reason: p.reason,
        added: p.added,
        revert: p.revert,
      }
    }
    case 'threadAutoStarted':
      return {
        ...base,
        kind: 'thread-auto-started',
        threadId: n.payload.value.threadId,
        title: n.payload.value.title,
      }
    case 'review': {
      const p = n.payload.value
      return {
        ...base,
        kind: 'review',
        threadId: p.threadId,
        reviewerId: p.reviewerId,
        round: p.round,
        state: p.state as 'started' | ReviewVerdict | 'abandoned',
        count: p.count,
      }
    }
    case 'turnChanges': {
      const p = n.payload.value
      return {
        ...base,
        kind: 'turn-changes',
        turnId: p.turnId,
        from: p.from,
        to: p.to,
        files: p.files.map((f) => ({
          path: f.path,
          previousPath: f.previousPath ?? null,
          status: f.status as ChangedFileStatus,
          added: f.added ?? null,
          removed: f.removed ?? null,
        })),
        total: {
          files: p.total?.files ?? 0,
          added: p.total?.added ?? 0,
          removed: p.total?.removed ?? 0,
        },
        ...(p.startedAt !== undefined ? { startedAt: Number(p.startedAt) } : {}),
      }
    }
    default:
      return null
  }
}

export function fromProtoMemoryView(m: ProtoMemoryView): MemoryView {
  return {
    id: m.id,
    projectId: m.projectId,
    text: m.text,
    author: m.author,
    createdAt: Number(m.createdAt),
    updatedAt: Number(m.updatedAt),
  }
}

export function fromProtoSkillView(s: ProtoSkillView): SkillView {
  return {
    name: s.name,
    when: s.when,
    source: s.source as 'repo' | 'local',
  }
}

function fromProtoTraceTokenUsage(u?: ProtoTraceTokenUsage): TokenUsage {
  const out: TokenUsage = {
    input: Number(u?.input ?? 0),
    output: Number(u?.output ?? 0),
  }
  if (u?.contextInput !== undefined) out.contextInput = Number(u.contextInput)
  if (u?.cached !== undefined) out.cached = Number(u.cached)
  if (u?.cacheWrite !== undefined) out.cacheWrite = Number(u.cacheWrite)
  if (u?.reasoning !== undefined) out.reasoning = Number(u.reasoning)
  if (u?.cost) {
    out.cost = { amount: u.cost.amount, currency: u.cost.currency as Currency }
  }
  return out
}

function fromProtoTraceRequest(r: ProtoTraceRequest): TraceRequest {
  return {
    kind: 'request',
    seq: Number(r.seq),
    number: r.number,
    purpose: r.purpose as TraceRequest['purpose'],
    provider: r.provider ?? null,
    model: r.model ?? null,
    effort: (r.effort ?? null) as Effort | null,
    startedAt: Number(r.startedAt),
    firstOutputAt: r.firstOutputAt === undefined ? null : Number(r.firstOutputAt),
    finishedAt: Number(r.finishedAt),
    usage: r.usage ? fromProtoTraceTokenUsage(r.usage) : null,
    finish: r.finish as TraceRequest['finish'],
    text: r.text,
    reasoning: r.reasoning.map((item) => ({ text: item.text, redacted: item.redacted })),
    toolCalls: r.toolCalls.map((tc) => ({
      id: tc.id,
      name: tc.name,
      arguments: tc.arguments,
      startedAt: Number(tc.startedAt),
      result: tc.result
        ? {
            content: tc.result.content,
            isError: tc.result.isError,
            finishedAt: Number(tc.result.finishedAt),
            files: fromProtoAttachments(tc.result.files),
          }
        : null,
      ...(tc.research.length > 0 ? { research: tc.research.map(fromProtoTraceRequest) } : {}),
    })),
  }
}

export function fromProtoTraceTurn(t: ProtoTraceTurn): TraceTurn {
  const items: TraceItem[] = []
  for (const item of t.items) {
    switch (item.item.case) {
      case 'request':
        items.push(fromProtoTraceRequest(item.item.value))
        break
      case 'requestFailed': {
        const f = item.item.value
        items.push({
          kind: 'request-failed',
          seq: Number(f.seq),
          purpose: f.purpose as TraceRequest['purpose'],
          ...(f.provider !== undefined ? { provider: f.provider } : {}),
          ...(f.model !== undefined ? { model: f.model } : {}),
          attempt: f.attempt,
          startedAt: Number(f.startedAt),
          finishedAt: Number(f.finishedAt),
          detail: f.detail,
          retryable: f.retryable,
          willRetry: f.willRetry,
          ...(f.agentError
            ? {
                agentError: {
                  operation: f.agentError.operation,
                  ...(f.agentError.executable !== undefined
                    ? { executable: f.agentError.executable }
                    : {}),
                  ...(f.agentError.name !== undefined ? { name: f.agentError.name } : {}),
                  ...(f.agentError.version !== undefined ? { version: f.agentError.version } : {}),
                  ...(f.agentError.protocolVersion !== undefined
                    ? { protocolVersion: f.agentError.protocolVersion }
                    : {}),
                  ...(f.agentError.sessionMethod !== undefined
                    ? { sessionMethod: f.agentError.sessionMethod }
                    : {}),
                  ...(f.agentError.rpcCode !== undefined ? { rpcCode: f.agentError.rpcCode } : {}),
                  ...(f.agentError.rpcData !== undefined ? { rpcData: f.agentError.rpcData } : {}),
                  ...(f.agentError.stopReason !== undefined
                    ? { stopReason: f.agentError.stopReason }
                    : {}),
                  ...(f.agentError.stderr !== undefined ? { stderr: f.agentError.stderr } : {}),
                },
              }
            : {}),
        })
        break
      }
      case 'retry': {
        const r = item.item.value
        items.push({
          kind: 'retry',
          at: Number(r.at),
          attempt: r.attempt,
          delayMs: Number(r.delayMs),
          detail: r.detail,
        })
        break
      }
      case 'fallback': {
        const fb = item.item.value
        items.push({
          kind: 'fallback',
          at: Number(fb.at),
          from: fb.from,
          to: fb.to,
          detail: fb.detail,
        })
        break
      }
      case 'compactionFailed':
        items.push({
          kind: 'compaction-failed',
          at: Number(item.item.value.at),
          detail: item.item.value.detail,
        })
        break
      case 'resultsCleared':
        items.push({ kind: 'results-cleared', at: Number(item.item.value.at) })
        break
      case 'instructions': {
        const ins = item.item.value
        items.push({
          kind: 'instructions',
          at: Number(ins.at),
          path: ins.path,
          changed: ins.changed,
          removed: ins.removed,
          uncommitted: ins.uncommitted,
        })
        break
      }
      default:
        break
    }
  }
  return {
    turnId: t.turnId,
    seq: Number(t.seq),
    model: t.model,
    startedAt: Number(t.startedAt),
    finishedAt: t.finishedAt === undefined ? null : Number(t.finishedAt),
    outcome: (t.outcome ?? null) as TurnOutcome | null,
    error: decodeLocalizedError(t.error),
    usage: fromProtoTraceTokenUsage(t.usage),
    items,
  }
}

export function fromProtoTraceRequestDetail(d: ProtoTraceRequestDetail): TraceRequestDetail {
  const messages: TraceMessage[] = []
  for (const msg of d.messages) {
    switch (msg.message.case) {
      case 'system':
        messages.push({ role: 'system', content: msg.message.value.content })
        break
      case 'user':
        messages.push({
          role: 'user',
          content: msg.message.value.content,
          ...(msg.message.value.images.length > 0
            ? { images: fromProtoAttachments(msg.message.value.images) }
            : {}),
        })
        break
      case 'assistant': {
        const a = msg.message.value
        messages.push({
          role: 'assistant',
          content: a.content,
          ...(a.toolCalls.length > 0
            ? {
                toolCalls: a.toolCalls.map((tc) => ({
                  id: tc.id,
                  name: tc.name,
                  arguments: tc.arguments,
                })),
              }
            : {}),
          ...(a.reasoning.length > 0
            ? {
                reasoning: a.reasoning.map((r) => ({
                  text: r.text,
                  ...(r.signature !== undefined ? { signature: r.signature } : {}),
                  ...(r.redacted !== undefined ? { redacted: r.redacted } : {}),
                })),
              }
            : {}),
        })
        break
      }
      case 'tool': {
        const tool = msg.message.value
        messages.push({
          role: 'tool',
          toolCallId: tool.toolCallId,
          content: tool.content,
          ...(tool.isError !== undefined ? { isError: tool.isError } : {}),
          ...(tool.images.length > 0 ? { images: fromProtoAttachments(tool.images) } : {}),
        })
        break
      }
      default:
        break
    }
  }
  return {
    messages,
    tools: d.tools.map((tool) => ({
      name: tool.name,
      description: tool.description,
      parameters: (tool.parameters ?? {}) as Record<string, unknown>,
    })),
    effort: (d.effort ?? null) as Effort | null,
    call: d.call
      ? {
          provider: d.call.provider,
          model: d.call.model,
          settings: (d.call.settings ?? {}) as Record<string, unknown>,
        }
      : null,
    body: d.body ? protoValueToUnknown(d.body) : null,
    newFrom: d.newFrom,
    systemChanged: d.systemChanged,
  }
}

export function fromProtoBranchChanges(changes?: ProtoBranchChanges): BranchChanges | null {
  if (!changes) return null
  return {
    branch: changes.branch,
    base: changes.base,
    from: changes.from,
    head: changes.head,
    ...(changes.baseHead !== undefined ? { baseHead: changes.baseHead } : {}),
    commits: changes.commits.map((c) => ({ sha: c.sha, subject: c.subject })),
    files: changes.files.map((f) => ({
      path: f.path,
      previousPath: f.previousPath ?? null,
      status: f.status as ChangedFileStatus,
      added: f.added === undefined ? null : Number(f.added),
      removed: f.removed === undefined ? null : Number(f.removed),
    })),
    uncommitted: [...changes.uncommitted],
  }
}

export function fromProtoFileVersions(file?: ProtoFileVersions): FileVersions {
  return {
    before: file?.before ?? null,
    after: file?.after ?? null,
    skipped: (file?.skipped ?? null) as 'binary' | 'large' | null,
  }
}

export function fromProtoMergeOutcome(outcome?: ProtoMergeOutcome): MergeOutcome {
  switch (outcome?.kind) {
    case 'merged':
      return { kind: 'merged', sha: outcome.sha ?? '', head: outcome.head ?? '' }
    case 'conflict':
      return { kind: 'conflict', files: [...outcome.files] }
    case 'moved':
      return { kind: 'moved' }
    default:
      return { kind: 'empty' }
  }
}

export function fromProtoProjectWorktrees(project: ProtoProjectWorktrees): ProjectWorktrees {
  return {
    id: project.id,
    name: project.name,
    worktrees: project.worktrees.map((wt) => ({
      threadId: wt.threadId,
      path: wt.path,
      bytes: wt.bytes === undefined ? null : Number(wt.bytes),
      ...(wt.orphaned ? { orphaned: true } : {}),
      thread: wt.thread ? fromProtoThreadView(wt.thread) : null,
      ...(wt.sourceId !== undefined ? { sourceId: wt.sourceId } : {}),
    })),
    shared: project.shared ?? null,
    error: decodeLocalizedError(project.error),
  }
}
