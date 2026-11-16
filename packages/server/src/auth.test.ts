import { Ariadne } from '@ariadne/core'
import type { Server } from 'bun'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { createHandler, joinShared, type SharedSocketData } from './app'
import { FILE_SESSION_MS, fileSessionCookie } from './file-auth'

let app: Ariadne
let handle: ReturnType<typeof createHandler>
const token = 'private-master-token'
const origin = 'http://127.0.0.1:7428'
let projectId: string
let file: string
beforeEach(() => {
  app = new Ariadne({ path: ':memory:' })
  handle = createHandler({ app, token })
  projectId = app.createProject('Private').id
  file = app.saveFile(
    projectId,
    { mime: 'text/plain', data: new TextEncoder().encode('private content') },
    { text: true },
  ).url
})
afterEach(() => {
  app.close()
  vi.restoreAllMocks()
})
const call = (path: string, headers: HeadersInit = {}, method = 'GET') =>
  handle(new Request(origin + path, { method, headers }))
const auth = { authorization: 'Bearer ' + token }
const cookieValue = (response: Response) => response.headers.get('set-cookie')!.split(';')[0]!

test('private files require credentials before the server even checks existence', async () => {
  const read = vi.spyOn(app, 'getFile')
  for (const path of [file, '/api/files/fil_missing', file + '?token=' + token]) {
    for (const headers of [{}, { authorization: 'Bearer wrong' }] as HeadersInit[]) {
      expect((await call(path, headers)).status).toBe(401)
    }
  }
  expect(read).not.toHaveBeenCalled()
  const response = await call(file, auth)
  expect(response.status).toBe(200)
  expect(await response.text()).toBe('private content')
  expect(response.headers.get('cache-control')).toBe('private, no-store')
  const head = await call(file, auth, 'HEAD')
  expect(head.status).toBe(200)
  expect(head.headers.get('content-length')).toBe(
    String(new TextEncoder().encode('private content').byteLength),
  )
  expect(await head.text()).toBe('')
  expect((await call('/api/files/fil_missing', auth)).status).toBe(404)
})

test('authenticated API requests issue a scoped HttpOnly session that serves native images without exposing the master token', async () => {
  const response = await call('/api/projects', auth)
  const session = response.headers.get('set-cookie')!
  expect(session).toContain('HttpOnly')
  expect(session).toContain('SameSite=Strict')
  expect(session).toContain('Path=/api/files/')
  expect(session).not.toContain(token)
  const cookie = cookieValue(response)
  expect((await call(file, { cookie, 'sec-fetch-site': 'same-origin' })).status).toBe(200)
  expect((await call(file, { cookie }, 'HEAD')).status).toBe(200)
  expect((await call(file, { cookie }, 'POST')).status).toBe(401)
  expect((await call(file, { cookie, authorization: 'Bearer wrong' })).status).toBe(401)
  expect((await call('/api/projects', { cookie })).status).toBe(401)
  expect((await call('/api/events', { cookie })).status).toBe(401)
})

test('file sessions reject tampering, expiry, token rotation, other origins and cross-site embedding', async () => {
  const cookie = cookieValue(await call('/api/projects', auth))
  expect(
    (await call(file, { cookie: cookie.slice(0, -1) + (cookie.endsWith('a') ? 'b' : 'a') })).status,
  ).toBe(401)
  expect((await call(file, { cookie: cookie + '; ' + cookie })).status).toBe(401)
  for (const site of ['cross-site', 'same-site'])
    expect((await call(file, { cookie, 'sec-fetch-site': site })).status).toBe(401)
  expect(
    (await handle(new Request('http://127.0.0.1:9999' + file, { headers: { cookie } }))).status,
  ).toBe(401)
  const changed = createHandler({ app, token: 'rotated-token' })
  expect((await changed(new Request(origin + file, { headers: { cookie } }))).status).toBe(401)
  const now = Date.now()
  vi.spyOn(Date, 'now').mockReturnValue(now + FILE_SESSION_MS + 1)
  expect((await call(file, { cookie })).status).toBe(401)
  const fresh = cookieValue(await call('/api/projects', auth))
  expect((await call(file, { cookie: fresh })).status).toBe(200)
  expect(fileSessionCookie(new URL('https://app.invalid'), token)).toContain('; Secure')
})

test('private read and write endpoints uniformly reject absent and invalid credentials', async () => {
  const paths = [
    '/api/projects',
    `/api/projects/${projectId}`,
    `/api/projects/${projectId}/export`,
    `/api/projects/${projectId}/files`,
    `/api/projects/${projectId}/artifacts`,
    `/api/projects/${projectId}/settings`,
    `/api/projects/${projectId}/messages`,
    '/api/conversations/crd_missing',
    '/api/threads/thr_missing/changes',
    '/api/settings/models',
    '/api/settings/mcp',
    '/api/settings/search',
    '/api/settings/input',
    '/api/settings/concurrency',
    '/api/links',
    '/api/links/preview?url=https://example.invalid',
    '/api/events',
    file,
    '/api/data',
    '/api/worktrees',
    '/api/artifacts/art_missing/live',
  ]
  for (const path of paths)
    for (const method of ['GET', 'HEAD', 'POST', 'PUT', 'DELETE', 'OPTIONS']) {
      for (const headers of [{}, { authorization: 'Bearer wrong' }] as HeadersInit[])
        expect((await call(path, headers, method)).status, method + ' ' + path).toBe(401)
    }
  expect((await call('/api/projects?token=' + token)).status).toBe(401)
  expect((await call('/api/health')).status).toBe(200)
  expect((await call('/api/health', {}, 'POST')).status).toBe(401)
  expect((await call('/api/events?token=' + token, { authorization: 'Bearer wrong' })).status).toBe(
    401,
  )
  const events = await call('/api/events?token=' + token)
  expect(events.status).toBe(200)
  await events.body!.cancel()
})

test('collaborative WebSocket upgrades require a bearer or explicit socket token, never a file session', async () => {
  const upgrade = vi.fn(() => true)
  const server = { upgrade } as unknown as Server<SharedSocketData>
  const url = origin + '/api/artifacts/art_private/live'
  const cookie = cookieValue(await call('/api/projects', auth))
  for (const headers of [{}, { cookie }, { authorization: 'Bearer wrong' }] as HeadersInit[]) {
    const result = joinShared(new Request(url, { headers }), server, { app, token })
    expect(result?.status).toBe(401)
  }
  expect(upgrade).not.toHaveBeenCalled()
  // A valid token reaches the artifact lookup (and gets 404 for this missing artifact).
  expect(joinShared(new Request(url + '?token=' + token), server, { app, token })?.status).toBe(404)
  expect(joinShared(new Request(url, { headers: auth }), server, { app, token })?.status).toBe(404)
  const artifact = app.createArtifact(projectId, {
    kind: 'document',
    title: 'Private doc',
    content: 'Private',
    author: 'user',
  })
  expect(
    joinShared(new Request(origin + `/api/artifacts/${artifact.id}/live?token=${token}`), server, {
      app,
      token,
    }),
  ).toBeUndefined()
  expect(upgrade).toHaveBeenCalledTimes(1)
})
