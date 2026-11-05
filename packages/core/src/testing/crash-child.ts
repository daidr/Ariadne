/**
 * A process that dies in the middle of a write, for the crash tests: it opens the database
 * at argv[2], arranges to exit (as a kill would, without unwinding) once an event of type
 * argv[3] is about to be written, and runs the scenario named argv[4].
 */
import { Ariadne } from '../ariadne'

const [path, crashAt, scenario, ...rest] = process.argv.slice(2)
if (!path || !crashAt || !scenario)
  throw new Error('usage: crash-child <db> <event type> <scenario>')

const app = new Ariadne({ path })
const append = app.store.append.bind(app.store)
app.store.append = (events) => {
  if (events.some((event) => event.type === crashAt)) process.exit(137)
  return append(events)
}

if (scenario === 'finish-turn') {
  const [conversationId, turnId] = rest
  app.finishTurn(conversationId!, turnId!, 'completed')
} else if (scenario === 'create-thread') {
  const [projectId] = rest
  app.transaction(() => {
    const thread = app.createThread(projectId!, { title: '新讨论串' })
    app.postMessage(projectId!, {
      conversationId: thread.id,
      author: 'coordinator',
      kind: 'relay',
      content: '要做的事',
    })
  })
} else {
  throw new Error(`unknown scenario ${scenario}`)
}
// Reaching this line means the crash never came.
process.exit(0)
