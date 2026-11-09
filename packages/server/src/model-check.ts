import type { ResolvedModel } from '@ariadne/agent'
import type { LocalizedMessage } from '@ariadne/i18n'
import type { ModelInfo } from './prices'

/** How long a model may take to start answering before the check gives up. */
const CHECK_TIMEOUT_MS = 20_000

/** What is wrong with a model as set up, or `null` when it answers. */
export type ModelCheck = (model: ResolvedModel) => Promise<LocalizedMessage | null>

/**
 * Ask the model for one word and stop at the first sign of an answer: the address, the key
 * and the model's name are right then. A provider with a check of its own (an agent
 * program, which takes long to start) is asked that instead.
 */
export const checkModel: ModelCheck = async (model) => {
  if (model.provider.check) {
    const problem = await model.provider.check(model.settings as never)
    return problem ? { key: 'error.modelCheck', params: { detail: problem } } : null
  }
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), CHECK_TIMEOUT_MS)
  try {
    const parts = model.provider.stream(model.settings as never, {
      model: model.model,
      messages: [{ role: 'user', content: 'Hi' }],
      tools: [],
      cache: false,
      ...(model.request?.headers ? { headers: model.request.headers } : {}),
      ...(model.request?.extraBody ? { extraBody: model.request.extraBody } : {}),
      signal: controller.signal,
    })
    for await (const part of parts) {
      if (part.type === 'finish' && part.reason === 'error') {
        return { key: 'error.modelCheck', params: { detail: part.error ?? '' } }
      }
      // It is answering: the rest of the answer is not needed.
      return null
    }
    return controller.signal.aborted
      ? { key: 'error.modelCheck', params: { detail: 'timed out' } }
      : null
  } catch (error) {
    return {
      key: 'error.modelCheck',
      params: { detail: error instanceof Error ? error.message : String(error) },
    }
  } finally {
    clearTimeout(timer)
    controller.abort()
  }
}

/** Names of models that are not for conversations with tools, or are the small ones. */
const NOT_FIRST =
  /embed|tts|whisper|audio|realtime|image|dall-e|sora|moderation|transcribe|search|babbage|davinci|instruct|mini|nano|lite|haiku|flash|small|guard|codex|vision|rerank|ocr|preview/i

/**
 * The model to start with among those a service lists (newest first): the newest the
 * catalog knows to call tools, leaving out the small and the special-purpose ones; else
 * the first that is not one of those by its name.
 */
export function suggestModel(
  listed: string[],
  info: ReadonlyMap<string, ModelInfo>,
): string | null {
  const general = listed.filter((name) => !NOT_FIRST.test(name))
  const known = general
    .flatMap((name) => {
      const facts = info.get(name)
      return facts && facts.tools !== false && facts.released ? [{ name, facts }] : []
    })
    .toSorted((a, b) => b.facts.released!.localeCompare(a.facts.released!))
  return known[0]?.name ?? general[0] ?? listed[0] ?? null
}
