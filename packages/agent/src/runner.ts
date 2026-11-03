import {
  DomainError,
  type Ariadne,
  type ConversationView,
  type ToolCallRecord,
} from '@ariadne/core'
import type { Locale, LocalizedMessage } from '@ariadne/i18n'
import type { ModelProvider } from '@ariadne/plugin-sdk'
import { coordinatorPrompt, threadPrompt, type ThreadContext } from './prompts'
import { toolsFor, ToolInputError, type Tool, type ToolContext } from './tools'
import { toChatMessages } from './transcript'
import type { WorkspaceManager } from './workspaces'

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
  /** Opens threads' workspaces; without it threads get no file or command tools. */
  workspaces?: WorkspaceManager
}

/** Why a turn stopped early, for the user. */
class TurnError extends Error {
  constructor(readonly localized: LocalizedMessage) {
    super(localized.key)
  }
}

function threadContext(
  app: Ariadne,
  conversation: ConversationView,
  locale: Locale,
  hasWorkspace: boolean,
): ThreadContext {
  const project = app.getProject(conversation.projectId)
  const thread = app.getThread(conversation.id)
  const plan = thread.nodeId ? app.getPlan(project.id) : null
  const node = plan?.nodes.find((n) => n.id === thread.nodeId) ?? null
  const threads = node ? app.listThreads(project.id) : []
  const upstream = (node?.dependsOn ?? []).flatMap((id) => {
    const title = plan?.nodes.find((n) => n.id === id)?.title ?? id
    return threads
      .filter((t) => t.nodeId === id && t.lastReport === 'done' && t.lastSummary)
      .map((t) => ({ node: title, summary: t.lastSummary as string }))
  })
  return {
    projectName: project.name,
    title: thread.title,
    locale,
    node: node
      ? { title: node.title, description: node.description, acceptance: node.acceptance }
      : null,
    upstream,
    workspace: !hasWorkspace ? 'none' : (thread.workspace ?? 'not-open'),
  }
}

function systemPrompt(
  app: Ariadne,
  conversation: ConversationView,
  locale: Locale,
  hasWorkspace: boolean,
): string {
  if (conversation.role === 'coordinator') {
    return coordinatorPrompt({ projectName: app.getProject(conversation.projectId).name, locale })
  }
  return threadPrompt(threadContext(app, conversation, locale, hasWorkspace))
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
  const { workspaces } = options
  const hasWorkspace =
    conversation.role === 'thread' && !!workspaces?.available(conversation.projectId)
  const tools = options.tools ?? toolsFor(conversation.role, hasWorkspace)
  const byName = new Map(tools.map((tool) => [tool.spec.name, tool]))
  const conversationId = conversation.id
  const turnId = app.startTurn(conversationId, model.label)
  const ctx: ToolContext = {
    app,
    projectId: conversation.projectId,
    conversationId,
    role: conversation.role,
    locale: options.locale,
    signal,
    workspace: () => {
      if (!workspaces || conversation.role !== 'thread') {
        return Promise.reject(new DomainError('error.noSource'))
      }
      return workspaces.forThread(conversation.projectId, conversationId)
    },
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
        {
          role: 'system' as const,
          content: systemPrompt(app, conversation, options.locale, hasWorkspace),
        },
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
        } else if (part.type === 'finish' && part.reason === 'length') {
          // A cut-off reply is not an answer, and cut-off tool arguments are not a call.
          throw new TurnError({ key: 'error.truncated' })
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
