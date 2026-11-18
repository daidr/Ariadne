import { Ariadne } from '../ariadne'
import { DomainError } from '../errors'
import type { AnyNewEvent } from '../events'

const input = (await Bun.stdin.json()) as {
  seed: AnyNewEvent[]
  commands: { op: string; args: any }[]
}
const app = new Ariadne({ path: ':memory:', now: () => 1_800_000_000_000 })
try {
  app.store.append(input.seed)
  console.info(
    JSON.stringify(
      input.commands.map(({ op, args: a }) => {
        let value: unknown = null
        let error: unknown = null
        const reviewer = app.getThread('thr_a').review?.reviewerId
        const id = a.id === 'reviewer' ? reviewer! : (a.id ?? 'thr_a')
        try {
          switch (op) {
            case 'ready':
              value = app.reportThread('thr_a', a.report, a.head, a.review)
              break
            case 'verdict':
              value = app.submitReview(reviewer!, a.input)
              break
            case 'handled': {
              const turn = app.startTurn(id, 'model_a')
              app.recordStep(id, turn, {
                text: '',
                reasoning: [],
                toolCalls: [],
                sawSeq: app.lastSeq(),
                usage: null,
                input: { system: 'review_test', tools: '[]', call: '{}', images: true },
                timing: { startedAt: 1_800_000_000_000, firstOutputAt: null },
              })
              app.finishTurn(id, turn, a.outcome ?? 'completed')
              break
            }
            case 'complete':
              value = app.completeThread('thr_a')
              break
            case 'delivery':
              value = app.setProjectDelivery('prj_a', a.input)
              break
            case 'settings':
              value = app.setProjectSettings('prj_a', a.input, [])
              break
            case 'stop':
              value = app.stopThread(id, { by: a.by, reason: a.reason })
              break
            case 'resume':
              value = app.resumeThread(id, a.by)
              break
            case 'return':
              app.noteReturn('prj_a', a.away)
              break
            default:
              throw new Error(`unknown command ${op}`)
          }
        } catch (caught) {
          error = caught instanceof DomainError ? caught.toLocalized() : { message: String(caught) }
          value = null
        }
        return {
          value,
          error,
          detail: app.getProjectDetail('prj_a'),
          reviews: app.listReviews('thr_a'),
          open: app.openReviewRound('thr_a'),
          passed: app.passedReview('thr_a'),
          reads: app.listThreads('prj_a').map((thread) => ({
            id: thread.id,
            transcript: app.listTranscript(thread.id),
            notices: app.listNotices(thread.id),
          })),
          coordinator: app.listTranscript('crd_a'),
          events: app
            .events()
            .map(({ seq, type, projectId, data, at }) => ({ seq, type, projectId, data, at })),
        }
      }),
    ),
  )
} finally {
  app.close()
}
