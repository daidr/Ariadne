import { Ariadne, DomainError, type DashboardContent, type WhiteboardContent } from '@ariadne/core'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { artifactTools } from './artifact-tools'
import { ToolInputError, type ToolContext, type ToolResult } from './tool-helpers'

let app: Ariadne
let ctx: ToolContext

beforeEach(() => {
  app = new Ariadne({ path: ':memory:' })
  const project = app.createProject('P')
  const thread = app.createThread(project.id, { title: '画图' })
  ctx = {
    app,
    projectId: project.id,
    conversationId: thread.id,
    role: 'thread',
    locale: 'zh-CN',
    signal: new AbortController().signal,
    workspace: () => Promise.reject(new Error('no workspace')),
    repo: () => Promise.reject(new Error('no repo')),
  }
})

afterEach(() => app.close())

async function run(name: string, args: Record<string, unknown>): Promise<ToolResult> {
  const tool = artifactTools.find((t) => t.spec.name === name)
  if (!tool) throw new Error(`no tool ${name}`)
  // As the runner does: bad input goes back to the model as an error result.
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

const board = (id: string) => JSON.parse(app.getArtifact(id).content) as WhiteboardContent

describe('whiteboards', () => {
  test('items without places are laid out along their connectors, facing sides joined', async () => {
    const { artifactId } = await ok('create_whiteboard', {
      title: '架构',
      items: [
        { id: 'web', text: '浏览器' },
        { id: 'server', text: '本地服务', tone: 'primary' },
        { id: 'db', kind: 'ellipse', text: 'SQLite' },
        { id: 'note', kind: 'sticky', text: '只在本机' },
      ],
      connectors: [
        { from: 'web', to: 'server', label: 'HTTP' },
        { from: 'server', to: 'db', dash: 'dashed' },
      ],
    })
    const { nodes, edges } = board(String(artifactId))
    const at = Object.fromEntries(nodes.map((n) => [n.id, n]))
    // A chain runs left to right on one row.
    expect(at.server!.x).toBeGreaterThan(at.web!.x)
    expect(at.db!.x).toBeGreaterThan(at.server!.x)
    expect(at.server!.y).toBe(at.web!.y)
    // The unconnected note goes below, clear of the rest.
    expect(at.note!.y).toBeGreaterThan(at.web!.y + 90)
    expect(at.server).toMatchObject({
      type: 'shape',
      data: { shape: 'rect', text: '本地服务', tone: 'primary' },
    })
    expect(at.db).toMatchObject({ type: 'shape', data: { shape: 'ellipse' } })
    expect(at.note).toMatchObject({ type: 'sticky' })
    expect(edges[0]).toMatchObject({
      source: { node: 'web', port: 'right' },
      target: { node: 'server', port: 'left-in' },
      label: 'HTTP',
    })
    expect(edges[1]!.data).toEqual({ dash: 'dashed' })
  })

  test("an update changes only what it names and keeps the user's drawings", async () => {
    const { artifactId } = await ok('create_whiteboard', {
      title: '草图',
      items: [
        { id: 'a', text: 'A', x: 0, y: 0 },
        { id: 'b', text: 'B', x: 400, y: 0 },
      ],
      connectors: [{ from: 'a', to: 'b' }],
    })
    const id = String(artifactId)
    // The user draws on it and moves B.
    const drawn = board(id)
    drawn.nodes.push({
      id: 'ink1',
      type: 'ink',
      x: 50,
      y: 300,
      width: 100,
      height: 40,
      data: { points: [0, 0, 0.5, 10, 10, 0.5], box: { width: 10, height: 10 } },
    })
    drawn.nodes[1] = { ...drawn.nodes[1]!, x: 0, y: 400 }
    app.reviseArtifact(id, {
      content: JSON.stringify(drawn),
      author: 'user',
      reason: '画了一笔',
      baseVersion: 1,
    })

    const read = await ok('read_artifact', { artifactId: id })
    expect(read.version).toBe(2)
    expect(JSON.stringify(read)).not.toContain('points')
    expect(read.items).toContainEqual(expect.objectContaining({ id: 'ink1', kind: 'drawing' }))

    // A stale base version is refused.
    expect(
      (await run('update_whiteboard', { artifactId: id, baseVersion: 1, reason: 'x', items: [] }))
        .isError,
    ).toBe(true)
    await expect(
      ok('update_whiteboard', {
        artifactId: id,
        baseVersion: 1,
        reason: 'x',
        items: [{ id: 'a', text: 'A2' }],
      }),
    ).rejects.toThrow()

    await ok('update_whiteboard', {
      artifactId: id,
      baseVersion: 2,
      reason: '加一步',
      items: [
        { id: 'a', text: '开始' },
        { id: 'c', kind: 'diamond', text: '判断' },
      ],
      connectors: [{ from: 'b', to: 'c' }],
    })
    const after = board(id)
    const at = Object.fromEntries(after.nodes.map((n) => [n.id, n]))
    expect(at.ink1).toEqual(drawn.nodes.find((n) => n.id === 'ink1'))
    expect(at.a).toMatchObject({ x: 0, y: 0, data: { text: '开始' } })
    expect(at.b).toMatchObject({ x: 0, y: 400 })
    // The new item sits right of what it is connected from.
    expect(at.c!.x).toBeGreaterThan(at.b!.x)
    expect(after.edges).toHaveLength(2)

    await ok('update_whiteboard', {
      artifactId: id,
      baseVersion: 3,
      reason: '去掉',
      removeItems: ['b'],
    })
    // Removing an item removes its connectors.
    expect(board(id).edges).toEqual([])
    expect(
      (
        await run('update_whiteboard', {
          artifactId: id,
          baseVersion: 4,
          reason: 'x',
          items: [{ id: 'ink1', text: '字' }],
        })
      ).isError,
    ).toBe(true)
  })

  test('connectors must join items on the board', async () => {
    const result = await run('create_whiteboard', {
      title: 'x',
      items: [{ id: 'a' }],
      connectors: [{ from: 'a', to: 'nope' }],
    })
    expect(result).toMatchObject({ isError: true, content: expect.stringContaining('nope') })
  })
})

describe('dashboards', () => {
  test('a snapshot keeps its data, lays widgets out on the grid and is updated in place', async () => {
    const { artifactId } = await ok('create_dashboard', {
      title: '本周',
      widgets: [
        { id: 'prs', type: 'stat', title: '合并的 PR', props: { value: 6 } },
        { id: 'tests', type: 'stat', props: { source: 'summary', field: 'tests' } },
        {
          id: 'trend',
          type: 'chart',
          props: { source: 'daily', kind: 'line', x: 'day', y: ['tests'] },
        },
        { id: 'list', type: 'table', props: { source: 'daily' } },
      ],
      data: {
        summary: { tests: 120 },
        daily: [
          { day: '周一', tests: 99 },
          { day: '周二', tests: 120 },
        ],
      },
    })
    const id = String(artifactId)
    const content = JSON.parse(app.getArtifact(id).content) as DashboardContent
    expect(content.takenAt).toBeTypeOf('number')
    const place = Object.fromEntries(content.dashboard.widgets.map((w) => [w.id, w.place?.lg]))
    expect(place.prs).toEqual({ x: 0, y: 0, w: 3, h: 2 })
    expect(place.tests).toEqual({ x: 3, y: 0, w: 3, h: 2 })
    expect(place.trend).toEqual({ x: 6, y: 0, w: 6, h: 4 })
    expect(place.list).toEqual({ x: 0, y: 4, w: 6, h: 4 })

    // A widget reading data that is not there is refused.
    expect(
      (
        await run('update_dashboard', {
          artifactId: id,
          baseVersion: 1,
          reason: 'x',
          widgets: [{ id: 'bad', type: 'table', props: { source: 'none' } }],
        })
      ).isError,
    ).toBe(true)

    await ok('update_dashboard', {
      artifactId: id,
      baseVersion: 1,
      reason: '更新数据',
      widgets: [
        { id: 'prs', props: { value: 7 } },
        { id: 'memo', type: 'text', props: { text: '备注' } },
      ],
      removeWidgets: ['list'],
      data: { summary: { tests: 130 } },
    })
    const next = JSON.parse(app.getArtifact(id).content) as DashboardContent
    expect(next.data.summary).toEqual({ tests: 130 })
    expect(next.data.daily).toHaveLength(2)
    const widgets = Object.fromEntries(next.dashboard.widgets.map((w) => [w.id, w]))
    expect(widgets.prs).toMatchObject({
      title: '合并的 PR',
      props: { value: 7 },
      place: { lg: { x: 0, y: 0 } },
    })
    expect(widgets.list).toBeUndefined()
    expect(widgets.memo!.place?.lg).toEqual({ x: 0, y: 4, w: 6, h: 2 })
  })
})
