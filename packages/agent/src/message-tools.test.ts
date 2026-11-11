import { Ariadne } from '@ariadne/core'
import { LocalWorkspace } from '@ariadne/plugin-local'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { browserLocator, installedBrowser, testingPlatform } from './browser'
import { BrowserPage, BrowserPages } from './browser-page'
import { isLoopback, LoopbackProxy, route } from './browser-proxy'
import {
  reactTool,
  renameThreadTool,
  searchMessagesTool,
  setAsideTool,
  suggestConnectorTool,
} from './message-tools'
import { Timers } from './timers'
import type { Tool, ToolContext } from './tool-helpers'
import { ToolInputError } from './tool-helpers'
import { sendMessage } from './tools'
import { describeNote } from './transcript'

let app: Ariadne
let dir: string
let now = 1_000_000

beforeEach(async () => {
  now = Date.UTC(2026, 10, 10)
  app = new Ariadne({ path: ':memory:', now: () => now })
  dir = await mkdtemp(join(tmpdir(), 'ariadne-message-tools-'))
})

afterEach(async () => {
  app.close()
  await rm(dir, { recursive: true, force: true })
})

function setup() {
  const project = app.createProject('P')
  const thread = app.createThread(project.id, { title: '写后端' })
  return { project, thread }
}

function context(
  projectId: string,
  conversationId: string,
  role: 'coordinator' | 'thread',
): ToolContext {
  return {
    app,
    projectId,
    conversationId,
    role,
    locale: 'zh-CN',
    signal: new AbortController().signal,
    workspace: async () => new LocalWorkspace('w', dir, null),
    repo: () => Promise.reject(new Error('no repo')),
  }
}

// Async whatever the tool is, so a refusal is a rejection to expect.
const run = async (tool: Tool, args: Record<string, unknown>, ctx: ToolContext) =>
  tool.run(args, ctx)

const fakePage = () => ({ closed: false, close: async () => {} }) as unknown as BrowserPage
type Listener = (events: { type: string; data: Record<string, unknown> }[]) => void
const ignore: Listener = () => {}
const fakeResolve = async (name: string) =>
  ({ 'dev.test': ['127.0.0.1'], 'mixed.test': ['10.0.0.2', '::1'], 'far.test': ['10.0.0.3'] })[
    name
  ] ?? []

describe('reactions', () => {
  test('an agent reacts to a message of its own conversation, and takes it back', async () => {
    const { project, thread } = setup()
    const asked = app.postMessage(project.id, {
      conversationId: thread.id,
      author: 'user',
      content: '开始吧',
    })
    const ctx = context(project.id, thread.id, 'thread')
    await run(reactTool, { messageId: asked.id, emoji: '👀' }, ctx)
    await run(reactTool, { messageId: asked.id, emoji: '👍🏽' }, ctx)
    // Reacting again with the same one changes nothing.
    await run(reactTool, { messageId: asked.id, emoji: '👀' }, ctx)
    expect(app.getMessage(asked.id).reactions).toEqual([
      { emoji: '👀', by: 'thread' },
      { emoji: '👍🏽', by: 'thread' },
    ])
    await run(reactTool, { messageId: asked.id, emoji: '👀', remove: true }, ctx)
    expect(app.getMessage(asked.id).reactions).toEqual([{ emoji: '👍🏽', by: 'thread' }])
  })

  test('not text, not another conversation, not its own message', async () => {
    const { project, thread } = setup()
    const elsewhere = app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: 'hi',
    })
    const ctx = context(project.id, thread.id, 'thread')
    await expect(run(reactTool, { messageId: elsewhere.id, emoji: '👍' }, ctx)).rejects.toThrow(
      /No message/,
    )
    const own = app.postMessage(project.id, {
      conversationId: thread.id,
      author: 'thread',
      content: 'done',
    })
    await expect(run(reactTool, { messageId: own.id, emoji: '👍' }, ctx)).rejects.toThrow(
      /own messages/,
    )
    const asked = app.postMessage(project.id, {
      conversationId: thread.id,
      author: 'user',
      content: 'go',
    })
    for (const emoji of ['ok', '👍👍', '']) {
      await expect(run(reactTool, { messageId: asked.id, emoji }, ctx)).rejects.toThrow(
        ToolInputError,
      )
    }
    for (const emoji of ['👍', '🎉', '✅'])
      await run(reactTool, { messageId: asked.id, emoji }, ctx)
    await expect(run(reactTool, { messageId: asked.id, emoji: '🚀' }, ctx)).rejects.toThrow(
      /remove one first/,
    )
  })
})

describe('the user reacts', () => {
  test('to an agent message: the agent reads it next turn, without being woken', () => {
    const { project, thread } = setup()
    const answer = app.postMessage(project.id, {
      conversationId: thread.id,
      author: 'thread',
      content: '导出放在报表页右上角',
    })
    const before = app.listPendingConversations().map((c) => c.id)
    app.reactAsUser(answer.id, '👍')
    app.reactAsUser(answer.id, '👍')
    expect(app.getMessage(answer.id).reactions).toEqual([{ emoji: '👍', by: 'user' }])
    app.reactAsUser(answer.id, '👍', true)
    expect(app.getMessage(answer.id).reactions).toEqual([])
    expect(app.listPendingConversations().map((c) => c.id)).toEqual(before)
    const notes = app
      .listTranscript(thread.id)
      .flatMap((entry) => (entry.kind === 'note' ? [entry.note] : []))
    expect(notes).toEqual([
      {
        type: 'user-reacted',
        messageId: answer.id,
        emoji: '👍',
        quote: '导出放在报表页右上角',
        removed: false,
      },
      {
        type: 'user-reacted',
        messageId: answer.id,
        emoji: '👍',
        quote: '导出放在报表页右上角',
        removed: true,
      },
    ])
    expect(describeNote(notes[0]!, () => '')).toContain('<user_reacted message=')
    expect(describeNote(notes[1]!, () => '')).toContain('<user_reaction_removed message=')
  })

  test('alongside the agent, one emoji each; no text, at most three of their own', () => {
    const { project, thread } = setup()
    const asked = app.postMessage(project.id, {
      conversationId: thread.id,
      author: 'user',
      content: '开始吧',
    })
    app.react(asked.id, { author: 'thread', conversationId: thread.id }, '👀')
    app.reactAsUser(asked.id, '👀')
    expect(app.getMessage(asked.id).reactions).toEqual([
      { emoji: '👀', by: 'thread' },
      { emoji: '👀', by: 'user' },
    ])
    expect(() => app.reactAsUser(asked.id, 'ok')).toThrow()
    app.reactAsUser(asked.id, '👍')
    app.reactAsUser(asked.id, '🎉')
    expect(() => app.reactAsUser(asked.id, '🚀')).toThrow()
    expect(() => app.reactAsUser('msg_nope', '👍')).toThrow()
  })
})

describe('rename and set aside', () => {
  test('a thread renames itself; the coordinator renames any thread of its project', async () => {
    const { project, thread } = setup()
    await run(
      renameThreadTool('thread'),
      { title: '  修导出按钮 ' },
      context(project.id, thread.id, 'thread'),
    )
    expect(app.getThread(thread.id).title).toBe('修导出按钮')
    await run(
      renameThreadTool('coordinator'),
      { threadId: thread.id, title: '修 Safari 导出' },
      context(project.id, project.coordinatorId, 'coordinator'),
    )
    expect(app.getThread(thread.id).title).toBe('修 Safari 导出')
    expect(app.search(project.id, 'Safari').threads.map((hit) => hit.id)).toEqual([thread.id])
    const other = app.createProject('Q')
    await expect(
      run(
        renameThreadTool('coordinator'),
        { threadId: thread.id, title: 'x' },
        context(other.id, other.coordinatorId, 'coordinator'),
      ),
    ).rejects.toThrow()
  })

  test('set aside, it comes back under "needs me" on its day; writing to it takes it up', async () => {
    const { project, thread } = setup()
    const ctx = context(project.id, thread.id, 'thread')
    await run(setAsideTool('thread'), { days: 3, reason: '等设计稿' }, ctx)
    expect(app.getThread(thread.id).aside).toEqual({
      until: now + 3 * 86_400_000,
      reason: '等设计稿',
      by: thread.id,
      due: false,
    })
    expect(app.listNeedsMe(project.id)).toEqual([])
    const timers = new Timers(app, () => now)
    timers.start()
    try {
      now += 3 * 86_400_000
      timers.check()
      expect(app.getThread(thread.id).aside?.due).toBe(true)
      expect(app.listNeedsMe(project.id).map((item) => item.kind)).toEqual(['thread'])
      // The user writes to it: back to work, out of "needs me".
      app.postMessage(project.id, { conversationId: thread.id, author: 'user', content: '继续' })
      expect(app.getThread(thread.id).aside).toBeNull()
      expect(app.listNeedsMe(project.id)).toEqual([])
    } finally {
      timers.stop()
    }
    // The coordinator sets it aside, and takes it up again with 0 days.
    const coordinator = context(project.id, project.coordinatorId, 'coordinator')
    await run(setAsideTool('coordinator'), { threadId: thread.id, days: 1 }, coordinator)
    expect(app.getThread(thread.id).aside?.by).toBe(project.coordinatorId)
    await run(setAsideTool('coordinator'), { threadId: thread.id, days: 0 }, coordinator)
    expect(app.getThread(thread.id).aside).toBeNull()
    await expect(run(setAsideTool('thread'), { days: 31 }, ctx)).rejects.toThrow(/0 to 30/)
    // The user taking it up when it came due.
    await run(setAsideTool('thread'), { days: 1 }, ctx)
    expect(app.endAside(thread.id, 'user')).toBe(true)
    expect(app.endAside(thread.id, 'user')).toBe(false)
  })
})

describe('search and connectors', () => {
  test('finds what was said anywhere in the project, with who said it', async () => {
    const { project, thread } = setup()
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '导出按钮在 Safari 上没反应',
    })
    now += 1000
    app.postMessage(project.id, {
      conversationId: thread.id,
      author: 'thread',
      content: 'Safari 的导出修好了',
    })
    const found = JSON.parse(
      (await run(searchMessagesTool, { query: 'Safari' }, context(project.id, thread.id, 'thread')))
        .content,
    )
    expect(
      found.messages.map((m: { where: string; author: string }) => [m.where, m.author]),
    ).toEqual([
      ['here', 'thread'],
      ['coordinator', 'user'],
    ])
  })

  test('a suggested service is a card with no secret values', async () => {
    const { project, thread } = setup()
    const ctx = context(project.id, thread.id, 'thread')
    await run(
      suggestConnectorTool,
      {
        text: '接上 Linear 就能直接读任务',
        name: 'Linear',
        why: '读写 Linear 任务',
        transport: 'http',
        url: 'https://mcp.linear.app/mcp',
        secrets: ['Authorization'],
      },
      ctx,
    )
    const [message] = app.listMessages(thread.id)
    expect(message?.blocks).toEqual([
      {
        type: 'connector',
        name: 'Linear',
        why: '读写 Linear 任务',
        transport: 'http',
        command: null,
        args: [],
        url: 'https://mcp.linear.app/mcp',
        secrets: ['Authorization'],
      },
    ])
    await expect(
      run(
        suggestConnectorTool,
        { name: 'X', why: 'y', transport: 'stdio', command: 'npx', secrets: ['TOKEN=abc'] },
        ctx,
      ),
    ).rejects.toThrow(/secrets/)
  })
})

describe('send_message', () => {
  test('sends workspace files and earlier tool files, and quotes the message it answers', async () => {
    const { project, thread } = setup()
    const asked = app.postMessage(project.id, {
      conversationId: thread.id,
      author: 'user',
      content: '日志发我看看，\n  还有截图',
    })
    await writeFile(join(dir, 'run.log'), 'ok\n')
    const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 13, 10, 26, 10, 0])
    const shot = app.saveFile(project.id, { mime: 'image/png', data: png })
    const ctx = context(project.id, thread.id, 'thread')
    await run(
      sendMessage,
      { text: '在这里', files: ['./run.log', shot.id], replyTo: asked.id },
      ctx,
    )
    const sent = app.listMessages(thread.id).at(-1)!
    expect(sent.attachments.map((file) => [file.name, file.mime])).toEqual([
      ['run.log', 'text/plain'],
      ['image.png', 'image/png'],
    ])
    expect(sent.replyTo).toEqual({
      messageId: asked.id,
      author: 'user',
      text: '日志发我看看， 还有截图',
    })
    await expect(run(sendMessage, { text: 'x', replyTo: 'msg_nope' }, ctx)).rejects.toThrow(
      /No message msg_nope/,
    )
    await expect(run(sendMessage, { text: 'x', files: ['missing.txt'] }, ctx)).rejects.toThrow(
      /Could not read missing.txt/,
    )
  })

  test('a relay is quoted by the user words it passed along', async () => {
    const { project, thread } = setup()
    const said = app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '加个导出按钮',
    })
    const relay = app.postMessage(project.id, {
      conversationId: thread.id,
      author: 'coordinator',
      kind: 'relay',
      content: '请处理',
      cites: [said.id],
    })
    await run(
      sendMessage,
      { text: '好', replyTo: relay.id },
      context(project.id, thread.id, 'thread'),
    )
    expect(app.listMessages(thread.id).at(-1)?.replyTo).toEqual({
      messageId: relay.id,
      author: 'user',
      text: '加个导出按钮',
    })
  })
})

const has = (paths: string[]) => (path: string) => paths.includes(path)

describe('browser', () => {
  test('finds the machine browser where installers put it', () => {
    expect(
      installedBrowser(
        'win32',
        { PROGRAMFILES: 'C:\\Program Files', 'PROGRAMFILES(X86)': 'C:\\Program Files (x86)' },
        has([join('C:\\Program Files (x86)', 'Microsoft', 'Edge', 'Application', 'msedge.exe')]),
        () => null,
      ),
    ).toBe(join('C:\\Program Files (x86)', 'Microsoft', 'Edge', 'Application', 'msedge.exe'))
    expect(
      installedBrowser(
        'linux',
        {},
        () => false,
        (name) => (name === 'chromium' ? '/usr/bin/chromium' : null),
      ),
    ).toBe('/usr/bin/chromium')
    expect(
      installedBrowser(
        'linux',
        {},
        () => false,
        () => null,
      ),
    ).toBeNull()
    expect(testingPlatform('darwin', 'arm64')).toBe('mac-arm64')
    expect(testingPlatform('linux', 'arm64')).toBeNull()
  })

  test('the proxy sends localhost through the tunnel and the rest straight', async () => {
    expect(
      [
        'localhost',
        'LOCALHOST.',
        'app.localhost',
        '127.0.0.1',
        '127.1.2.3',
        '0.0.0.0',
        '[::1]',
        '::',
        '0:0:0:0:0:0:0:1',
        '::ffff:127.0.0.1',
        '::ffff:7f00:1',
        '[::ffff:0.0.0.0]',
      ].every(isLoopback),
    ).toBe(true)
    expect(
      ['example.com', '10.0.0.1', 'localhost.example', '::2', '::ffff:10.0.0.1', '1::1'].some(
        isLoopback,
      ),
    ).toBe(false)
    // A name is judged by what it resolves to, and then used as that address.
    expect(await route('dev.test.', fakeResolve)).toEqual({ tunnel: true })
    expect(await route('mixed.test', fakeResolve)).toEqual({ tunnel: true })
    expect(await route('far.test', fakeResolve)).toEqual({ tunnel: false, address: '10.0.0.3' })
    expect(await route('[2001:db8::1]', fakeResolve)).toEqual({
      tunnel: false,
      address: '2001:db8::1',
    })
    await expect(route('nowhere.test', fakeResolve)).rejects.toThrow('did not resolve')
    const server = Bun.listen({
      hostname: '127.0.0.1',
      port: 0,
      socket: {
        data: (socket, data) => void socket.write(`got:${new TextDecoder().decode(data)}`),
      },
    })
    const asked: number[] = []
    const proxy = LoopbackProxy.start(async (port) => {
      asked.push(port)
      const pipe = {
        onData: (_bytes: Uint8Array) => {},
        onClose: (_error?: string) => {},
        write: async (_bytes: Uint8Array) => false,
        close: () => {},
      }
      const socket = await Bun.connect({
        hostname: '127.0.0.1',
        port: server.port,
        socket: { data: (_s, data) => pipe.onData(data.slice()), close: () => pipe.onClose() },
      })
      pipe.write = async (bytes) => (socket.write(bytes), true)
      pipe.close = () => socket.end()
      return pipe
    })
    try {
      const received = Promise.withResolvers<Uint8Array>()
      let seen = new Uint8Array()
      const client = await Bun.connect({
        hostname: '127.0.0.1',
        port: proxy.port,
        socket: {
          data(_socket, data) {
            const joined = new Uint8Array(seen.length + data.length)
            joined.set(seen)
            joined.set(data, seen.length)
            seen = joined
            // Greeting answer (2) + connect answer (10) + the echo.
            if (seen.length > 12) received.resolve(seen)
          },
        },
      })
      const host = new TextEncoder().encode('localhost')
      client.write(new Uint8Array([5, 1, 0]))
      client.write(new Uint8Array([5, 1, 0, 3, host.length, ...host, 0x1f, 0x90]))
      client.write(new TextEncoder().encode('hi'))
      const bytes = await received.promise
      expect([...bytes.subarray(0, 4)]).toEqual([5, 0, 5, 0])
      expect(new TextDecoder().decode(bytes.subarray(12))).toBe('got:hi')
      expect(asked).toEqual([8080])
      client.end()
    } finally {
      proxy.stop()
      server.stop(true)
    }
  })

  test.skipIf(testingPlatform() !== 'linux64' || !Bun.which('zip'))(
    'one caller giving up does not call off the download another still waits for',
    async () => {
      const work = await mkdtemp(join(tmpdir(), 'ariadne-download-'))
      const folder = join(work, 'src', 'chrome-headless-shell-linux64')
      await Bun.write(join(folder, 'chrome-headless-shell'), '#!/bin/sh\n')
      const zipped = Bun.spawnSync(['zip', '-qr', join(work, 'shell.zip'), '.'], {
        cwd: join(work, 'src'),
        windowsHide: true,
      })
      expect(zipped.exitCode).toBe(0)
      let downloads = 0
      const server: Bun.Server<undefined> = Bun.serve({
        port: 0,
        async fetch(request): Promise<Response> {
          if (new URL(request.url).pathname === '/versions.json') {
            const url: string = `http://127.0.0.1:${server.port}/shell.zip`
            return Response.json({
              channels: {
                Stable: { downloads: { 'chrome-headless-shell': [{ platform: 'linux64', url }] } },
              },
            })
          }
          downloads++
          await Bun.sleep(200)
          return new Response(Bun.file(join(work, 'shell.zip')))
        },
      })
      try {
        const locator = browserLocator(
          join(work, 'browser'),
          `http://127.0.0.1:${server.port}/versions.json`,
        )
        const first = new AbortController()
        const a = locator.download(first.signal)
        const b = locator.download(new AbortController().signal)
        first.abort(new Error('stopped'))
        await expect(a).rejects.toThrow('stopped')
        expect(await b).toBe(
          join(work, 'browser', 'chrome-headless-shell-linux64', 'chrome-headless-shell'),
        )
        expect(downloads).toBe(1)
      } finally {
        server.stop(true)
        await rm(work, { recursive: true, force: true })
      }
    },
  )

  test('a failed open leaves no timer to close the page opened after it', async () => {
    let fail = true
    const closed: string[] = []
    const fake = { closed: false, close: async () => void closed.push('page') }
    const open = vi.spyOn(BrowserPage, 'open').mockImplementation(async () => {
      if (fail) throw new Error('no start')
      return fake as unknown as BrowserPage
    })
    const pages = new BrowserPages({ installed: () => '/bin/chrome', download: async () => '' }, 60)
    try {
      await expect(pages.page('thr_a')).rejects.toThrow('no start')
      expect(pages.has('thr_a')).toBe(false)
      await new Promise((done) => setTimeout(done, 30))
      fail = false
      expect(await pages.page('thr_a')).toBe(fake)
      // The first attempt's timer would have fired by now.
      await new Promise((done) => setTimeout(done, 40))
      expect(pages.has('thr_a')).toBe(true)
      expect(closed).toEqual([])
    } finally {
      open.mockRestore()
      await pages.closeAll()
    }
  })

  test('progress keeps the browser open; ending reports and stopping close it', async () => {
    const open = vi.spyOn(BrowserPage, 'open').mockImplementation(async () => fakePage())
    let listener: Listener = ignore
    const pages = new BrowserPages({ installed: () => '/bin/chrome', download: async () => '' })
    pages.follow({
      subscribe: (fn: typeof listener) => ((listener = fn), () => {}),
    } as never)
    try {
      await pages.page('thr_a')
      await pages.page('thr_b')
      listener([{ type: 'thread.reported', data: { threadId: 'thr_a', report: 'progress' } }])
      listener([{ type: 'thread.reported', data: { threadId: 'thr_a', report: 'waiting' } }])
      listener([{ type: 'thread.reported', data: { threadId: 'thr_b', report: 'done' } }])
      await Promise.resolve()
      expect([pages.has('thr_a'), pages.has('thr_b')]).toEqual([true, false])
      listener([{ type: 'thread.stopped', data: { threadId: 'thr_a' } }])
      expect(pages.has('thr_a')).toBe(false)
    } finally {
      open.mockRestore()
      await pages.closeAll()
    }
  })

  // Needs a Chrome, Edge or Chromium on the machine (or ARIADNE_TEST_BROWSER).
  const executable = process.env.ARIADNE_TEST_BROWSER ?? installedBrowser()
  test.skipIf(!executable)(
    'a page stays open between actions: read it, fill it in, click, look',
    async () => {
      const html = `<!doctype html><title>Form</title><h1>Sign up</h1><label for=n>Name</label><input id=n><select id=s><option>Red</option><option>Blue</option></select><button onclick="out.textContent='Hello '+n.value+' '+s.value; console.error('clicked')">Go</button><p id=out></p>`
      const server = Bun.serve({
        port: 0,
        fetch: () => new Response(html, { headers: { 'content-type': 'text/html' } }),
      })
      const pages = new BrowserPages({
        installed: () => executable!,
        download: () => Promise.reject(new Error('no download in tests')),
      })
      try {
        const page = await pages.page('thr_a')
        await page.goto(`http://127.0.0.1:${server.port}/`)
        expect(await page.snapshot()).toContain('[1] textbox "Name" value=""')
        await page.type(1, 'Ada', false)
        await page.select(2, 'Blue')
        await page.click(3)
        expect(await page.snapshot()).toContain('text "Hello Ada Blue"')
        expect(page.readConsole(false).lines.map((line) => line.text)).toContain('clicked')
        const shot = await page.screenshot({ fullPage: false, ref: null })
        expect([shot.mime, shot.width, shot.height]).toEqual(['image/png', 1280, 800])
        // The same page again, until it is closed.
        expect(await pages.page('thr_a')).toBe(page)
        expect(await pages.close('thr_a')).toBe(true)
        expect(pages.has('thr_a')).toBe(false)
      } finally {
        await pages.closeAll()
        server.stop(true)
      }
    },
    30_000,
  )
})
