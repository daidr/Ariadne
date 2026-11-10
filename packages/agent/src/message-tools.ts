import { DomainError, MAX_REACTIONS, type ThreadView } from '@ariadne/core'
import {
  json,
  optionalBool,
  optionalInt,
  optionalStr,
  ownThread,
  str,
  ToolInputError,
  withImages,
  type Tool,
  type ToolContext,
} from './tool-helpers'

export const reactTool: Tool = {
  spec: {
    name: 'react',
    description: `Put an emoji on a message of your conversation, e.g. 👀 on a request you are starting on or 👍 on a go-ahead: an answer that needs no words. Not for your own messages. At most ${MAX_REACTIONS} per message; "remove" takes one back.`,
    parameters: {
      type: 'object',
      properties: {
        messageId: { type: 'string' },
        emoji: { type: 'string', description: 'One emoji character, e.g. "👍".' },
        remove: { type: 'boolean' },
      },
      required: ['messageId', 'emoji'],
    },
  },
  run(args, ctx) {
    const messageId = str(args, 'messageId')
    const remove = optionalBool(args, 'remove')
    try {
      ctx.app.react(
        messageId,
        { author: ctx.role, conversationId: ctx.conversationId },
        str(args, 'emoji'),
        remove,
      )
    } catch (error) {
      if (!(error instanceof DomainError)) throw error
      const reason = error.params?.reason
      throw new ToolInputError(
        error.code === 'error.notFound'
          ? `No message ${messageId} in your conversation.`
          : reason === 'emoji'
            ? '"emoji" must be one emoji character, e.g. "👍".'
            : reason === 'reactions'
              ? `You already put ${MAX_REACTIONS} emojis on that message; remove one first.`
              : 'You cannot react to your own messages.',
      )
    }
    return { content: remove ? 'Removed.' : 'Reacted.' }
  },
}

/** The thread a coordinator's call names, or the calling thread itself. */
function targetThread(args: Record<string, unknown>, ctx: ToolContext): ThreadView {
  return ctx.role === 'coordinator'
    ? ownThread(ctx, str(args, 'threadId'))
    : ctx.app.getThread(ctx.conversationId)
}

const threadIdParam = { threadId: { type: 'string' } }

export const renameThreadTool = (role: 'coordinator' | 'thread'): Tool => ({
  spec: {
    name: 'rename_thread',
    description:
      role === 'thread'
        ? "Give your thread a new title, when the work turned out to be something else than its title says. The user finds the thread by it: three to six words, the goal first, in the user's language."
        : "Give a thread a new title, when its work turned out to be something else than its title says. The user finds the thread by it: three to six words, the goal first, in the user's language.",
    parameters: {
      type: 'object',
      properties: { ...(role === 'coordinator' ? threadIdParam : {}), title: { type: 'string' } },
      required: [...(role === 'coordinator' ? ['threadId'] : []), 'title'],
    },
  },
  run(args, ctx) {
    const thread = targetThread(args, ctx)
    try {
      ctx.app.renameThread(thread.id, str(args, 'title'), ctx.conversationId)
    } catch (error) {
      if (error instanceof DomainError && error.params?.reason === 'title') {
        throw new ToolInputError('"title" must be a short line of at most 80 characters')
      }
      throw error
    }
    return { content: 'Renamed.' }
  },
})

/** The longest a thread is set aside for at once. */
export const MAX_ASIDE_DAYS = 30

export const setAsideTool = (role: 'coordinator' | 'thread'): Tool => ({
  spec: {
    name: 'set_aside',
    description: `${role === 'thread' ? 'Set your thread aside' : 'Set a thread aside'} for some days, when the user says not to go on with it for now. It moves out of the user's way and comes back to them under "needs me" when the days are over, with your reason. Writing to the thread takes it up again sooner. "days" 0 takes it up again now. Then end your turn.`,
    parameters: {
      type: 'object',
      properties: {
        ...(role === 'coordinator' ? threadIdParam : {}),
        days: { type: 'integer', description: `1 to ${MAX_ASIDE_DAYS}, or 0.` },
        reason: {
          type: 'string',
          description: "What it waits on, shown when it comes back; the user's language.",
        },
      },
      required: [...(role === 'coordinator' ? ['threadId'] : []), 'days'],
    },
  },
  run(args, ctx) {
    const thread = targetThread(args, ctx)
    const days = optionalInt(args, 'days')
    if (days === null || days < 0 || days > MAX_ASIDE_DAYS) {
      throw new ToolInputError(`"days" must be 0 to ${MAX_ASIDE_DAYS}`)
    }
    if (days === 0) {
      return {
        content: ctx.app.endAside(thread.id, ctx.conversationId)
          ? 'Taken up again.'
          : 'It was not set aside.',
      }
    }
    const { aside } = ctx.app.setAside(thread.id, {
      days,
      reason: optionalStr(args, 'reason') ?? '',
      by: ctx.conversationId,
    })
    return { content: `Set aside until ${new Date(aside!.until).toISOString().slice(0, 10)}.` }
  },
})

/** How much of each message found the model reads. */
const FOUND_CHARS = 400

export const searchMessagesTool: Tool = {
  readOnly: true,
  spec: {
    name: 'search_messages',
    description:
      "Search what was said in every conversation of the project (the coordinator's and all threads'), and threads by title. Every word must match; Chinese matches by three or more characters in a row. Newest first.",
    parameters: {
      type: 'object',
      properties: {
        query: { type: 'string' },
        limit: { type: 'integer', description: 'Messages to return, default 10, at most 30.' },
      },
      required: ['query'],
    },
  },
  run(args, ctx) {
    const limit = Math.min(Math.max(optionalInt(args, 'limit') ?? 10, 1), 30)
    const found = ctx.app.search(ctx.projectId, str(args, 'query'))
    if (!found.terms.length) throw new ToolInputError('"query" has no words to look for')
    const messages = found.messages.slice(0, limit).flatMap((hit) => {
      let message
      try {
        message = ctx.app.getMessage(hit.id)
      } catch {
        return []
      }
      const text = [...message.content.replace(/\s+/g, ' ').trim()]
      return [
        {
          id: hit.id,
          where:
            hit.conversationId === ctx.conversationId
              ? 'here'
              : hit.where === null
                ? 'coordinator'
                : `thread ${hit.conversationId} "${hit.where}"`,
          author: message.author,
          at: new Date(hit.at).toISOString(),
          text:
            text.length > FOUND_CHARS ? `${text.slice(0, FOUND_CHARS).join('')}…` : text.join(''),
        },
      ]
    })
    return json({
      messages,
      threads: found.threads.map((hit) => ({ id: hit.id, title: hit.title })),
    })
  },
}

export const suggestConnectorTool: Tool = {
  spec: {
    name: 'suggest_connector',
    description:
      'Suggest the user add an external tool server (MCP) the work needs and you cannot reach otherwise, e.g. an issue tracker or a database. It posts a message with a card that opens the settings with the server filled in; the user only adds the secrets and saves, and it is then on for this project. Name the secrets it needs, never their values. Suggest each service once.',
    parameters: {
      type: 'object',
      properties: {
        text: { type: 'string', description: "A short lead-in, in the user's language." },
        name: { type: 'string', description: 'What it is called, e.g. "Linear".' },
        why: { type: 'string', description: 'What it lets you do here, one sentence.' },
        transport: { type: 'string', enum: ['stdio', 'http'] },
        command: { type: 'string', description: 'stdio: the program, e.g. "npx".' },
        args: { type: 'array', items: { type: 'string' }, description: 'stdio: its arguments.' },
        url: { type: 'string', description: 'http: its address.' },
        secrets: {
          type: 'array',
          items: { type: 'string' },
          description:
            'Names of the environment variables (stdio) or headers (http) the user fills in, e.g. "GITHUB_TOKEN" or "Authorization".',
        },
      },
      required: ['name', 'why', 'transport'],
    },
  },
  async run(args, ctx) {
    const { text, ...rest } = args
    if (text !== undefined && text !== null && typeof text !== 'string') {
      throw new ToolInputError('"text" must be a string')
    }
    const content = await withImages(ctx, typeof text === 'string' ? text.trim() : '')
    let message
    try {
      message = ctx.app.postMessage(ctx.projectId, {
        conversationId: ctx.conversationId,
        author: ctx.role,
        content,
        blocks: [{ ...rest, type: 'connector' }],
      })
    } catch (error) {
      if (error instanceof DomainError && error.code === 'error.blockInvalid') {
        throw new ToolInputError(String(error.params?.reason ?? 'invalid').replace(/^block\./, ''))
      }
      throw error
    }
    return { content: `Suggested (${message.id}).` }
  },
}
