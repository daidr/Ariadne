import { Ariadne } from '@ariadne/core'
import {
  browserProfiles,
  createSandbox,
  detectSandbox,
  PluginHost,
  RepoWatcher,
  Scheduler,
  WorkspaceManager,
  WorktreeJanitor,
} from '@ariadne/agent'
import anthropic from '@ariadne/plugin-anthropic'
import github from '@ariadne/plugin-github'
import gitlab from '@ariadne/plugin-gitlab'
import local from '@ariadne/plugin-local'
import openAICompatible from '@ariadne/plugin-openai-compatible'
import ssh from '@ariadne/plugin-ssh'
import { homedir } from 'node:os'
import { join, resolve } from 'node:path'
import { createHandler } from './app'
import { loadConfig } from './config'
import { loadUserConfig, SettingsStore } from './settings'

const config = loadConfig()
const app = new Ariadne({ path: config.databasePath })

const configPath = join(config.home, 'config.json')

// Thread commands must not see the token or the settings, nor a browser that holds the token.
const userConfig = loadUserConfig(configPath)
// The plugin paths as the settings hold them now, once the settings are loaded.
let pluginPaths = () => userConfig.plugins
const sandboxSettings = userConfig.sandbox
const sandbox = createSandbox({
  kind: sandboxSettings.mode === 'off' ? null : await detectSandbox(),
  mode: sandboxSettings.mode,
  hide: [config.home, ...browserProfiles(), ...sandboxSettings.hide],
  // What the server runs later, outside the sandbox: its own code, the plugins it loads,
  // and the user's git config.
  readOnly: () => [
    resolve(import.meta.dir, '../../..'),
    ...pluginPaths(),
    join(homedir(), '.gitconfig'),
    join(process.env.XDG_CONFIG_HOME || join(homedir(), '.config'), 'git'),
  ],
})
if (!sandbox.kind) {
  console.warn(
    sandboxSettings.mode === 'off'
      ? '工作区沙箱已关闭：thread 的命令能读到 Ariadne 的令牌和设置'
      : process.platform === 'win32'
        ? 'Windows 上暂时没有工作区沙箱：thread 的命令能读到 Ariadne 的令牌和设置'
        : '这台机器上没有可用的工作区沙箱：thread 的命令能读到 Ariadne 的令牌和设置',
  )
}

const host = new PluginHost(join(config.home, 'plugins'), sandbox)
await host.load(openAICompatible)
await host.load(anthropic)
await host.load(local)
await host.load(github)
await host.load(gitlab)
await host.load(ssh)
const settings = new SettingsStore(configPath, host)
pluginPaths = () => settings.current.plugins
for (const path of settings.current.plugins) {
  await host.loadFromPath(path).catch((error) => console.error(`插件加载失败：${path}`, error))
}

const workspaces = new WorkspaceManager(app, host, (projectId) => settings.sourceSecrets(projectId))
// Pull requests are polled for their checks; ARIADNE_REPO_POLL_MS shortens it for testing.
const watcher = new RepoWatcher(app, workspaces, Number(process.env.ARIADNE_REPO_POLL_MS) || 60_000)
const scheduler = new Scheduler({
  app,
  workspaces,
  resolveModel: (conversation) => settings.resolveModel(conversation),
  locale: () => settings.current.locale,
  slots: settings.current.concurrency.slots,
  reservedSlots: settings.current.concurrency.reserved,
  dashboardSources: host.dashboards,
})
// Worktrees of work that landed go once the thread is finished with them.
const janitor = new WorktreeJanitor(app, workspaces)
scheduler.start()
watcher.start()
janitor.start()

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
    host,
    workspaces,
  }),
})

console.info(`Ariadne 已启动：http://${server.hostname}:${server.port}/#token=${config.token}`)

async function shutdown() {
  server.stop()
  // Running turns end as interrupted and resume on the next start.
  await watcher.stop()
  await janitor.stop()
  await scheduler.stop()
  await workspaces.dispose()
  await host.unloadAll()
  app.close()
  process.exit(0)
}
process.on('SIGINT', () => void shutdown())
process.on('SIGTERM', () => void shutdown())
