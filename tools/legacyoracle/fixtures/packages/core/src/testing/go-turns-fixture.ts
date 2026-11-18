import { Ariadne } from '../ariadne'
import { DomainError } from '../errors'
import type { AnyNewEvent } from '../events'

const input = (await Bun.stdin.json()) as {
  seed: AnyNewEvent[]
  commands: { op: string; args: any }[]
}
const app = new Ariadne({ path: ':memory:', now: () => 1_800_000_000_000 })
const turns = new Map<string, string>()
const waits = new Map<string, string>()
try {
  app.store.append(input.seed)
  console.info(
    JSON.stringify(
      input.commands.map(({ op, args: a }) => {
        let value: unknown = null
        let error: unknown = null
        const id = a.id as string
        const turn = a.wrong ? 'wrong_turn' : turns.get(id)!
        try {
          switch (op) {
            case 'request':
              app.requestTurn(id)
              break
            case 'start':
              value = app.startTurn(id, a.model)
              turns.set(id, value as string)
              break
            case 'step':
              app.recordStep(id, turn, { ...a.step, sawSeq: a.all ? app.lastSeq() : a.step.sawSeq })
              break
            case 'changes':
              app.recordTurnChanges(id, turn, a.input)
              break
            case 'retry':
              app.recordModelRetry(id, turn, {
                attempt: a.attempt,
                delayMs: a.delayMs,
                detail: a.detail,
              })
              break
            case 'fallback':
              app.recordModelFallback(id, turn, { from: a.from, to: a.to, detail: a.detail })
              break
            case 'search':
              app.recordSearch(id, turn, { model: a.model, usage: a.usage })
              break
            case 'research':
              app.recordResearchStep(id, turn, a.step)
              break
            case 'instructions':
              value = app.recordInstructions(id, turn, a.file)
              break
            case 'prompt':
              app.updatePrompt(id, turn, a)
              break
            case 'compact':
              app.compactConversation(id, turn, a.compaction)
              break
            case 'compaction-failed':
              app.recordCompactionFailure(id, turn, a.detail)
              break
            case 'clear':
              app.clearToolResults(id, turn, a)
              break
            case 'tool':
              app.recordToolResult(id, turn, a.result)
              break
            case 'finish':
              app.finishTurn(id, turn, a.outcome, a.error ?? null)
              break
            case 'fail':
              app.failBeforeTurn(id, a.error ? new DomainError(a.error.key, a.error.params) : null)
              break
            case 'wait':
              value = app.setWait(id, a)
              waits.set(id, value as string)
              break
            case 'end-wait':
              value = app.endWait(id, a.how, a.by, a.wrong ? 'wrong_wait' : waits.get(id))
              break
            default:
              throw new Error(`unknown command ${op}`)
          }
        } catch (caught) {
          error = caught instanceof DomainError ? caught.toLocalized() : { message: String(caught) }
          value = null
        }
        const ids = ['crd_a', 'thr_a']
        return {
          value,
          error,
          events: app
            .events()
            .map(({ seq, type, projectId, data, at }) => ({ seq, type, projectId, data, at })),
          thread: app.getThread('thr_a'),
          reads: ids.map((conversationId) => ({
            conversation: app.getConversation(conversationId),
            transcript: app.listTranscript(conversationId),
            todo: app.getTodo(conversationId),
            triage: app.pendingIsTriage(conversationId),
            userMessage: app.pendingIsUserMessage(conversationId),
            instructions: app.getInstructions(conversationId),
            count: app.countRequests(conversationId, Number.MAX_SAFE_INTEGER),
            last: app.lastStepBefore(conversationId, Number.MAX_SAFE_INTEGER),
            research: app.researchSteps(conversationId, 'call_research', Number.MAX_SAFE_INTEGER),
            trace: [
              app.listTurnEvents(conversationId),
              app.listTurnEvents(conversationId, { turns: 1 }),
              app.listTurnEvents(conversationId, {
                turns: 2,
                before: app
                  .events()
                  .findLast(
                    (event) =>
                      event.type === 'turn.started' && event.data.conversationId === conversationId,
                  )?.seq,
              }),
            ],
          })),
          prompts: app.store.db.query('SELECT * FROM prompts ORDER BY hash').all(),
          waits: app.listWaits(),
          pending: app.listPendingConversations(),
          running: app.listRunningConversations(),
        }
      }),
    ),
  )
} finally {
  app.close()
}
