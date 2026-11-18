import { Ariadne } from '../ariadne'
import { DomainError } from '../errors'
import { lineDiff } from '../text-diff'
import type { AnyNewEvent } from '../events'

const input = (await Bun.stdin.json()) as {
  seed: AnyNewEvent[]
  sql: string
  commands: { op: string; args: any }[]
}
const app = new Ariadne({ path: ':memory:', now: () => 1_800_000_000_000 })
const ids = new Map<string, string>()
try {
  app.store.append(input.seed)
  app.store.db.exec(input.sql)
  console.info(
    JSON.stringify(
      input.commands.map(({ op, args: a }) => {
        let value: unknown = null
        let error: unknown = null
        const id = ids.get(a.id) ?? a.id
        try {
          switch (op) {
            case 'create': {
              const artifact = app.createArtifact(a.projectId ?? 'prj_a', a.input)
              value = artifact
              ids.set(a.id, artifact.id)
              break
            }
            case 'revise':
              value = app.reviseArtifact(id, a.input)
              break
            case 'comment':
              value = app.commentOnArtifact(id, a.by, a.action)
              break
            case 'feedback':
              value = app.sendArtifactFeedback(id, a.input)
              break
            case 'finish':
              value = app.finishArtifactFeedback(id, a.round, a.by, a.summary)
              break
            case 'stop':
              value = app.stopThread(a.id, { by: 'user', reason: '暂停' })
              break
            case 'diff':
              value = lineDiff(a.before, a.after, a.maxChars)
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
          artifacts: app.listArtifacts('prj_a').map((artifact) => ({
            view: app.getArtifact(artifact.id),
            versions: app.listArtifactVersions(artifact.id),
            comments: app.listArtifactComments(artifact.id),
            feedback: app.listArtifactFeedback(artifact.id),
          })),
          reads: ['crd_a', 'thr_a'].map((conversation) => ({
            transcript: app.listTranscript(conversation),
            notices: app.listNotices(conversation),
          })),
          files: app.store.db
            .query('SELECT id, project_id, mime, size, sha256 FROM files ORDER BY id')
            .all(),
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
