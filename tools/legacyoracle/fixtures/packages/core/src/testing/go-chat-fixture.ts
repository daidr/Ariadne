import { Ariadne } from '../ariadne'
import { DomainError } from '../errors'
import type { AnyNewEvent } from '../events'

const input = (await Bun.stdin.json()) as {
  seed: AnyNewEvent[]
  sql: string
  commands: { op: string; args: any }[]
}
const app = new Ariadne({ path: ':memory:', now: () => 1_800_000_000_000 })
let attention = ''
try {
  app.store.append(input.seed)
  app.store.db.exec(input.sql)
  console.info(
    JSON.stringify(
      input.commands.map(({ op, args: a }) => {
        let value: any = null
        let error: unknown = null
        try {
          switch (op) {
            case 'post':
              value = app.postMessage(a.projectId, a.input)
              if (value.ask) attention = value.ask.attentionId
              break
            case 'open':
              value = app.openAttention(a.projectId, a.input)
              attention = value.id
              break
            case 'answer':
              app.answerAttention(a.id === 'latest' ? attention : a.id, a.answer)
              break
            case 'withdraw':
              app.withdrawAttention(
                a.conversationId,
                a.id === 'latest' ? attention : a.id,
                a.reason,
              )
              break
            case 'stop':
              value = app.stopThread(a.id, { reason: a.reason, by: a.by })
              break
            case 'edit':
              value = app.editMessage(
                a.id,
                { author: a.author, conversationId: a.conversationId },
                a.content,
                a.reason,
              )
              break
            case 'read':
              app.markRead(a.conversationId, a.upTo)
              break
            case 'reactUser':
              value = app.reactAsUser(a.id, a.emoji, a.remove)
              break
            case 'react':
              value = app.react(
                a.id,
                { author: a.author, conversationId: a.conversationId },
                a.emoji,
                a.remove,
              )
              break
            case 'approvalFor':
              value = app.approvalFor('prj_a', a.threadId, a.purpose)
              break
            case 'requestApproval':
              value = app.requestApproval('prj_a', a.threadId, a.purpose, () => a.card)
              break
            case 'callApproval':
              value = app.callApproval('prj_a', a.threadId, a.purpose, () => a.card, a.options)
              break
            case 'useApproval':
              app.useApproval('prj_a', {
                threadId: a.threadId,
                attentionId: a.id === 'latest' ? attention : a.id,
                what: a.what,
              })
              break
            case 'issueApproval':
              value = app.issueApproval('prj_a', a.threadId, a.purpose, () => a.card)
              break
            case 'startIssueWrite':
              app.startIssueWrite('prj_a', {
                ...a.write,
                attentionId: a.write.attentionId === 'latest' ? attention : a.write.attentionId,
              })
              break
            case 'finishIssueWrite':
              app.finishIssueWrite(
                'prj_a',
                {
                  ...a.write,
                  attentionId: a.write.attentionId === 'latest' ? attention : a.write.attentionId,
                },
                a.url,
              )
              break
            case 'unsureIssueWrite':
              app.markIssueWriteUnsure('prj_a', a.id === 'latest' ? attention : a.id, a.threadId)
              break
            default:
              throw new Error(`unknown command ${op}`)
          }
          if (op === 'callApproval' || op === 'requestApproval' || op === 'issueApproval') {
            const opened = app
              .events()
              .filter((event) => event.type === 'attention.opened')
              .at(-1)
            if (opened?.type === 'attention.opened') attention = opened.data.attentionId
          }
        } catch (caught) {
          error = caught instanceof DomainError ? caught.toLocalized() : { message: String(caught) }
          value = null
        }
        return {
          value,
          error,
          detail: app.getProjectDetail('prj_a'),
          reads: ['crd_a', 'thr_a', 'thr_b'].map((id) => ({
            messages: app.listMessages(id),
            transcript: app.listTranscript(id),
            notices: app.listNotices(id),
          })),
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
