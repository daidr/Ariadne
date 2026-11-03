import { DomainError, type Ariadne, type ReportKind, type TodoItem } from '@ariadne/core'
import type { ToolSpec } from '@ariadne/plugin-sdk'

export interface ToolContext {
  app: Ariadne
  projectId: string
  conversationId: string
  role: 'coordinator' | 'thread'
  signal: AbortSignal
}

export interface ToolResult {
  content: string
  isError?: boolean
}

export interface Tool {
  spec: ToolSpec
  run(args: Record<string, unknown>, ctx: ToolContext): ToolResult | Promise<ToolResult>
}

/** A bad argument the model can fix; it gets the message back as an error result. */
export class ToolInputError extends Error {}

function str(args: Record<string, unknown>, key: string): string {
  const value = args[key]
  if (typeof value !== 'string' || !value.trim()) {
    throw new ToolInputError(`"${key}" must be a non-empty string`)
  }
  return value
}

function optionalInt(args: Record<string, unknown>, key: string): number | null {
  const value = args[key]
  if (value === undefined || value === null) return null
  if (!Number.isInteger(value)) throw new ToolInputError(`"${key}" must be an integer`)
  return value as number
}

const TODO_STATUSES = ['doing', 'next', 'waiting', 'done'] as const
const REPORT_KINDS = ['progress', 'waiting', 'ready', 'blocked', 'failed', 'done'] as const

const json = (value: unknown): ToolResult => ({ content: JSON.stringify(value) })

const updateTodo: Tool = {
  spec: {
    name: 'update_todo',
    description:
      'Replace your live todo list, which the user watches instead of progress messages. Keep it current: what you are doing, what comes next, and what you are waiting on (say on what in "detail").',
    parameters: {
      type: 'object',
      properties: {
        items: {
          type: 'array',
          items: {
            type: 'object',
            properties: {
              label: { type: 'string' },
              status: { type: 'string', enum: TODO_STATUSES },
              detail: { type: 'string' },
            },
            required: ['label', 'status'],
          },
        },
      },
      required: ['items'],
    },
  },
  run(args, ctx) {
    if (!Array.isArray(args.items)) throw new ToolInputError('"items" must be an array')
    const items: TodoItem[] = args.items.map((raw, index) => {
      const item = (raw ?? {}) as Record<string, unknown>
      const status = item.status as TodoItem['status']
      if (typeof item.label !== 'string' || !TODO_STATUSES.includes(status)) {
        throw new ToolInputError(`items[${index}] needs a label and a valid status`)
      }
      return {
        label: item.label,
        status,
        ...(typeof item.detail === 'string' && item.detail ? { detail: item.detail } : {}),
      }
    })
    ctx.app.updateTodo(ctx.projectId, ctx.conversationId, items)
    return { content: 'Todo list updated.' }
  },
}

const askUser: Tool = {
  spec: {
    name: 'ask_user',
    description:
      'Put one decision in front of the user. It appears in their "needs me" list; the answer reaches you later as a <user_answer>. Ask only when the choice changes their goal or cannot be undone, recommend an option, and keep working on that option meanwhile unless it is irreversible.',
    parameters: {
      type: 'object',
      properties: {
        question: { type: 'string' },
        options: {
          type: 'array',
          minItems: 2,
          maxItems: 4,
          items: {
            type: 'object',
            properties: {
              label: { type: 'string' },
              consequence: { type: 'string', description: 'What happens if chosen, one line.' },
            },
            required: ['label', 'consequence'],
          },
        },
        recommended: { type: 'integer', description: 'Index of the option you recommend.' },
      },
      required: ['question', 'options'],
    },
  },
  run(args, ctx) {
    const question = str(args, 'question')
    if (!Array.isArray(args.options)) throw new ToolInputError('"options" must be an array')
    const options = args.options.map((raw, index) => {
      const option = (raw ?? {}) as Record<string, unknown>
      if (typeof option.label !== 'string' || typeof option.consequence !== 'string') {
        throw new ToolInputError(`options[${index}] needs a label and a consequence`)
      }
      return { label: option.label, consequence: option.consequence }
    })
    const attention = ctx.app.openAttention(ctx.projectId, {
      threadId: ctx.role === 'thread' ? ctx.conversationId : null,
      question,
      options,
      recommended: optionalInt(args, 'recommended'),
    })
    return json({ attentionId: attention.id, status: 'asked; the answer will arrive later' })
  },
}

const listMessages: Tool = {
  spec: {
    name: 'list_messages',
    description:
      'List the latest messages of your own conversation with their ids, e.g. to find one of yours to edit.',
    parameters: {
      type: 'object',
      properties: { limit: { type: 'integer', description: 'Default 20.' } },
    },
  },
  run(args, ctx) {
    const limit = optionalInt(args, 'limit') ?? 20
    return json(
      ctx.app
        .listMessages(ctx.conversationId)
        .slice(-limit)
        .map((m) => ({ id: m.id, author: m.author, content: m.content, edited: !!m.editedAt })),
    )
  },
}

const editMessage: Tool = {
  spec: {
    name: 'edit_message',
    description:
      'Correct one of your own earlier messages, e.g. a wrong fact or the wrong language. The user sees it marked as edited, with your reason.',
    parameters: {
      type: 'object',
      properties: {
        messageId: { type: 'string' },
        content: { type: 'string' },
        reason: { type: 'string', description: 'Short, shown to the user.' },
      },
      required: ['messageId', 'content', 'reason'],
    },
  },
  run(args, ctx) {
    ctx.app.editMessage(str(args, 'messageId'), ctx.role, str(args, 'content'), str(args, 'reason'))
    return { content: 'Message edited.' }
  },
}

const report: Tool = {
  spec: {
    name: 'report',
    description:
      'Tell the coordinator where your work stands. "ready" means it waits for the user to review, "blocked"/"failed" that you cannot go on, "done" that it is finished, "waiting" that this round ended, "progress" is an update that needs no reaction.',
    parameters: {
      type: 'object',
      properties: {
        kind: { type: 'string', enum: REPORT_KINDS },
        summary: { type: 'string', description: 'What changed and what it depends on.' },
      },
      required: ['kind', 'summary'],
    },
  },
  run(args, ctx) {
    const kind = args.kind as ReportKind
    if (!REPORT_KINDS.includes(kind)) throw new ToolInputError('"kind" is not a report kind')
    ctx.app.reportThread(ctx.conversationId, kind, str(args, 'summary'))
    return { content: 'Reported.' }
  },
}

const createThread: Tool = {
  spec: {
    name: 'create_thread',
    description:
      'Open a thread for one piece of work and hand it a brief. The thread starts as soon as a slot is free. Brief it like a capable peer: the goal, what is known, the scope, and when it is done.',
    parameters: {
      type: 'object',
      properties: {
        title: { type: 'string', description: 'Three to six words, the goal first.' },
        brief: { type: 'string' },
      },
      required: ['title', 'brief'],
    },
  },
  run(args, ctx) {
    const thread = ctx.app.createThread(ctx.projectId, { title: str(args, 'title') })
    ctx.app.postMessage(ctx.projectId, {
      conversationId: thread.id,
      author: 'coordinator',
      kind: 'relay',
      content: str(args, 'brief'),
    })
    return json({ threadId: thread.id, stage: thread.stage })
  },
}

const messageThread: Tool = {
  spec: {
    name: 'message_thread',
    description:
      'Send a message into a thread: steer it, pass along what the user decided, or ask it to fix one of its messages. The thread works on it in its next turn.',
    parameters: {
      type: 'object',
      properties: { threadId: { type: 'string' }, content: { type: 'string' } },
      required: ['threadId', 'content'],
    },
  },
  run(args, ctx) {
    ctx.app.postMessage(ctx.projectId, {
      conversationId: str(args, 'threadId'),
      author: 'coordinator',
      kind: 'relay',
      content: str(args, 'content'),
    })
    return { content: 'Sent.' }
  },
}

const listThreads: Tool = {
  spec: {
    name: 'list_threads',
    description: "List the project's threads with what each waits on and its last report.",
    parameters: { type: 'object', properties: {} },
  },
  run(_args, ctx) {
    return json(
      ctx.app.listThreads(ctx.projectId).map((t) => ({
        id: t.id,
        title: t.title,
        stage: t.stage,
        lastReport: t.lastReport,
        lastSummary: t.lastSummary,
      })),
    )
  },
}

const readThread: Tool = {
  spec: {
    name: 'read_thread',
    description:
      "Read a thread's recent messages, its todo list and stage, to check on it or catch drift.",
    parameters: {
      type: 'object',
      properties: {
        threadId: { type: 'string' },
        limit: { type: 'integer', description: 'Messages to return, default 20.' },
      },
      required: ['threadId'],
    },
  },
  run(args, ctx) {
    const threadId = str(args, 'threadId')
    const thread = ctx.app.getThread(threadId)
    if (thread.projectId !== ctx.projectId)
      throw new DomainError('error.notFound', { what: threadId })
    const limit = optionalInt(args, 'limit') ?? 20
    return json({
      title: thread.title,
      stage: thread.stage,
      lastSummary: thread.lastSummary,
      todo: ctx.app.getTodo(threadId).items,
      messages: ctx.app
        .listMessages(threadId)
        .slice(-limit)
        .map((m) => ({
          id: m.id,
          author: m.author,
          kind: m.kind,
          content: m.content,
          edited: !!m.editedAt,
        })),
    })
  },
}

export const coordinatorTools: Tool[] = [
  updateTodo,
  askUser,
  createThread,
  messageThread,
  listThreads,
  readThread,
  listMessages,
  editMessage,
]

export const threadTools: Tool[] = [updateTodo, askUser, report, listMessages, editMessage]
