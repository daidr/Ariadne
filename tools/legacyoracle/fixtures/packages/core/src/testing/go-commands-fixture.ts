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
        try {
          switch (op) {
            case 'rename-project':
              value = app.renameProject(a.id, a.name)
              break
            case 'slots':
              value = app.setProjectSlots(a.id, a.slots)
              break
            case 'mcp':
              value = app.setProjectMcp(a.id, a.servers)
              break
            case 'settings':
              value = app.setProjectSettings(a.id, a.input, ['fetch', 'commands'])
              break
            case 'delivery':
              value = app.setProjectDelivery(a.id, a.input)
              break
            case 'routines':
              value = app.setRoutines(a.id, a.input, a.change)
              break
            case 'merge-method':
              app.rememberMergeMethod(a.id, a.method)
              break
            case 'pause':
              value = app.setAutopilotPaused(a.id, a.paused)
              break
            case 'source':
              value = app.setProjectSource(a.id, a.source)
              break
            case 'model':
              value = app.setThreadModel(a.id, a.model, a.by)
              break
            case 'start':
              value = app.startThread(a.id)
              break
            case 'rename-thread':
              value = app.renameThread(a.id, a.title, a.by)
              break
            case 'aside':
              value = app.setAside(a.id, a)
              break
            case 'end-aside':
              value = app.endAside(a.id, a.by)
              break
            case 'aside-due':
              app.asideDue(a.id)
              break
            case 'stop':
              value = app.stopThread(a.id, a)
              break
            case 'resume':
              value = app.resumeThread(a.id, a.by)
              break
            case 'complete':
              value = app.completeThread(a.id)
              break
            case 'workspace':
              app.recordWorkspace(a.id, a.input)
              break
            case 'removed':
              app.recordWorktreeRemoved(a.id, a.reason)
              break
            case 'merge':
              value = app.recordMerge(a.id, a.input)
              break
            case 'undo-merge':
              value = app.recordMergeUndone(a.id)
              break
            case 'conflict':
              value = app.recordMergeConflict(a.id, a.input)
              break
            case 'pull-request':
              app.syncPullRequest(a.id, a.input, a.auto)
              break
            case 'note-setup':
              value = app.noteSetup(a.id, a.command)
              break
            case 'note-check':
              value = app.noteCheck(a.id, a.command)
              break
            case 'can-note':
              value = { setup: app.canNoteSetup(a.id), check: app.canNoteCheck(a.id) }
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
