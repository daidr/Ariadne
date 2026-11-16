import { Ariadne } from '@ariadne/core'
import { PluginHost } from '@ariadne/agent'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { expect, test } from 'vitest'
import { createHandler } from './app'
import { SettingsStore } from './settings'

test('the HTTP connection check uses an unsaved draft without persisting or returning its key', async () => {
  const home = await mkdtemp(join(tmpdir(), 'ariadne-search-check-'))
  let received: unknown
  const mock = Bun.serve({
    hostname: '127.0.0.1',
    port: 0,
    async fetch(request) {
      received = {
        path: new URL(request.url).pathname,
        key: request.headers.get('authorization'),
        body: await request.json(),
      }
      return Response.json({ results: [] })
    },
  })
  const app = new Ariadne({ path: ':memory:' })
  try {
    const path = join(home, 'config.json')
    const store = new SettingsStore(path, new PluginHost(join(home, 'plugins')))
    store.updateSearch({ apiKey: 'old-tavily' })
    const before = await readFile(path, 'utf8')
    const handle = createHandler({ app, settings: store, token: 'test' })
    const response = await handle(
      new Request('http://localhost/api/settings/search/check', {
        method: 'POST',
        headers: { authorization: 'Bearer test', 'content-type': 'application/json' },
        body: JSON.stringify({
          provider: 'deepseek',
          services: {
            deepseek: { apiKey: 'draft-secret', baseUrl: `http://127.0.0.1:${mock.port}/tools` },
          },
        }),
      }),
    )
    expect(await response.json()).toEqual({ ok: true, status: null })
    expect(received).toEqual({
      path: '/tools/search',
      key: 'Bearer draft-secret',
      body: { query: 'Ariadne', count: 1, need_content: false },
    })
    expect(await readFile(path, 'utf8')).toBe(before)
    expect(JSON.stringify(store.publicSearch())).not.toContain('old-tavily')
    expect(store.current.search.provider).toBe('tavily')
  } finally {
    mock.stop(true)
    app.close()
    await rm(home, { recursive: true, force: true })
  }
})
