import { Ariadne, type AnyNewEvent } from '@ariadne/core'
import { createHandler } from '../app'
import { PluginHost, McpHub } from '@ariadne/agent'
import { BackgroundCommands } from '../../../agent/src/background'
import { WorkspaceManager } from '../../../agent/src/workspaces'
import { SettingsStore } from '../settings'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import openai from '@ariadne/plugin-openai-compatible'
import anthropic from '@ariadne/plugin-anthropic'
import agents from '@ariadne/plugin-agents'
import local from '@ariadne/plugin-local'
import { ModelCatalog } from '../prices'

const input = (await Bun.stdin.json()) as {
  settings?: boolean
  initialSettings?: Record<string, unknown>
  models?: boolean
  mcp?: boolean
  local?: boolean
  links?: boolean
  background?: boolean
  workspaces?: boolean
  dashboards?: boolean
  skillsDirs?: string[]
  catalog?: unknown
  find?: { env: Record<string, string>; home: string; available: string[] }
  seed: AnyNewEvent[]
  sql: string
  requests: {
    method: string
    path: string
    body?: unknown
    raw?: string
    auth?: boolean
    save?: string
    headers?: Record<string, string>
  }[]
}
Date.now = () => 1_800_000_000_000
const app = new Ariadne({ path: ':memory:', now: Date.now })
const directory = mkdtempSync(join(tmpdir(), 'ariadne-go-http-oracle-'))
const host = new PluginHost(directory)
if (input.dashboards)
  await host.load({
    id: 'go-dashboard-fixture',
    name: { 'zh-CN': '测试', en: 'Fixture' },
    setup(ctx) {
      ctx.registerDashboardSource({
        kind: 'fixture',
        title: { 'zh-CN': '测试', en: 'Fixture' },
        description: 'named rows',
        settings: {
          type: 'object',
          properties: { query: { type: 'string', title: { 'zh-CN': '查询' } } },
          required: ['query'],
        },
        async fetch(settings: Record<string, unknown>) {
          if (settings.query === 'fail') throw new Error('source failed')
          return { rows: [{ value: 5 }] }
        },
      })
    },
  })
if (input.models) {
  await host.load(openai)
  await host.load(anthropic)
  await host.load(agents)
}
if (input.local) await host.load(local)
if (input.workspaces)
  await host.load({
    id: 'go-workspaces-fixture',
    name: { 'zh-CN': '测试', en: 'Fixture' },
    setup(ctx) {
      ctx.registerContextProvider({
        kind: 'fixture',
        title: { 'zh-CN': '测试', en: 'Fixture' },
        provides: ['workspace'],
        settings: { type: 'object', properties: {} },
        async connect() {
          return {
            workspace: {
              isolated: true,
              async open() {
                throw new Error('fixture does not open files')
              },
              branches: {
                async changes() {
                  return {
                    branch: 'work/thread',
                    base: 'integration',
                    from: 'a'.repeat(40),
                    head: 'b'.repeat(40),
                    commits: [],
                    files: [],
                    uncommitted: [],
                  }
                },
                async file() {
                  return { before: 'before', after: 'after', skipped: null }
                },
                async merged() {
                  return false
                },
                async merge(request) {
                  return request.message === 'conflict'
                    ? { kind: 'conflict', files: ['file.txt'] }
                    : { kind: 'merged', sha: 'c'.repeat(40), head: 'b'.repeat(40) }
                },
                async undoMerge() {},
                async remove() {
                  return true
                },
                async worktrees() {
                  return []
                },
              },
            },
          }
        },
      })
    },
  })
if (input.links)
  await host.load({
    id: 'go-links-fixture',
    name: { 'zh-CN': '测试', en: 'Fixture' },
    setup(ctx) {
      ctx.registerLinkProvider({
        kind: 'fixture',
        title: { 'zh-CN': '测试', en: 'Fixture' },
        match(url) {
          return url.hostname === 'x.test' && url.pathname === '/pull/1'
            ? { kind: 'pull-request', label: 'acme/app#1' }
            : null
        },
        async preview() {
          return {
            kind: 'pull-request',
            repository: 'acme/app',
            number: 1,
            url: 'https://x.test/pull/1',
            title: '标题',
            state: 'open',
            draft: true,
            author: null,
            additions: 4,
            deletions: 1,
            changedFiles: 2,
            at: '2026-11-01',
          }
        },
      })
    },
  })
if (input.initialSettings)
  writeFileSync(join(directory, 'config.json'), JSON.stringify(input.initialSettings))
const settings = input.settings
  ? new SettingsStore(join(directory, 'config.json'), host)
  : undefined
if (settings && input.find) {
  const found = settings.foundSetups.bind(settings)
  const { env, home, available } = input.find
  settings.foundSetups = () =>
    found({ env, home, which: (program) => (available.includes(program) ? program : null) })
}
const prices =
  input.catalog === undefined
    ? undefined
    : new ModelCatalog((async () => Response.json(input.catalog)) as unknown as typeof fetch)
const mcp =
  input.mcp && settings
    ? new McpHub({ servers: () => settings.current.mcp.servers, env: () => ({}) })
    : undefined
const background = input.background ? new BackgroundCommands() : undefined
const workspaces = input.workspaces ? new WorkspaceManager(app, host) : undefined
if (background)
  background.start(
    'thread',
    'serve',
    (options) => {
      options.onOutput?.('Local: http://localhost:5173/\nprogress 10%\rready\n', 'stdout')
      return new Promise((resolve) => {
        options.signal?.addEventListener(
          'abort',
          () => resolve({ exitCode: 130, stdout: '', stderr: '' }),
          { once: true },
        )
      })
    },
    { local: true },
  )
const handler = createHandler({
  app,
  token: 'test-token',
  settings,
  host,
  skillsDirs: input.skillsDirs,
  prices,
  mcp,
  ...(workspaces ? { workspaces } : {}),
  ...(background ? { workspaces: { background } as WorkspaceManager } : {}),
})
const aliases = new Map<string, string>()
try {
  app.store.append(input.seed)
  app.store.db.exec(input.sql)
  const results = []
  for (const item of input.requests) {
    const path = item.path.replace(/@([a-z]+)@/g, (_, key: string) => aliases.get(key) ?? key)
    const headers = {
      ...(item.auth === false ? {} : { authorization: 'Bearer test-token' }),
      ...item.headers,
    }
    const response = await handler(
      new Request(`http://test.invalid${path}`, {
        method: item.method,
        headers,
        ...(item.raw !== undefined
          ? { body: item.raw }
          : item.body !== undefined
            ? { body: JSON.stringify(item.body) }
            : {}),
      }),
    )
    const text = await response.text()
    let body: any = text
    try {
      body = JSON.parse(text)
    } catch {
      /* File bodies and 204 have no JSON. */
    }
    if (item.save && body.artifact) aliases.set(item.save, body.artifact.id)
    if (body.sync) {
      aliases.set('cursor', body.sync.cursor)
      body.sync.cursor = body.sync.cursor.replace(/^[^.]+/, 'epoch')
    }
    results.push({ status: response.status, body })
  }
  console.info(JSON.stringify(results))
} finally {
  await workspaces?.dispose()
  await background?.stopAll()
  await mcp?.dispose()
  app.close()
  await host.unloadAll()
  rmSync(directory, { recursive: true, force: true, maxRetries: 10, retryDelay: 50 })
}
