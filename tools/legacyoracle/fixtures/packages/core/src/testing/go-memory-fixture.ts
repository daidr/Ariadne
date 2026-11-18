import { Ariadne } from '../ariadne'
import { DomainError } from '../errors'
import type { AnyNewEvent } from '../events'

const input = (await Bun.stdin.json()) as {
  seed: AnyNewEvent[]
  commands: { op: string; args: any }[]
}
const app = new Ariadne({ path: ':memory:', now: () => 1_800_000_000_000 })
let lastMemory = 'mem_a'
try {
  app.store.append(input.seed)
  console.info(
    JSON.stringify(
      input.commands.map(({ op, args: a }) => {
        let value: unknown = null
        let error: unknown = null
        const memoryId = a.memoryId === 'last' ? lastMemory : a.memoryId
        const noticeId = a.noticeId === 'latest' ? app.listNotices('thr_a').at(-1)!.id : a.noticeId
        try {
          switch (op) {
            case 'save':
              value = app.saveMemory(a.projectId, {
                text: a.text,
                author: a.author,
                ...(memoryId === undefined ? {} : { memoryId }),
              })
              lastMemory = (value as { id: string }).id
              break
            case 'remove':
              app.removeMemory(a.projectId, memoryId, a.author)
              break
            case 'undo':
              app.undoMemoryNotice(a.projectId, noticeId)
              break
            case 'everywhere':
              app.noteEverywhere(a.projectId, {
                conversationId: a.id,
                action: a.action,
                text: a.text,
              })
              break
            case 'notice':
              value = app.everywhereNotice(a.projectId, noticeId)
              break
            case 'settle':
              app.settleNotice(a.projectId, noticeId)
              break
            case 'todo':
              value = app.updateTodo(a.projectId, a.id, a.items)
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
          memories: app.listMemories('prj_a'),
          notices: app.listNotices('thr_a'),
          todo: app.getTodo('thr_a'),
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
