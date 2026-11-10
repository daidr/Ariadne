import {
  askTitle,
  BLOCK_LIMITS,
  CHART_KINDS,
  describeAnswer,
  FORM_FIELD_KINDS,
  STEP_STATUSES,
  type MessageView,
  type ThreadView,
} from '@ariadne/core'
import { json, str, ToolInputError, withImages, type Tool } from './tool-helpers'

const text = (description?: string) => ({ type: 'string', ...(description ? { description } : {}) })
const label = text()

/** The blocks' JSON schema; `parseBlock` in core is what actually checks them. */
const blockSchemas = [
  {
    type: 'object',
    description: 'Ask the user to pick one of 2 to 6 options. Recommend one.',
    properties: {
      type: { const: 'choice' },
      question: label,
      options: {
        type: 'array',
        minItems: 2,
        maxItems: BLOCK_LIMITS.options,
        items: {
          type: 'object',
          properties: {
            label: text('A few words.'),
            consequence: text('What happens if chosen, one line.'),
          },
          required: ['label', 'consequence'],
        },
      },
      recommended: { type: 'integer', description: 'Index of the option you recommend.' },
    },
    required: ['type', 'question', 'options'],
  },
  {
    type: 'object',
    description:
      'Ask for a yes or no on one action. `detail` holds what exactly will be done (a command, a text to send), shown in monospace.',
    properties: {
      type: { const: 'confirm' },
      title: text('The question, e.g. "Delete the staging database?"'),
      description: text('What agreeing does, one sentence.'),
      detail: label,
      tone: { type: 'string', enum: ['neutral', 'warning', 'danger'] },
      approveText: text('The yes button, e.g. "Delete".'),
      denyText: text('The no button.'),
    },
    required: ['type', 'title'],
  },
  {
    type: 'object',
    description:
      'Ask for several values at once, e.g. settings for a task. The answer has each value by field key.',
    properties: {
      type: { const: 'form' },
      title: label,
      description: label,
      submitText: label,
      fields: {
        type: 'array',
        minItems: 1,
        maxItems: BLOCK_LIMITS.fields,
        items: {
          type: 'object',
          properties: {
            key: text('Letters, digits, _ or -; unique in the form.'),
            label,
            kind: { type: 'string', enum: FORM_FIELD_KINDS },
            required: { type: 'boolean' },
            placeholder: label,
            options: {
              type: 'array',
              description: 'For select and multiselect: strings, or { label, value }.',
              items: {
                anyOf: [
                  { type: 'string' },
                  {
                    type: 'object',
                    properties: { label, value: label },
                    required: ['label', 'value'],
                  },
                ],
              },
            },
            default: {
              description:
                'Text, a number, a boolean (switch), an option value (select) or a list of them (multiselect).',
            },
            min: { type: 'number' },
            max: { type: 'number' },
          },
          required: ['key', 'label', 'kind'],
        },
      },
    },
    required: ['type', 'title', 'fields'],
  },
  {
    type: 'object',
    description: `Show rows of data, up to ${BLOCK_LIMITS.rows}. Each row holds cells by column key: text, numbers or booleans.`,
    properties: {
      type: { const: 'table' },
      title: label,
      columns: {
        type: 'array',
        minItems: 1,
        maxItems: BLOCK_LIMITS.columns,
        items: {
          type: 'object',
          properties: {
            key: label,
            title: label,
            align: { type: 'string', enum: ['start', 'end'], description: 'end for numbers.' },
          },
          required: ['key', 'title'],
        },
      },
      rows: { type: 'array', items: { type: 'object' } },
    },
    required: ['type', 'columns', 'rows'],
  },
  {
    type: 'object',
    description:
      'Draw numbers. Each row has its x value (a category, number or date like 2026-11-04) and a number for each key in y. A pie has exactly one y.',
    properties: {
      type: { const: 'chart' },
      title: label,
      kind: { type: 'string', enum: CHART_KINDS },
      x: text('The key of x in each row.'),
      y: { type: 'array', items: label, maxItems: BLOCK_LIMITS.series },
      labels: {
        type: 'object',
        description: 'Display names of the y keys, e.g. { "count": "Issues" }.',
      },
      rows: { type: 'array', items: { type: 'object' }, maxItems: BLOCK_LIMITS.chartRows },
      stacked: { type: 'boolean' },
      horizontal: { type: 'boolean', description: 'Bars only.' },
    },
    required: ['type', 'kind', 'x', 'y', 'rows'],
  },
  {
    type: 'object',
    description:
      'Show an ordered procedure or the stages of something and where each stands, e.g. a release checklist. Not for your own progress: that is update_todo.',
    properties: {
      type: { const: 'steps' },
      title: label,
      items: {
        type: 'array',
        minItems: 1,
        maxItems: BLOCK_LIMITS.steps,
        items: {
          type: 'object',
          properties: {
            title: label,
            description: label,
            status: { type: 'string', enum: STEP_STATUSES },
          },
          required: ['title'],
        },
      },
    },
    required: ['type', 'items'],
  },
  {
    type: 'object',
    description:
      "An entry to one of the project's threads. The user sees its title and where it stands now, live, and opens it from there.",
    properties: {
      type: { const: 'thread' },
      threadId: label,
    },
    required: ['type', 'threadId'],
  },
]

export const postUi: Tool = {
  spec: {
    name: 'post_ui',
    description: `Post a message with interface blocks into your conversation, for what plain text shows badly. Blocks: choice, confirm and form ask the user something; table, chart and steps show something; thread is an entry to a thread. A message asks at most one thing, shown after the other blocks. An ask appears in the message and in the user's "needs me" list; the answer reaches you later as a <user_answer>. Ask only when the answer changes the user's goal or the step cannot be undone, recommend an option, and keep working meanwhile unless it is irreversible. Put a short lead-in in "text" and do not repeat in your reply what the blocks already show. At most ${BLOCK_LIMITS.blocks} blocks.`,
    parameters: {
      type: 'object',
      properties: {
        text: text("A short lead-in shown above the blocks, in the user's language."),
        blocks: {
          type: 'array',
          minItems: 1,
          maxItems: BLOCK_LIMITS.blocks,
          items: { anyOf: blockSchemas },
        },
      },
      required: ['blocks'],
    },
  },
  async run(args, ctx) {
    const content = args.text === undefined || args.text === null ? '' : args.text
    if (typeof content !== 'string') throw new ToolInputError('"text" must be a string')
    if (!Array.isArray(args.blocks) || !args.blocks.length) {
      throw new ToolInputError('"blocks" must be a non-empty array')
    }
    const message = ctx.app.postMessage(ctx.projectId, {
      conversationId: ctx.conversationId,
      author: ctx.role,
      content: await withImages(ctx, content.trim()),
      blocks: args.blocks,
    })
    return json({
      messageId: message.id,
      ...(message.ask
        ? { attentionId: message.ask.attentionId, status: 'asked; the answer will arrive later' }
        : { status: 'shown' }),
    })
  },
}

export const withdrawAsk: Tool = {
  spec: {
    name: 'withdraw_ask',
    description:
      'Take back a question you asked with post_ui that the user has not answered there: they answered it in a message instead, or it no longer matters. It leaves their "needs me" list and the message shows it was withdrawn with your reason. Do not withdraw a question just because the answer is slow to come.',
    parameters: {
      type: 'object',
      properties: {
        attentionId: { type: 'string' },
        reason: text('Short, shown to the user, e.g. "answered in your message".'),
      },
      required: ['attentionId', 'reason'],
    },
  },
  run(args, ctx) {
    ctx.app.withdrawAttention(ctx.conversationId, str(args, 'attentionId'), str(args, 'reason'))
    return { content: 'Withdrawn.' }
  },
}

/**
 * A message's blocks in a line each, for tools that list messages. `thread` looks up a
 * thread block's thread, so the line names it and where it stands now.
 */
export function blockLines(
  message: MessageView,
  thread: (id: string) => Pick<ThreadView, 'title' | 'stage'>,
): string[] {
  const shown = message.blocks.map((block) => {
    if (block.type === 'table')
      return `table${block.title ? ` "${block.title}"` : ''}: ${block.rows.length} rows`
    if (block.type === 'thread') {
      const { title, stage } = thread(block.threadId)
      return `thread ${block.threadId} "${title}" [${stage}]`
    }
    if (block.type === 'chart')
      return `${block.kind} chart${block.title ? ` "${block.title}"` : ''}: ${block.rows.length} points`
    if (block.type === 'connector') return `suggested service "${block.name}"`
    return `steps${block.title ? ` "${block.title}"` : ''}: ${block.items.map((item) => `${item.title} [${item.status}]`).join(', ')}`
  })
  const { ask } = message
  if (!ask) return shown
  const answer = ask.answer
    ? describeAnswer(ask.block, ask.answer)
    : ask.withdrawn
      ? `withdrawn: ${ask.withdrawn}`
      : 'waiting for the user'
  return [...shown, `${ask.block.type} "${askTitle(ask.block)}" (${ask.attentionId}): ${answer}`]
}
