import { Ariadne } from '../ariadne'
import { DomainError } from '../errors'

const commands = (await Bun.stdin.json()) as { op: string; args: any }[]
const app = new Ariadne({ path: ':memory:', now: () => 1_800_000_000_000 })
let project = ''
let thread = ''
const refs = new Map<string, string>()
try {
  console.info(
    JSON.stringify(
      commands.map(({ op, args: a }) => {
        let value: any = null
        let error: unknown = null
        const reference = refs.get(a.ref) ?? a.ref
        try {
          switch (op) {
            case 'create':
              value = app.createProject(a.name, a.start)
              project = value.id
              break
            case 'thread':
              value = app.createThread(project, a)
              thread = value.id
              break
            case 'project-ref':
              value = app.addProjectReference(project, a.source, a.where)
              break
            case 'thread-ref':
              value = app.addThreadReference(thread, a.source, a.where, a.attentionId)
              break
            case 'keep':
              value = app.keepThreadReference(thread, reference)
              break
            case 'remove-thread':
              value = app.removeThreadReference(thread, reference)
              break
            case 'remove-project':
              value = app.removeProjectReference(project, reference)
              break
            default:
              throw new Error(`unknown command ${op}`)
          }
          if (a.tag) refs.set(a.tag, value.id)
        } catch (caught) {
          error = caught instanceof DomainError ? caught.toLocalized() : { message: String(caught) }
          value = null
        }
        const projects = app.listProjects()
        return {
          value,
          error,
          details: projects.map((p) => app.getProjectDetail(p.id)),
          reads: projects
            .flatMap((p) => [p.coordinatorId, ...app.listThreads(p.id).map((t) => t.id)])
            .map((id) => app.listTranscript(id)),
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
