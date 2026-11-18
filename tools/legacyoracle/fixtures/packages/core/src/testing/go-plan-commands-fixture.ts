import { Ariadne } from '../ariadne'
import { DomainError } from '../errors'
import { confirmBlock } from '../blocks'
import type { AnyNewEvent } from '../events'

const input = (await Bun.stdin.json()) as {
  seed: AnyNewEvent[]
  commands: { op: string; args: any }[]
}
const app = new Ariadne({ path: ':memory:', now: () => 1_800_000_000_000 })
const revisions = new Map<string, string>()
let attention = ''
let proposal = ''
try {
  app.store.append(input.seed)
  console.info(
    JSON.stringify(
      input.commands.map(({ op, args: a }) => {
        let value: any = null
        let error: unknown = null
        const revision = revisions.get(a.revision) ?? a.revision
        const confirmation = a.confirm
          ? { card: () => confirmBlock({ title: '确认调整计划？' }) }
          : undefined
        try {
          switch (op) {
            case 'revise':
              value = app.revisePlan('prj_a', {
                author: a.author,
                reason: a.reason,
                ops: a.ops,
                confirmation,
              })
              break
            case 'revert':
              value = app.revertPlanRevision('prj_a', revision, a.author, a.reason, confirmation)
              break
            case 'preview':
              value = app.previewPlanUndo('prj_a', revision)
              break
            case 'describe':
              value = app.describePlanChange('prj_a', a.ops)
              break
            case 'answer':
              app.answerAttention(attention, a.answer)
              break
            case 'dismiss':
              app.dismissPlanProposal('prj_a', proposal)
              break
            case 'linkIssue':
              value = app.linkIssue('prj_a', a.nodeId, a.issue, 'user')
              break
            case 'unlinkIssue':
              value = app.unlinkIssue('prj_a', a.nodeId, a.issue, 'user')
              break
            case 'gate':
              value = app.askGate('prj_a', a.nodeId, confirmBlock({ title: '继续下一阶段？' }))
              attention = value.id
              break
            case 'auto':
              value = app.startAutoThread('prj_a', a)
              break
            default:
              throw new Error(`unknown command ${op}`)
          }
          if (value?.status === 'applied' && a.tag) revisions.set(a.tag, value.revisionId)
          if (value?.status === 'proposed') {
            attention = value.attentionId
            proposal = value.proposalId
          }
          if (op === 'answer' && a.tag) revisions.set(a.tag, app.getPlan('prj_a').revisions[0]!.id)
        } catch (caught) {
          error = caught instanceof DomainError ? caught.toLocalized() : { message: String(caught) }
          value = null
        }
        return {
          value,
          error,
          plan: app.getPlan('prj_a'),
          detail: app.getProjectDetail('prj_a'),
          transcript: app.listTranscript('crd_a'),
          proposals: app.store.db.query('SELECT * FROM plan_proposals ORDER BY id').all(),
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
