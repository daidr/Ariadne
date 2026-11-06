import type { ReasoningRecord, TokenUsage, ToolCallRecord } from '@ariadne/core'
import { withImageData, type ModelMessage } from './transcript'
import { requestExtras } from './compaction'
import { DEFAULT_LIMITS, type Limits } from './limits'
import { requestStep } from './model-step'
import { callOf, type ResolvedModel } from './model'
import { researchHistory } from './trace'
import { str, ToolInputError, type Tool, type ToolContext, type ToolResult } from './tool-helpers'

/** Model requests one research makes at most; the last one has no tools, so it answers. */
export const MAX_RESEARCH_STEPS = 24

/** What a conversation's research runs with. */
export interface Researcher {
  model: ResolvedModel
  /** The conversation's own tools that only look; the research gets these. */
  tools: Tool[]
  limits: Limits
}

/** The tools a research never gets: it looks, and it does not start another research. */
const NOT_FOR_RESEARCH = new Set(['research', 'list_messages', 'read_command_output'])

/** Of a conversation's tools, those a research may call: the ones that only look. */
export const researchTools = (tools: Tool[]) =>
  tools.filter((tool) => tool.readOnly && !NOT_FOR_RESEARCH.has(tool.spec.name))

function researchSystem(role: 'coordinator' | 'thread', tools: Tool[]): string {
  return `You look into one question for a ${role === 'thread' ? 'colleague working on the code' : 'coordinator planning work'}, who reads only your final answer, not your steps. You can only look: ${tools.map((t) => t.spec.name).join(', ')}. You cannot change anything or ask anyone.

Look as far as the question needs, and no further. Then answer in the language of the question: the conclusion first, then the facts it rests on, each with where you found it (file path and line numbers, or the address). Say plainly what you did not find or could not check. Keep it short: no account of how you searched, no restating the question.`
}

const usageLine = (usage: TokenUsage) =>
  `${Math.round((usage.input + usage.output) / 1000)}k tokens`

/**
 * Run a research: a model (often a cheaper one) with the conversation's looking tools, in
 * a context of its own, until it answers. Each step is recorded with the call it answers,
 * so the trace shows it under that call; the conversation gets the answer alone.
 */
export async function runResearch(question: string, ctx: ToolContext): Promise<ToolResult> {
  const researcher = ctx.research
  if (!researcher || !ctx.turnId || !ctx.toolCallId) {
    return { content: 'Research is not available here.', isError: true }
  }
  const { model, tools } = researcher
  const limits = researcher.limits ?? DEFAULT_LIMITS
  const byName = new Map(tools.map((tool) => [tool.spec.name, tool]))
  const specs = tools.map((tool) => tool.spec)
  const system = researchSystem(ctx.role, tools)
  const input = {
    system,
    tools: JSON.stringify(specs),
    call: callOf(model, model.effort ?? null),
  }
  // The research's tools look on the conversation's behalf, but what they read is not what
  // the conversation saw, and they start no research of their own.
  const sub: ToolContext = { ...ctx, research: undefined, toolCallId: undefined, sub: true }
  const done: {
    text: string
    toolCalls: ToolCallRecord[]
    reasoning: ReasoningRecord[]
    results: ResultRecord[]
  }[] = []
  const total: TokenUsage = { input: 0, output: 0 }
  for (let number = 1; number <= MAX_RESEARCH_STEPS; number++) {
    const last = number === MAX_RESEARCH_STEPS
    const messages: ModelMessage[] = [
      { role: 'system', content: system },
      { role: 'user', content: question },
      ...researchHistory(done),
      ...(last
        ? [
            {
              role: 'user' as const,
              content:
                'That is as far as this research goes: answer now with what you found, and say what is still open.',
            },
          ]
        : []),
    ]
    const answer = await requestStep(
      model,
      {
        model: model.model,
        messages: withImageData(messages, () => null),
        tools: last ? [] : specs,
        ...(model.effort ? { effort: model.effort } : {}),
        conversation: `${ctx.conversationId}/research/${ctx.toolCallId}`,
        ...requestExtras(model),
        signal: ctx.signal,
      },
      () => {},
      1000,
      limits,
      model.request?.timeoutSeconds,
    )
    if (!answer) return { content: 'The research was stopped.', isError: true }
    const answeredAt = Date.now()
    if (answer.usage) {
      total.input += answer.usage.input
      total.output += answer.usage.output
    }
    const results: ResultRecord[] = []
    for (const call of answer.toolCalls) {
      if (ctx.signal.aborted) break
      results.push({ toolCallId: call.id, name: call.name, ...(await runOne(byName, call, sub)) })
    }
    ctx.app.recordResearchStep(ctx.conversationId, ctx.turnId, {
      toolCallId: ctx.toolCallId,
      number,
      question,
      text: answer.text,
      reasoning: answer.reasoning,
      toolCalls: answer.toolCalls,
      results,
      finish: answer.cut ? 'length' : answer.toolCalls.length ? 'tool-calls' : 'stop',
      usage: answer.usage,
      input,
      timing: answer.timing,
      answeredAt,
    })
    if (ctx.signal.aborted) return { content: 'The research was stopped.', isError: true }
    if (!answer.toolCalls.length && !answer.cut) {
      const text = answer.text.trim() || '(The research ended without an answer.)'
      return {
        content: `${text}\n\n[research: ${number} ${number === 1 ? 'request' : 'requests'}, ${usageLine(total)}, ${model.label}]`,
      }
    }
    done.push({
      text: answer.text,
      toolCalls: answer.toolCalls,
      reasoning: answer.reasoning,
      results,
    })
  }
  return { content: 'The research ended without an answer.', isError: true }
}

interface ResultRecord {
  toolCallId: string
  name: string
  content: string
  isError: boolean
}

async function runOne(
  byName: ReadonlyMap<string, Tool>,
  call: ToolCallRecord,
  ctx: ToolContext,
): Promise<{ content: string; isError: boolean }> {
  const tool = byName.get(call.name)
  if (!tool) return { content: `Unknown tool "${call.name}"; you can only look.`, isError: true }
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
    const result = await tool.run(args as Record<string, unknown>, ctx)
    const images = result.files?.length ? '\n[an image; a research cannot look at it]' : ''
    return { content: result.content + images, isError: result.isError ?? false }
  } catch (error) {
    return { content: error instanceof Error ? error.message : String(error), isError: true }
  }
}

export const researchTool: Tool = {
  spec: {
    name: 'research',
    description:
      'Hand one question that takes a lot of looking (where something is done across the code, how a part works, what the docs of a library say) to a research of its own: it reads files and the web with read-only tools in a separate context, and you get only its conclusion with where it found things. It keeps your context small. Not for something one or two reads answer, and it cannot change anything.',
    parameters: {
      type: 'object',
      properties: {
        question: {
          type: 'string',
          description:
            'The question, with what you already know and what the answer is for, so it knows when it is done.',
        },
      },
      required: ['question'],
    },
  },
  run(args, ctx) {
    const question = str(args, 'question')
    if (question.length > 8000) throw new ToolInputError('"question" is too long; ask one thing')
    return runResearch(question, ctx)
  },
}
