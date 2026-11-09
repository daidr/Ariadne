import { Ariadne, DomainError, type WhiteboardContent, type WhiteboardNode } from '@ariadne/core'
import type { Workspace } from '@ariadne/plugin-sdk'
import * as decoding from 'lib0/decoding'
import * as encoding from 'lib0/encoding'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import * as syncProtocol from 'y-protocols/sync'
import * as Y from 'yjs'
import { ArtifactFeedback, boardChanges, whereOn } from './artifact-feedback'
import { artifactTools } from './artifact-tools'
import { SharedArtifacts } from './shared-artifacts'
import { ToolInputError, type ToolContext, type ToolResult } from './tool-helpers'
import { imageSize } from './whiteboard-options'

/** A PNG header of the given size: enough for its type and size to be read. */
function png(width: number, height: number): Uint8Array {
  const bytes = new Uint8Array(33)
  bytes.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13, 0x49, 0x48, 0x44, 0x52])
  const view = new DataView(bytes.buffer)
  view.setUint32(16, width)
  view.setUint32(20, height)
  return bytes
}

let app: Ariadne
let shared: SharedArtifacts
let ctx: ToolContext
let files: Record<string, Uint8Array>

beforeEach(() => {
  app = new Ariadne({ path: ':memory:' })
  shared = new SharedArtifacts(app, { versionDelayMs: 10_000, stateDelayMs: 10_000 })
  const project = app.createProject('P')
  const thread = app.createThread(project.id, { title: '方案' })
  files = { 'shots/a.png': png(1200, 800), 'shots/b.png': png(800, 1200) }
  ctx = {
    app,
    projectId: project.id,
    conversationId: thread.id,
    role: 'thread',
    locale: 'zh-CN',
    signal: new AbortController().signal,
    shared,
    workspace: async () =>
      ({
        async readBytes(path: string) {
          const file = files[path]
          if (!file) throw new Error(`no ${path}`)
          return file
        },
      }) as unknown as Workspace,
    repo: () => Promise.reject(new Error('no repo')),
  }
})

afterEach(() => {
  shared.dispose()
  app.close()
})

async function run(name: string, args: Record<string, unknown>): Promise<ToolResult> {
  const tool = artifactTools.find((t) => t.spec.name === name)
  if (!tool) throw new Error(`no tool ${name}`)
  try {
    return await tool.run(args, ctx)
  } catch (error) {
    if (error instanceof ToolInputError || error instanceof DomainError) {
      return { content: error.message, isError: true }
    }
    throw error
  }
}

async function ok(name: string, args: Record<string, unknown>) {
  const result = await run(name, args)
  if (result.isError) throw new Error(result.content)
  return JSON.parse(result.content) as Record<string, unknown>
}

const board = (id: string) => JSON.parse(shared.content(id)) as WhiteboardContent
const node = (b: WhiteboardContent, id: string) => b.nodes.find((n) => n.id === id)!

/** A page joined to an artifact's room: its own Yjs document, synced as y-websocket does. */
function page(artifactId: string) {
  const doc = new Y.Doc()
  let link: { message(data: Uint8Array): void; close(): void } | null = null
  const socket = {
    send(data: Uint8Array) {
      const decoder = decoding.createDecoder(data)
      if (decoding.readVarUint(decoder) !== 0) return
      const encoder = encoding.createEncoder()
      encoding.writeVarUint(encoder, 0)
      syncProtocol.readSyncMessage(decoder, encoder, doc, 'server')
      if (encoding.length(encoder) > 1)
        queueMicrotask(() => link?.message(encoding.toUint8Array(encoder)))
    },
    close() {},
  }
  doc.on('update', (update: Uint8Array, origin: unknown) => {
    if (origin === 'server') return
    const encoder = encoding.createEncoder()
    encoding.writeVarUint(encoder, 0)
    syncProtocol.writeUpdate(encoder, update)
    link!.message(encoding.toUint8Array(encoder))
  })
  link = shared.join(artifactId, socket)
  // what the page has goes to the server too, as y-websocket's first step does
  const encoder = encoding.createEncoder()
  encoding.writeVarUint(encoder, 0)
  syncProtocol.writeSyncStep1(encoder, doc)
  link.message(encoding.toUint8Array(encoder))
  return {
    doc,
    nodes: doc.getMap('nodes') as Y.Map<Y.Map<unknown>>,
    leave: () => link!.close(),
  }
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 0))

describe('option boards', () => {
  test('reads the size of PNG, GIF, JPEG and WebP images', () => {
    expect(imageSize(png(640, 480))).toEqual({ width: 640, height: 480 })
    const gif = new Uint8Array([0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x20, 0x00, 0x10, 0x00])
    expect(imageSize(gif)).toEqual({ width: 32, height: 16 })
    // SOI, an APP0 segment, then a start of frame with height 200 and width 300
    const jpeg = new Uint8Array([
      0xff, 0xd8, 0xff, 0xe0, 0x00, 0x04, 0x00, 0x00, 0xff, 0xc0, 0x00, 0x11, 0x08, 0x00, 0xc8,
      0x01, 0x2c, 0x03,
    ])
    expect(imageSize(jpeg)).toEqual({ width: 300, height: 200 })
    expect(imageSize(new Uint8Array([1, 2, 3]))).toBeNull()
  })

  test('options are framed side by side with their screenshots, scaled to the frame', async () => {
    const { artifactId } = await ok('create_whiteboard', {
      title: '登录页',
      options: [
        { id: 'a', title: '卡片', note: '居中', images: [{ path: 'shots/a.png', alt: '卡片式' }] },
        { id: 'b', title: '分栏', images: [{ path: 'shots/b.png' }], text: '左边插画，右边表单' },
      ],
    })
    const b = board(String(artifactId))
    const [a, second] = [node(b, 'a'), node(b, 'b')]
    expect(a).toMatchObject({ type: 'option', data: { title: '卡片', note: '居中' } })
    expect(second.x).toBeGreaterThan(a.x + a.width!)
    expect(second.y).toBe(a.y)
    const shot = node(b, 'a-image-1')
    expect(shot).toMatchObject({ type: 'image', parent: 'a', data: { alt: '卡片式' } })
    // 1200×800 into a frame 328 wide
    expect(shot.width).toBe(328)
    expect(shot.height).toBe(219)
    expect(String(shot.data!.src)).toMatch(/^\/api\/files\/fil_/)
    expect(node(b, 'b-text').data!.text).toBe('左边插画，右边表单')
    // the frame holds what is in it
    const text = node(b, 'b-text')
    expect(text.y + text.height!).toBeLessThanOrEqual(second.y + second.height!)
    // nothing on the board looks wrong to the checks: frames hold their contents
    const read = await ok('read_artifact', { artifactId })
    expect(read.problems).toBeUndefined()
    expect(read.items).toContainEqual(
      expect.objectContaining({ id: 'a', kind: 'option', title: '卡片' }),
    )
    expect(read.items).toContainEqual(expect.objectContaining({ id: 'a-image-1', in: 'a' }))
  })

  test('a new version of an option goes under the one it revises, joined by a dashed line', async () => {
    const { artifactId } = await ok('create_whiteboard', {
      title: '登录页',
      options: [{ id: 'a', title: '卡片', images: [{ path: 'shots/a.png' }] }],
    })
    await ok('update_whiteboard', {
      artifactId,
      reason: '按批注改了卡片',
      options: [{ id: 'a2', title: '卡片（改）', revises: 'a', images: [{ path: 'shots/b.png' }] }],
    })
    const b = board(String(artifactId))
    const [a, a2] = [node(b, 'a'), node(b, 'a2')]
    expect(a2.x).toBe(a.x)
    expect(a2.y).toBeGreaterThan(a.y + a.height!)
    expect(b.edges).toContainEqual(
      expect.objectContaining({
        source: { node: 'a', port: 'bottom' },
        data: expect.objectContaining({ dash: 'dashed' }),
      }),
    )
    expect(
      (
        await run('update_whiteboard', {
          artifactId,
          reason: 'x',
          options: [{ id: 'c', title: 'c', revises: 'zz' }],
        })
      ).isError,
    ).toBe(true)
  })

  test('the AI writes into the room while the user edits it: both changes stay', async () => {
    const { artifactId } = await ok('create_whiteboard', {
      title: '登录页',
      options: [
        { id: 'a', title: '卡片', images: [{ path: 'shots/a.png' }] },
        { id: 'b', title: '分栏', images: [{ path: 'shots/b.png' }] },
      ],
    })
    const id = String(artifactId)
    const user = page(id)
    await settle()
    expect(user.nodes.get('a')?.get('type')).toBe('option')
    // the user picks one and puts a note on its screenshot
    user.doc.transact(() => {
      const a = user.nodes.get('a')!
      a.set('data', { ...(a.get('data') as object), chosen: true })
      const sticky = new Y.Map<unknown>()
      user.nodes.set('n1', sticky)
      const shot = node(board(id), 'a-image-1')
      sticky.set('type', 'sticky')
      sticky.set('x', shot.x + shot.width! * 0.75 - 80)
      sticky.set('y', shot.y + shot.height! * 0.25 - 45)
      sticky.set('width', 160)
      sticky.set('height', 90)
      sticky.set('data', { text: '按钮太小' })
    })
    // meanwhile the AI renames the other option
    await ok('update_whiteboard', {
      artifactId,
      reason: '改名',
      options: [{ id: 'b', title: '左右分栏', images: [{ path: 'shots/b.png' }] }],
    })
    await settle()
    const after = board(id)
    expect(node(after, 'a').data!.chosen).toBe(true)
    expect(node(after, 'n1').data!.text).toBe('按钮太小')
    expect(node(after, 'b').data!.title).toBe('左右分栏')
    // the page sees the AI's change
    expect((user.nodes.get('b')!.get('data') as { title: string }).title).toBe('左右分栏')
    // the user's part was saved as their own version before the AI's
    const versions = app.listArtifactVersions(id)
    expect(versions.map((v) => v.author)).toEqual([ctx.conversationId, 'user', ctx.conversationId])
    user.leave()
  })

  test("the user's picks, marks and comments go to the AI as one round; it finishes it", async () => {
    const { artifactId } = await ok('create_whiteboard', {
      title: '登录页',
      options: [
        { id: 'a', title: '卡片', images: [{ path: 'shots/a.png', alt: '卡片式' }] },
        { id: 'b', title: '分栏', images: [{ path: 'shots/b.png' }] },
      ],
    })
    const id = String(artifactId)
    const feedback = new ArtifactFeedback(app, shared, { fallbackMs: 60_000 })
    feedback.start()
    const user = page(id)
    await settle()
    const shot = node(board(id), 'a-image-1')
    user.doc.transact(() => {
      const a = user.nodes.get('a')!
      a.set('data', { ...(a.get('data') as object), chosen: true })
      const sticky = new Y.Map<unknown>()
      user.nodes.set('n1', sticky)
      sticky.set('type', 'sticky')
      sticky.set('x', shot.x + shot.width! / 2 - 80)
      sticky.set('y', shot.y + shot.height! / 2 - 45)
      sticky.set('width', 160)
      sticky.set('height', 90)
      sticky.set('parent', 'a')
      sticky.set('data', { text: '按钮太小' })
    })
    app.commentOnArtifact(id, 'user', {
      type: 'create',
      threadId: 'cth_1',
      commentId: 'cmt_1',
      quote: '',
      anchor: { node: 'b' },
      body: '这个太挤',
    })
    expect(feedback.pending(id)).toBe(true)
    const round = feedback.send(id)!
    expect(round).toMatchObject({ round: 1, notify: ctx.conversationId })
    expect(round.items).toEqual([
      { kind: 'choice', chosen: [{ id: 'a', title: '卡片' }] },
      {
        kind: 'mark',
        nodeId: 'n1',
        type: 'sticky note',
        text: '按钮太小',
        where: 'in option "卡片" (a), over the image "卡片式" (a-image-1) at 50% across, 50% down',
      },
      {
        kind: 'comment',
        threadId: 'cth_1',
        action: 'create',
        body: '这个太挤',
        where: 'on option "分栏" (b) as a whole',
      },
    ])
    expect(feedback.pending(id)).toBe(false)
    // the AI reads the board with the comment and the round to finish
    const read = await ok('read_artifact', { artifactId })
    expect(read.openFeedbackRounds).toEqual([1])
    expect(read.comments).toEqual([
      expect.objectContaining({
        commentThread: 'cth_1',
        on: 'b',
        where: 'on option "分栏" (b) as a whole',
      }),
    ])
    await ok('comment_artifact', { artifactId, commentThread: 'cth_1', body: '下一版放宽' })
    const own = await ok('comment_artifact', { artifactId, on: 'a', body: '按钮会放大' })
    expect(app.listArtifactComments(id).find((t) => t.id === own.commentThread)?.anchor).toEqual({
      node: 'a',
    })
    await ok('finish_feedback_round', { artifactId, round: 1, summary: '按钮放大了' })
    expect(app.listArtifactFeedback(id)[0]).toMatchObject({
      summary: '按钮放大了',
      doneBy: ctx.conversationId,
    })
    // nothing new: no round
    expect(feedback.send(id)).toBeNull()
    feedback.stop()
    user.leave()
  })

  test('a comment pinned to a spot of a screenshot shows there and says where it is', async () => {
    const { artifactId } = await ok('create_whiteboard', {
      title: '登录页',
      options: [{ id: 'a', title: '卡片', images: [{ path: 'shots/a.png', alt: '卡片式' }] }],
    })
    const id = String(artifactId)
    const { commentThread } = await ok('comment_artifact', {
      artifactId,
      on: 'a-image-1',
      at: { x: 0.8, y: 0.1 },
      body: '这里的按钮太小',
    })
    const shot = node(board(id), 'a-image-1')
    expect(shot.comments).toEqual([commentThread])
    expect(shot.commentPins).toEqual({ [String(commentThread)]: { x: 0.8, y: 0.1 } })
    const read = await ok('read_artifact', { artifactId })
    expect(read.comments).toEqual([
      expect.objectContaining({
        on: 'a-image-1',
        where:
          'pinned in option "卡片" (a), on the image "卡片式" (a-image-1) at 80% across, 10% down',
      }),
    ])
    const refused = await run('comment_artifact', {
      artifactId,
      on: 'a',
      at: { x: 'a' },
      body: 'x',
    })
    expect(refused.isError).toBe(true)
  })

  test('a document written from an older version keeps the edits made since', async () => {
    const { artifactId } = await ok('create_document', {
      title: '方案',
      content: '# 方案\n\n第一段\n\n第二段',
    })
    const id = String(artifactId)
    // the user changes the end, saved as version 2
    shared.write(id, 'user', '# 方案\n\n第一段\n\n第二段，用户改的', { reason: '' })
    // the AI changes the start from version 1
    await ok('update_document', {
      artifactId,
      baseVersion: 1,
      reason: '改开头',
      content: '# 新方案\n\n第一段\n\n第二段',
    })
    expect(app.getArtifact(id).content).toBe('# 新方案\n\n第一段\n\n第二段，用户改的\n')
    // the user types into the heading while the AI, from version 3, rewrites the end
    shared.write(id, 'user', '# 新方案（草稿）\n\n第一段\n\n第二段，用户改的', { reason: '' })
    await ok('update_document', {
      artifactId,
      baseVersion: 3,
      reason: '改结尾',
      content: '# 新方案\n\n第一段\n\n第二段，AI 补充',
    })
    expect(app.getArtifact(id).content).toBe('# 新方案（草稿）\n\n第一段\n\n第二段，AI 补充\n')
  })
})

describe('whiteboards in words', () => {
  const frame: WhiteboardNode = {
    id: 'o',
    type: 'option',
    x: 0,
    y: 0,
    width: 400,
    height: 400,
    data: { title: 'O' },
  }
  const image: WhiteboardNode = {
    id: 'i',
    type: 'image',
    x: 0,
    y: 0,
    width: 400,
    height: 200,
    parent: 'o',
    data: { src: '/x' },
  }

  test('says where a mark is: its option, and the spot on the screenshot', () => {
    const sticky: WhiteboardNode = {
      id: 's',
      type: 'sticky',
      x: 0,
      y: 0,
      width: 40,
      height: 40,
      data: { text: 'hi' },
    }
    const b = { version: 1 as const, nodes: [frame, image, sticky], edges: [] }
    expect(whereOn(b, sticky)).toBe('in option "O" (o), over the image (i) at 5% across, 10% down')
    const outside = { ...sticky, x: 900 }
    expect(whereOn({ ...b, nodes: [frame, image, outside] }, outside)).toBe(
      'on the board, in no option',
    )
  })

  test('lists what the user picked, added, removed and rewrote', () => {
    const before = { version: 1 as const, nodes: [frame, image], edges: [] }
    const after = {
      version: 1 as const,
      nodes: [{ ...frame, data: { title: 'O2', chosen: true } }],
      edges: [],
    }
    expect(boardChanges(before, after)).toEqual([
      { kind: 'choice', chosen: [{ id: 'o', title: 'O2' }] },
      { kind: 'board', changes: ['They renamed option o to "O2".', 'They removed the image i.'] },
    ])
  })
})
