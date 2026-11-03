import { Ariadne } from '@ariadne/core'
import { createHandler } from './app'
import { loadConfig } from './config'

const config = loadConfig()
const app = new Ariadne({ path: config.databasePath })
const server = Bun.serve({
  hostname: config.hostname,
  port: config.port,
  // Event streams stay open; Bun's default idle timeout would cut them.
  idleTimeout: 0,
  fetch: createHandler({ app, token: config.token, webDist: config.webDist }),
})

console.info(`Ariadne 已启动：http://${server.hostname}:${server.port}/#token=${config.token}`)

function shutdown() {
  server.stop()
  app.close()
  process.exit(0)
}
process.on('SIGINT', shutdown)
process.on('SIGTERM', shutdown)
