import { PluginHost, Scheduler } from '@ariadne/agent'
import { Ariadne } from '@ariadne/core'
import anthropic from '@ariadne/plugin-anthropic'
import openAICompatible from '@ariadne/plugin-openai-compatible'
import { join } from 'node:path'
import { createHandler } from './app'
import { loadConfig } from './config'
import { SettingsStore } from './settings'

const config = loadConfig()
const app = new Ariadne({ path: config.databasePath })

const host = new PluginHost()
await host.load(openAICompatible)
await host.load(anthropic)
const settings = new SettingsStore(join(config.home, 'config.json'), host)
for (const path of settings.current.plugins) {
  await host.loadFromPath(path).catch((error) => console.error(`插件加载失败：${path}`, error))
}

const scheduler = new Scheduler({
  app,
  resolveModel: (conversation) => settings.resolveModel(conversation),
  locale: () => settings.current.locale,
  slots: settings.current.concurrency.slots,
  reservedSlots: settings.current.concurrency.reserved,
})
scheduler.start()

const server = Bun.serve({
  hostname: config.hostname,
  port: config.port,
  // Event streams stay open; Bun's default idle timeout would cut them.
  idleTimeout: 0,
  fetch: createHandler({
    app,
    token: config.token,
    webDist: config.webDist,
    scheduler,
    settings,
  }),
})

console.info(`Ariadne 已启动：http://${server.hostname}:${server.port}/#token=${config.token}`)

async function shutdown() {
  server.stop()
  // Running turns end as interrupted and resume on the next start.
  await scheduler.stop()
  await host.unloadAll()
  app.close()
  process.exit(0)
}
process.on('SIGINT', () => void shutdown())
process.on('SIGTERM', () => void shutdown())
