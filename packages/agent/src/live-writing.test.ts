import { Ariadne, DomainError, type DashboardContent, type WhiteboardContent } from '@ariadne/core'
import type { ModelProvider, ModelStreamPart } from '@ariadne/plugin-sdk'
import * as decoding from 'lib0/decoding'
import * as encoding from 'lib0/encoding'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import * as awarenessProtocol from 'y-protocols/awareness'
import * as syncProtocol from 'y-protocols/sync'
import * as Y from 'yjs'
import { artifactTools } from './artifact-tools'
import { editsSoFar, rewriteSoFar } from './document-writing'
import { DEFAULT_LIMITS } from './limits'
import { liveCalls, type LiveCalls } from './live-calls'
import { requestStep } from './model-step'
import { readPartialJson } from './partial-json'
import { SharedArtifacts } from './shared-artifacts'
import { ToolInputError, type ToolContext, type ToolResult } from './tool-helpers'

let app: Ariadne
let shared: SharedArtifacts
let ctx: ToolContext
let live: LiveCalls

beforeEach(() => {
  app = new Ariadne({ path: ':memory:' })
  shared = new SharedArtifacts(app, { versionDelayMs: 10_000, stateDelayMs: 10_000 })
  const project = app.createProject('P')
  const thread = app.createThread(project.id, { title: '写方案' })
  ctx = {
    app,
    projectId: project.id,
    conversationId: thread.id,
    role: 'thread',
    locale: 'zh-CN',
    signal: new AbortController().signal,
    shared,
    workspace: () => Promise.reject(new Error('no workspace')),
    repo: () => Promise.reject(new Error('no repo')),
  }
  live = liveCalls(new Map(artifactTools.map((tool) => [tool.spec.name, tool])), ctx)
})

afterEach(() => {
  shared.dispose()
  app.close()
})

async function run(name: string, args: Record<string, unknown>): Promise<ToolResult> {
  const tool = artifactTools.find((t) => t.spec.name === name)!
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
  return JSON.parse(result.content) as { artifactId: string; version: number }
}

/** A page joined to an artifact's room, as y-websocket joins: its document and who it sees. */
function page(artifactId: string) {
  const doc = new Y.Doc()
  const awareness = new awarenessProtocol.Awareness(doc)
  awareness.setLocalState(null)
  let link: { message(data: Uint8Array): void; close(): void } | null = null
  const socket = {
    send(data: Uint8Array) {
      const decoder = decoding.createDecoder(data)
      const type = decoding.readVarUint(decoder)
      if (type === 1) {
        awarenessProtocol.applyAwarenessUpdate(
          awareness,
          decoding.readVarUint8Array(decoder),
          'server',
        )
        return
      }
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
  const encoder = encoding.createEncoder()
  encoding.writeVarUint(encoder, 0)
  syncProtocol.writeSyncStep1(encoder, doc)
  link.message(encoding.toUint8Array(encoder))
  return {
    doc,
    /** The agents the page sees at work. */
    writers: () => [...awareness.getStates().values()] as Record<string, never>[],
    leave: () => {
      link!.close()
      awareness.destroy()
    },
  }
}

/** A document as it reads now, without the line break it ends in. */
const reads = (artifactId: string) => shared.content(artifactId).trimEnd()
const stored = (artifactId: string) => app.getArtifact(artifactId).content.trimEnd()

const versions = (artifactId: string) =>
  app
    .listArtifactVersions(artifactId)
    .toSorted((a, b) => a.version - b.version)
    .map((version) => ({
      author: version.author,
      content: app.getArtifact(artifactId, version.version).content.trimEnd(),
    }))

/** The user on a page types at the end of the document's title. */
function typeInTitle(doc: Y.Doc, text: string) {
  const title = [...(doc.getMap('document').values() as Iterable<Y.Map<unknown>>)].find(
    (block) => block.get('type') === 'heading',
  )!
  const words = title.get('text') as Y.Text
  words.insert(words.length, text)
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 0))

/** The model writing a call: its arguments come in pieces, slower than they are shown. */
async function written(name: string, args: Record<string, unknown>, upTo: string) {
  const text = JSON.stringify(args)
  const end = text.indexOf(upTo)
  if (end < 0) throw new Error(`"${upTo}" is not in the arguments`)
  live.input(0, name, text.slice(0, end + upTo.length))
  await settle()
}

describe('arguments read while written', () => {
  test('a string stops where the text does, and says so', () => {
    expect(readPartialJson('{"artifactId":"art_1","content":"# 标题\\n\\n正在')).toEqual({
      value: { artifactId: 'art_1', content: '# 标题\n\n正在' },
      open: ['content'],
    })
  })

  test('a key without its value, a cut number and a cut escape are left out', () => {
    expect(readPartialJson('{"a":1,"b":')?.value).toEqual({ a: 1 })
    expect(readPartialJson('{"a":1,"b":12')?.value).toEqual({ a: 1 })
    expect(readPartialJson('{"a":1,"b":tr')?.value).toEqual({ a: 1 })
    expect(readPartialJson('{"a":"x\\')?.value).toEqual({ a: 'x' })
    expect(readPartialJson('{"a":"x\\u4e')?.value).toEqual({ a: 'x' })
    expect(readPartialJson('{"a":"x","b')?.value).toEqual({ a: 'x' })
  })

  test('the open path names the item of a list still being written', () => {
    const read = readPartialJson('{"items":[{"id":"a","text":"甲"},{"id":"b","te')
    expect(read?.value).toEqual({ items: [{ id: 'a', text: '甲' }, { id: 'b' }] })
    expect(read?.open).toEqual(['items', 1])
    expect(readPartialJson('{"items":[{"id":"a"}],"reason":"x"}')?.open).toBeNull()
  })

  test('what is not JSON reads as nothing', () => {
    expect(readPartialJson('')).toBeNull()
    expect(readPartialJson('{"a" 1')).toBeNull()
  })
})

describe('a document while its change is written', () => {
  const base = '# 方案\n\n第一段。\n\n<pending>减法清单</pending>\n\n最后一段。'

  test('a replacement shows once its old text is whole, with as much new text as there is', () => {
    const edits = [{ old: '<pending>减法清单</pending>', new: '## 减法清单\n\n去掉' }]
    expect(editsSoFar(base, edits, ['edits', 0, 'old'])).toBe(base)
    expect(editsSoFar(base, edits, ['edits', 0, 'new'])).toBe(
      '# 方案\n\n第一段。\n\n## 减法清单\n\n去掉\n\n最后一段。',
    )
  })

  test('written again from the top, the rest stays below what is written so far', () => {
    expect(rewriteSoFar(base, '# 方案\n\n第一段。\n\n## 减法清单\n\n去掉')).toBe(
      '# 方案\n\n第一段。\n\n## 减法清单\n\n去掉\n\n<pending>减法清单</pending>\n\n最后一段。',
    )
    // Past the part it left out: that part is gone.
    expect(
      rewriteSoFar(base, '# 方案\n\n第一段。\n\n## 减法清单\n\n去掉这些。\n\n最后一段。\n'),
    ).toBe('# 方案\n\n第一段。\n\n## 减法清单\n\n去掉这些。\n\n最后一段。')
  })

  test('a line being copied as it was does not show twice', () => {
    expect(rewriteSoFar(base, '# 方案\n\n第一')).toBe(base)
  })

  test('a short line found further down does not swallow what lies between', () => {
    const ruled = '# 标题\n\n甲段落内容在这里。\n\n---\n\n乙段落内容在这里。'
    expect(rewriteSoFar(ruled, '# 标题\n\n---\n\n新')).toBe(
      '# 标题\n\n---\n\n新\n\n甲段落内容在这里。\n\n---\n\n乙段落内容在这里。',
    )
  })
})

describe('documents written where the user watches', () => {
  const outline =
    '# 方案\n\n第一段。\n\n<pending>减法清单</pending>\n\n<pending>要拍板的问题</pending>'
  let artifactId: string

  beforeEach(async () => {
    artifactId = (await ok('create_document', { title: '方案', content: outline })).artifactId
  })

  test('a placeholder is a block of its own and reads back as written', async () => {
    const viewer = page(artifactId)
    await settle()
    const blocks = [...viewer.doc.getMap('document').values()] as Y.Map<unknown>[]
    expect(blocks.filter((block) => block.get('type') === 'pending')).toHaveLength(2)
    expect(reads(artifactId)).toBe(outline)
    viewer.leave()
  })

  test("a replacement lands as it is written, under the thread's name, and is one version", async () => {
    const viewer = page(artifactId)
    await settle()
    const args = {
      artifactId,
      edits: [{ old: '<pending>减法清单</pending>', new: '## 减法清单\n\n去掉手动暂停。' }],
      reason: '写减法清单',
    }
    await written('update_document', args, '去掉')
    expect(reads(artifactId)).toContain('## 减法清单\n\n去掉\n\n<pending>要拍板的问题')
    const [writer] = viewer.writers()
    expect(writer?.user).toMatchObject({ name: '写方案' })
    // The shape the editor's own cursors have among themselves, which is not public yet:
    // when a dou-ui upgrade changes it this fails, instead of the cursor quietly going.
    // Remove with daidr/dou-ui#409.
    const blocks = viewer.doc.getMap('document') as Y.Map<Y.Map<unknown>>
    const cursor = writer!.cursor as unknown as Record<'anchor' | 'head', Record<string, unknown>>
    expect(Object.keys(cursor).toSorted()).toEqual(['anchor', 'head'])
    expect(Object.keys(cursor.anchor).toSorted()).toEqual(['block', 'position'])
    expect(cursor.head).toEqual(cursor.anchor)
    const block = blocks.get(cursor.anchor.block as string)!
    expect(Object.keys(block.toJSON()).toSorted()).toEqual([
      'order',
      'parent',
      'props',
      'text',
      'type',
    ])
    expect(block.get('type')).toBe('paragraph')
    const at = Y.createAbsolutePositionFromRelativePosition(
      Y.createRelativePositionFromJSON(cursor.anchor.position),
      viewer.doc,
    )!
    expect(at.type).toBe(block.get('text'))
    expect(at.type.toString()).toBe('去掉')
    expect(at.index).toBe(2)
    // Nothing is saved until the call runs.
    expect(app.getArtifact(artifactId).version).toBe(1)

    const saved = await ok('update_document', args)
    expect(saved.version).toBe(2)
    expect(stored(artifactId)).toBe(
      '# 方案\n\n第一段。\n\n## 减法清单\n\n去掉手动暂停。\n\n<pending>要拍板的问题</pending>',
    )
    expect(app.getArtifact(artifactId).updatedBy).toBe(ctx.conversationId)
    expect(shared.writing(artifactId, ctx.conversationId)).toBeNull()
    viewer.leave()
  })

  test('a call that never runs leaves nothing behind but what the user typed meanwhile', async () => {
    const viewer = page(artifactId)
    await settle()
    await written(
      'update_document',
      { artifactId, edits: [{ old: '第一段。', new: '改写的第一段，还没写完' }], reason: 'x' },
      '还没',
    )
    expect(reads(artifactId)).toContain('改写的第一段，还没')
    // The user adds to the title while the agent writes.
    typeInTitle(viewer.doc, '（草稿）')
    await settle()
    live.reset()
    await settle()
    expect(reads(artifactId)).toBe(outline.replace('# 方案', '# 方案（草稿）'))
    expect(viewer.writers()).toHaveLength(0)
    // Nothing was saved of the half the agent wrote; the user's words are a version of theirs.
    expect(versions(artifactId)).toHaveLength(1)
    shared.save(artifactId)
    expect(versions(artifactId).slice(1)).toEqual([
      { author: 'user', content: outline.replace('# 方案', '# 方案（草稿）') },
    ])
    viewer.leave()
  })

  test("what the user types while the agent writes is a version of theirs, without the agent's half", async () => {
    const viewer = page(artifactId)
    await settle()
    const args = {
      artifactId,
      edits: [{ old: '第一段。', new: '改写的第一段，还没写完，终于写完了。' }],
      reason: '改写',
    }
    await written('update_document', args, '还没')
    typeInTitle(viewer.doc, '（草稿）')
    await settle()
    expect(versions(artifactId)).toHaveLength(1)
    await written('update_document', args, '终于')
    await ok('update_document', args)
    const titled = outline.replace('# 方案', '# 方案（草稿）')
    expect(versions(artifactId).slice(1)).toEqual([
      { author: 'user', content: titled },
      {
        author: ctx.conversationId,
        content: titled.replace('第一段。', '改写的第一段，还没写完，终于写完了。'),
      },
    ])
    expect(reads(artifactId)).toBe(stored(artifactId))
    viewer.leave()
  })

  test('half a change is never the state a restart comes back to', async () => {
    const quick = new SharedArtifacts(app, { versionDelayMs: 10_000, stateDelayMs: 5 })
    const before = shared
    shared = quick
    ctx = { ...ctx, shared: quick }
    live = liveCalls(new Map(artifactTools.map((tool) => [tool.spec.name, tool])), ctx)
    try {
      const viewer = page(artifactId)
      await settle()
      await written(
        'update_document',
        { artifactId, edits: [{ old: '第一段。', new: '改写的第一段，还没写完' }], reason: 'x' },
        '还没',
      )
      await new Promise((resolve) => setTimeout(resolve, 30))
      expect(reads(artifactId)).toContain('改写的第一段，还没')
      const kept = new Y.Doc()
      Y.applyUpdate(kept, app.readSharedState(artifactId)!.state)
      expect(JSON.stringify(kept.getMap('document').toJSON())).not.toContain('改写')
      // Stopping in the middle takes the half back instead of saving it.
      quick.flush()
      expect(versions(artifactId)).toHaveLength(1)
      expect(reads(artifactId)).toBe(outline)
      viewer.leave()
    } finally {
      quick.dispose()
      shared = before
    }
  })

  test('in a table the cursor is in the cell being written', async () => {
    const viewer = page(artifactId)
    await settle()
    const args = {
      artifactId,
      edits: [
        {
          old: '<pending>减法清单</pending>',
          new: '| 项 | 理由 |\n| --- | --- |\n| 手动暂停 | 没人用 |\n| 排序菜单 | 只有一种排法 |',
        },
      ],
      reason: '写减法清单',
    }
    await written('update_document', args, '只有一种')
    const cursor = viewer.writers()[0]!.cursor as unknown as { anchor: Record<string, unknown> }
    // Remove with daidr/dou-ui#409, like the shape of the cursor above.
    expect(Object.keys(cursor.anchor).toSorted()).toEqual(['block', 'cell', 'position'])
    const table = (viewer.doc.getMap('document') as Y.Map<Y.Map<unknown>>).get(
      cursor.anchor.block as string,
    )!
    expect(table.get('type')).toBe('table')
    expect(Object.keys(table.toJSON())).toEqual(
      expect.arrayContaining(['cells', 'columns', 'rows', 'text']),
    )
    const cell = (table.get('cells') as Y.Map<Y.Text>).get(cursor.anchor.cell as string)!
    expect(cell.toString()).toBe('只有一种')
    const [row, column] = (cursor.anchor.cell as string).split(':')
    expect((table.get('rows') as Y.Map<Y.Map<unknown>>).get(row!)!.get('order')).toBeTypeOf(
      'string',
    )
    expect((table.get('columns') as Y.Map<unknown>).has(column!)).toBe(true)
    live.reset()
    viewer.leave()
  })

  test('a replacement that does not find its text changes nothing, shown or saved', async () => {
    const viewer = page(artifactId)
    await settle()
    const args = {
      artifactId,
      edits: [
        { old: '第一段。', new: '第一段，改过。' },
        { old: '没有这句', new: 'x' },
      ],
      reason: 'x',
    }
    await written('update_document', args, '没有这句')
    expect(reads(artifactId)).toContain('第一段，改过。')
    const result = await run('update_document', args)
    expect(result.isError).toBe(true)
    expect(result.content).toContain('edits[1]')
    expect(reads(artifactId)).toBe(outline)
    viewer.leave()
  })

  test('the whole text written again shows from the top down', async () => {
    const viewer = page(artifactId)
    await settle()
    const content =
      '# 方案\n\n第一段。\n\n## 减法清单\n\n去掉手动暂停。\n\n## 要拍板的问题\n\n没有。'
    const args = { artifactId, baseVersion: 1, content, reason: '写完' }
    await written('update_document', args, '去掉手动')
    expect(reads(artifactId)).toBe(
      '# 方案\n\n第一段。\n\n## 减法清单\n\n去掉手动\n\n<pending>减法清单</pending>\n\n<pending>要拍板的问题</pending>',
    )
    await ok('update_document', args)
    expect(stored(artifactId)).toBe(content)
    expect(app.getArtifact(artifactId).version).toBe(2)
    viewer.leave()
  })

  test('with nobody watching nothing is written until the call runs', async () => {
    const args = {
      artifactId,
      edits: [{ old: '第一段。', new: '第一段，改过。' }],
      reason: 'x',
    }
    await written('update_document', args, '改过')
    expect(shared.writing(artifactId, ctx.conversationId)).toBeNull()
    await ok('update_document', args)
    expect(stored(artifactId)).toContain('第一段，改过。')
  })
})

describe('whiteboards and dashboards', () => {
  test("items show one by one as written, outlined under the writer's name", async () => {
    const { artifactId } = await ok('create_whiteboard', {
      title: '架构',
      items: [
        { id: 'web', text: '页面' },
        { id: 'api', kind: 'pending', text: '服务端：待定' },
      ],
      connectors: [{ from: 'web', to: 'api' }],
    })
    const before = JSON.parse(app.getArtifact(artifactId).content) as WhiteboardContent
    expect(before.nodes.find((n) => n.id === 'api')).toMatchObject({
      type: 'pending',
      data: { text: '服务端：待定' },
    })
    const viewer = page(artifactId)
    await settle()
    const args = {
      artifactId,
      reason: '补上服务端',
      items: [
        { id: 'api', kind: 'rect', text: '服务端' },
        { id: 'db', kind: 'ellipse', text: '数据库' },
      ],
      connectors: [{ from: 'api', to: 'db' }],
    }
    await written('update_whiteboard', args, '"服务端"},')
    const half = JSON.parse(shared.content(artifactId)) as WhiteboardContent
    expect(half.nodes.find((n) => n.id === 'api')).toMatchObject({ type: 'shape' })
    expect(half.nodes.find((n) => n.id === 'db')).toBeUndefined()
    expect(viewer.writers()[0]).toMatchObject({
      user: { name: '写方案' },
      flow: { selection: { nodes: ['api'] } },
    })
    await ok('update_whiteboard', args)
    const after = JSON.parse(app.getArtifact(artifactId).content) as WhiteboardContent
    expect(after.nodes.map((n) => n.id).toSorted()).toEqual(['api', 'db', 'web'])
    expect(after.edges).toHaveLength(2)
    expect(app.getArtifact(artifactId).version).toBe(2)
    viewer.leave()
  })

  test('a placeholder widget becomes the widget it stood for, in its place', async () => {
    const { artifactId } = await ok('create_dashboard', {
      title: '周报',
      widgets: [
        { id: 'total', type: 'stat', props: { value: 3 } },
        { id: 'trend', type: 'pending', title: '趋势', props: { text: '每天的合并数，等数据' } },
      ],
    })
    const before = JSON.parse(app.getArtifact(artifactId).content) as DashboardContent
    const place = before.dashboard.widgets[1]!.place
    await ok('update_dashboard', {
      artifactId,
      baseVersion: 1,
      reason: '补上趋势',
      data: { days: [{ day: '周一', merged: 2 }] },
      widgets: [
        {
          id: 'trend',
          type: 'chart',
          props: { source: 'days', kind: 'bar', x: 'day', y: ['merged'] },
        },
      ],
    })
    const after = JSON.parse(app.getArtifact(artifactId).content) as DashboardContent
    expect(after.dashboard.widgets[1]).toMatchObject({
      id: 'trend',
      type: 'chart',
      title: '趋势',
      place,
    })
  })
})

/** A model that answers each attempt with the parts scripted for it. */
const model = (parts: ModelStreamPart[][]) => {
  let attempt = 0
  const provider: ModelProvider<never> = {
    kind: 'fake',
    title: { 'zh-CN': '假模型' },
    settings: { type: 'object', properties: {} },
    async *stream() {
      for (const part of parts[attempt++] ?? []) yield part
    },
  }
  return { provider, settings: {}, model: 'fake-1', label: 'fake' }
}

describe('a model response', () => {
  test('hands the arguments on as they come, and takes them back when the attempt fails', async () => {
    const seen: string[] = []
    const calls: LiveCalls = {
      input: (call, name, text) => seen.push(`${call} ${name} ${text}`),
      reset: () => seen.push('reset'),
    }
    const answer = await requestStep(
      model([
        [
          { type: 'calling' },
          { type: 'tool-input', call: 0, name: 'update_document', arguments: '{"artifactId":' },
          { type: 'finish', reason: 'error', retryable: true, error: 'cut' },
        ],
        [
          { type: 'tool-input', call: 0, name: 'update_document', arguments: '{}' },
          { type: 'tool-call', id: 'c1', name: 'update_document', arguments: '{}' },
          { type: 'finish', reason: 'tool-calls' },
        ],
      ]) as never,
      { model: 'fake-1', messages: [], tools: [], signal: new AbortController().signal },
      () => {},
      0,
      DEFAULT_LIMITS,
      undefined,
      undefined,
      calls,
    )
    expect(answer?.toolCalls).toHaveLength(1)
    expect(seen).toEqual(['0 update_document {"artifactId":', 'reset', '0 update_document {}'])
  })
})
