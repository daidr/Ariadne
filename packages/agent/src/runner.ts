import {
  DomainError,
  type Ariadne,
  type ConversationView,
  type ToolCallRecord,
} from '@ariadne/core'
import type { Locale, LocalizedMessage } from '@ariadne/i18n'
import type { ModelProvider } from '@ariadne/plugin-sdk'
import { coordinatorPrompt, threadPrompt } from './prompts'
import { coordinatorTools, threadTools, ToolInputError, type Tool, type ToolContext } from './tools'
import { toChatMessages } from './transcript'

/** The model a conversation talks to, with its provider's settings. */
export interface ResolvedModel {
  provider: ModelProvider<never>
  settings: unknown
  model: string
  /** Shown to the user, e.g. `anthropic / some-model`. */
  label: string
}

export interface TurnOptions {
  app: Ariadne
  conversation: ConversationView
  model: ResolvedModel
  locale: Locale
  signal: AbortSignal
  maxSteps: number
  tools?: Tool[]
}

/** Why a turn stopped early, for the user. */
class TurnError extends Error {
  constructor(readonly localized: LocalizedMessage) {
    super(localized.key)
  }
}

function systemPrompt(app: Ariadne, conversation: ConversationView, locale: Locale): string {
  const project = app.getProject(conversation.projectId)
  if (conversation.role === 'coordinator') {
    return coordinatorPrompt({ projectName: project.name, locale })
  }
  const thread = app.getThread(conversation.id)
  return threadPrompt({ projectName: project.name, title: thread.title, locale })
}

async function runTool(tool: Tool | undefined, call: ToolCallRecord, ctx: ToolContext) {
  if (!tool) return { content: `Unknown tool "${call.name}".`, isError: true }
  let args: unknown
  try {
    args = JSON.parse(call.arguments || '{}')
  } catch {
    return { content: 'The arguments are not valid JSON.', isError: true }
  }
  if (!args || typeof args !== 'object' || Array.isArray(args)) {
    return { content: 'The arguments must be a JSON object.', isError: true }
  }
  try {
    return await tool.run(args as Record<string, unknown>, ctx)
  } catch (error) {
    if (error instanceof ToolInputError) return { content: error.message, isError: true }
    if (error instanceof DomainError) {
      return { content: `${error.code} ${JSON.stringify(error.params ?? {})}`, isError: true }
    }
    throw error
  }
}

/**
 * Run one turn: ask the model, run the tools it calls, and repeat until it answers without
 * calls. Every step is an event, and the model's input is rebuilt from the log each time,
 * so a turn can be replayed or resumed from what is stored.
 */
export async function runTurn(options: TurnOptions): Promise<void> {
  const { app, conversation, model, signal } = options
  const tools =
    options.tools ?? (conversation.role === 'coordinator' ? coordinatorTools : threadTools)
  const byName = new Map(tools.map((tool) => [tool.spec.name, tool]))
  const conversationId = conversation.id
  const turnId = app.startTurn(conversationId, model.label)
  const ctx: ToolContext = {
    app,
    projectId: conversation.projectId,
    conversationId,
    role: conversation.role,
    signal,
  }
  const titleOf = (threadId: string) => {
    try {
      return app.getThread(threadId).title
    } catch {
      return threadId
    }
  }

  try {
    for (let step = 0; step < options.maxSteps; step++) {
      const sawSeq = app.lastSeq()
      const messages = [
        { role: 'system' as const, content: systemPrompt(app, conversation, options.locale) },
        ...toChatMessages(app.listTranscript(conversationId), titleOf),
      ]
      let text = ''
      const toolCalls: ToolCallRecord[] = []
      let usage: { input: number; output: number } | null = null
      for await (const part of model.provider.stream(model.settings as never, {
        model: model.model,
        messages,
        tools: tools.map((tool) => tool.spec),
        signal,
      })) {
        if (part.type === 'text') text += part.text
        else if (part.type === 'tool-call')
          toolCalls.push({ id: part.id, name: part.name, arguments: part.arguments })
        else if (part.type === 'usage') usage = { input: part.input, output: part.output }
        else if (part.type === 'finish' && part.reason === 'error') {
          throw new TurnError({ key: 'error.modelRequest', params: { detail: part.error ?? '' } })
        }
      }
      if (signal.aborted) break
      app.recordStep(conversationId, turnId, { text, toolCalls, sawSeq, usage })

      if (!toolCalls.length) {
        if (text.trim()) {
          app.postMessage(conversation.projectId, {
            conversationId,
            author: conversation.role,
            content: text.trim(),
          })
        }
        app.finishTurn(conversationId, turnId, 'completed')
        return
      }
      for (const call of toolCalls) {
        if (signal.aborted) break
        const result = await runTool(byName.get(call.name), call, ctx)
        app.recordToolResult(conversationId, turnId, {
          toolCallId: call.id,
          name: call.name,
          content: result.content,
          isError: result.isError ?? false,
        })
      }
      if (signal.aborted) break
    }
    if (signal.aborted) {
      app.finishTurn(conversationId, turnId, abortOutcome(signal))
      return
    }
    app.finishTurn(conversationId, turnId, 'failed', {
      key: 'error.stepLimit',
      params: { limit: options.maxSteps },
    })
  } catch (error) {
    if (signal.aborted) {
      app.finishTurn(conversationId, turnId, abortOutcome(signal))
      return
    }
    const localized =
      error instanceof TurnError
        ? error.localized
        : error instanceof DomainError
          ? error.toLocalized()
          : { key: 'error.modelRequest' as const, params: { detail: String(error) } }
    app.finishTurn(conversationId, turnId, 'failed', localized)
  }
}

/** The scheduler aborts with `interrupted` on shutdown and `cancelled` when the user stops. */
function abortOutcome(signal: AbortSignal): 'cancelled' | 'interrupted' {
  return signal.reason === 'interrupted' ? 'interrupted' : 'cancelled'
}
