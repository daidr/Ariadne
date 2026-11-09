import { Ariadne } from '@ariadne/core'
import { createTranslator } from '@ariadne/i18n'
import {
  ArtifactFeedback,
  Autopilot,
  browserProfiles,
  createSandbox,
  detectSandbox,
  McpHub,
  PluginHost,
  RepoWatcher,
  Scheduler,
  SharedArtifacts,
  Timers,
  WorkspaceManager,
  WorktreeJanitor,
} from '@ariadne/agent'
import agents from '@ariadne/plugin-agents'
import anthropic from '@ariadne/plugin-anthropic'
import daemon from '@ariadne/plugin-daemon'
import github from '@ariadne/plugin-github'
import gitlab from '@ariadne/plugin-gitlab'
import local, { threadEnv } from '@ariadne/plugin-local'
import openAICompatible from '@ariadne/plugin-openai-compatible'
import { processes } from '@ariadne/plugin-sdk'
import ssh from '@ariadne/plugin-ssh'
import { homedir } from 'node:os'
import { join, resolve } from 'node:path'
import { createHandler, joinShared, type SharedSocketData } from './app'
import { DailyBackup, DataStore } from './data'
import { loadConfig } from './config'
import { loadUserConfig, SettingsStore } from './settings'

const config = loadConfig()
const app = new Ariadne({ path: config.databasePath })

const configPath = join(config.home, 'config.json')
// The user's own skills for every project: Ariadne's first, then the folder shared between tools.
const skillsDirs = [join(config.home, 'skills'), join(homedir(), '.agents', 'skills')]

// Thread commands must not see the token or the settings, nor a browser that holds the token.
const userConfig = loadUserConfig(configPath)
// What the server prints is in the language the user chose for Ariadne.
const t = createTranslator(userConfig.locale)
// The plugin paths as the settings hold them now, once the settings are loaded.
let pluginPaths = () => userConfig.plugins
const sandboxSettings = userConfig.sandbox
const sandbox = createSandbox({
  kind: sandboxSettings.mode === 'off' ? null : await detectSandbox(),
  mode: sandboxSettings.mode,
  hide: [config.home, ...browserProfiles(), ...sandboxSettings.hide],
  // What the server runs later, outside the sandbox: its own code, the plugins it loads,
  // and the user's git config.
  // Skills are read, and their scripts run, by every thread; none of them changes them.
  show: () => skillsDirs,
  readOnly: () => [
    resolve(import.meta.dir, '../../..'),
    ...pluginPaths(),
    join(homedir(), '.gitconfig'),
    join(process.env.XDG_CONFIG_HOME || join(homedir(), '.config'), 'git'),
  ],
})
// What the last server left running when it died (a dev server, a tool server) ends now,
// before anything new starts.
const leftovers = await processes.open(join(config.home, 'processes.json'))
if (leftovers.ownerAlive) console.warn(t('cli.server.otherServer'))
else if (leftovers.killed.length) {
  console.info(t('cli.server.leftovers', { count: leftovers.killed.length }))
}
if (!sandbox.kind) {
  console.warn(
    t(
      sandboxSettings.mode === 'off'
        ? 'cli.server.sandboxOff'
        : process.platform === 'win32'
          ? 'cli.server.sandboxWindows'
          : 'cli.server.noSandbox',
    ),
  )
}

const host = new PluginHost(join(config.home, 'plugins'), sandbox)
await host.load(openAICompatible)
await host.load(anthropic)
await host.load(agents)
await host.load(local)
await host.load(github)
await host.load(gitlab)
await host.load(ssh)
await host.load(daemon)
const settings = new SettingsStore(configPath, host)
pluginPaths = () => settings.current.plugins
for (const path of settings.current.plugins) {
  await host
    .loadFromPath(path)
    .catch((error) => console.error(t('cli.server.pluginFailed', { path }), error))
}

// External tool servers run like thread commands: in the sandbox, without the server's
// credentials, with only what the user gave them on top.
const mcp = new McpHub({
  servers: () => settings.current.mcp.servers,
  sandbox,
  env: () => threadEnv(),
})

const workspaces = new WorkspaceManager(
  app,
  host,
  (projectId) => settings.sourceSecrets(projectId),
  {
    env: (projectId) => settings.turnSettings(projectId).env,
    commandRules: (projectId) => settings.turnSettings(projectId).commandRules,
    setupTimeoutMs: () => settings.current.limits.commandTimeoutMax * 1000,
  },
)
// Pull requests are polled for their checks; ARIADNE_REPO_POLL_MS shortens it for testing.
const watcher = new RepoWatcher(app, workspaces, Number(process.env.ARIADNE_REPO_POLL_MS) || 60_000)
// Documents and whiteboards are edited together; what the user does in them reaches the
// agent that wrote them in rounds.
const shared = new SharedArtifacts(app)
const feedback = new ArtifactFeedback(app, shared)
const scheduler = new Scheduler({
  app,
  workspaces,
  resolveModel: (conversation) =>
    conversation.role === 'thread'
      ? settings.resolveModel('thread', app.getThread(conversation.id).model)
      : // What the user wrote in a thread, and nothing else waiting: a quick look will do.
        settings.resolveCoordinator({
          triage: app.pendingIsTriage(conversation.id),
          contextTokens: app.getConversation(conversation.id).contextTokens,
        }),
  models: () => settings.modelChoices(),
  settings: (projectId) => settings.turnSettings(projectId),
  researchModel: () => settings.resolveModel('research'),
  skillsDirs,
  locale: () => settings.current.locale,
  slots: settings.current.concurrency.slots,
  reservedSlots: settings.current.concurrency.reserved,
  dashboardSources: host.dashboards,
  searchProviders: host.searches,
  searchModels: () => settings.searchModels(),
  searchService: () => settings.searchService(),
  mcp,
  shared,
})
// Worktrees of work that landed go once the thread is finished with them.
const janitor = new WorktreeJanitor(app, workspaces)
// Waits and routines that come due wake their conversations, after a restart too.
const timers = new Timers(app)
// Projects that switched it on merge reviewed work and start the next nodes on their own.
const autopilot = new Autopilot(app, workspaces, {
  locale: () => settings.current.locale,
  slots: () => settings.current.concurrency.slots,
})
// The database is copied once a day, so a bad day costs at most that day.
const dataStore = new DataStore(app, config.home, config.databasePath)
const backups = new DailyBackup(dataStore, (error) =>
  console.error(t('cli.server.backupFailed'), error),
)
scheduler.start()
backups.start()
timers.start()
feedback.start()
watcher.start()
janitor.start()
autopilot.start()

const server = Bun.serve<SharedSocketData>({
  hostname: config.hostname,
  port: config.port,
  // Event streams stay open; Bun's default idle timeout would cut them.
  idleTimeout: 0,
  websocket: {
    open(ws) {
      ws.data.link = shared.join(ws.data.artifactId, {
        send: (data) => void ws.sendBinary(data),
        close: (code, reason) => ws.close(code, reason),
      })
    },
    message(ws, message) {
      if (typeof message !== 'string') ws.data.link?.message(new Uint8Array(message))
    },
    close(ws) {
      ws.data.link?.close()
    },
  },
  fetch(request, bun) {
    const joined = joinShared(request, bun, { app, token: config.token })
    return joined === null ? handle(request) : joined
  },
})
const handle = createHandler({
  app,
  token: config.token,
  webDist: config.webDist,
  scheduler,
  settings,
  host,
  workspaces,
  mcp,
  data: dataStore,
  skillsDirs,
  shared,
  feedback,
})

console.info(
  t('cli.server.started', {
    url: `http://${server.hostname}:${server.port}/#token=${config.token}`,
  }),
)

async function shutdown() {
  server.stop()
  // Running turns end as interrupted and resume on the next start.
  await watcher.stop()
  await janitor.stop()
  await autopilot.stop()
  timers.stop()
  backups.stop()
  feedback.stop()
  await scheduler.stop()
  shared.dispose()
  await mcp.dispose()
  await workspaces.dispose()
  await host.unloadAll()
  app.close()
  process.exit(0)
}
process.on('SIGINT', () => void shutdown())
process.on('SIGTERM', () => void shutdown())
