import { Ariadne, DomainError, type DashboardContent, type WhiteboardContent } from '@ariadne/core'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { artifactTools } from './artifact-tools'
import { fetchDashboardData } from './dashboard-sources'
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

const box = (id: string, text: string) => ({ id, text })
type Rect = { x: number; y: number; width: number; height: number }
const hit = (a: Rect, b: Rect) =>
  a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height

describe('whiteboards', () => {
  test('items without places are laid out along their connectors, facing sides joined', async () => {
    const { artifactId } = await ok('create_whiteboard', {
      title: '架构',
      direction: 'right',
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
    expect(edges[1]!.data).toEqual({ dash: 'dashed', route: 'curve' })
  })

  test('a box grows taller once its text wraps', async () => {
    const { artifactId } = await ok('create_whiteboard', {
      title: '长文',
      items: [{ id: 'a', text: '一段很长的说明文字，'.repeat(8) }],
    })
    const [node] = board(String(artifactId)).nodes
    expect(node!.width).toBe(280)
    expect(node!.height).toBeGreaterThan(120)
  })

  test('a big board keeps items, labels and long connectors clear of each other', async () => {
    for (const direction of ['right', 'down'] as const) {
      const { artifactId } = await ok('create_whiteboard', {
        title: '架构',
        direction,
        items: [
          box('reader', '阅读站 / 浏览器（仓库外）\n服务端代理 /api/auth，转发登录和会话'),
          box('dashboard', 'dashboard\nVue 3 管理 SPA'),
          box('basic', 'basic\n组件 + auth store / client'),
          box('shared', 'shared\n设置 schema · 实时协议 · 常量'),
          box('api', '@story-verse/api\nEden treaty + core DTS'),
          box('dql', 'dql-schema\n过滤字段与运算符'),
          box('kit', 'dql-kit（邻仓 checkout）\ntsconfig paths / vite alias'),
          box('core', 'core 入口\nindex.ts → app.ts'),
          box('plugins', '全局插件\n错误 · LogId · IP · Origin · 安全头 · auth macro'),
          box('modules', 'modules 路由层\n28 个模块，/api 下 .use()'),
          box('service', 'service / job\n业务规则、定时任务'),
          box('infra', 'shared 基础设施\ndb · queue+leader · realtime · rate-limit'),
          { id: 'pg', kind: 'ellipse', text: 'Postgres\nDrizzle snake_case' },
          { id: 'redis', kind: 'ellipse', text: 'Redis\nsession/缓存/队列/事件' },
          box('external', '外部服务\nS3 · SMTP · Bark · LLM · Turnstile · …'),
          box('consola', 'consola\n内置 fork'),
        ],
        connectors: [
          { from: 'reader', to: 'api', label: 'REST / BFF 代理' },
          { from: 'dashboard', to: 'basic', label: '组件 / auth store' },
          { from: 'dashboard', to: 'api', label: 'getClient()' },
          { from: 'basic', to: 'api', label: 'AuthUser' },
          { from: 'shared', to: 'dashboard', label: '设置项与 topic' },
          { from: 'shared', to: 'core', label: '设置/协议类型' },
          { from: 'api', to: 'core', label: 'VerseBackend' },
          { from: 'dql', to: 'basic', label: '过滤 UI' },
          { from: 'dql', to: 'modules', label: 'AST → SQL' },
          { from: 'kit', to: 'dql', label: '源码解析' },
          { from: 'kit', to: 'api', label: 'normalFields' },
          { from: 'core', to: 'plugins', label: '先 .use()' },
          { from: 'core', to: 'modules', label: "group('/api')" },
          { from: 'core', to: 'consola', label: 'logger' },
          { from: 'modules', to: 'service', label: '调用' },
          { from: 'service', to: 'external', label: '存 · 发信 · 审核' },
          { from: 'service', to: 'infra', label: 'db / enqueue / redis' },
          { from: 'infra', to: 'pg', label: '读写' },
          { from: 'infra', to: 'redis', label: '读写 + Pub/Sub' },
        ],
      })
      const { nodes, edges } = board(String(artifactId))
      const at = new Map(nodes.map((n) => [n.id, n]))
      const rect = (n: (typeof nodes)[number]) => ({
        x: n.x,
        y: n.y,
        width: n.width!,
        height: n.height!,
      })
      // Items don't overlap.
      for (const a of nodes) {
        for (const b of nodes)
          if (a !== b) expect(hit(rect(a), rect(b)), `${a.id} ${b.id}`).toBe(false)
      }
      // Each label sits where the curve has its middle, clear of every item and other label.
      const anchor = (id: string, port: string) => {
        const n = rect(at.get(id)!)
        if (port.startsWith('right')) return { x: n.x + n.width, y: n.y + n.height / 2 }
        if (port.startsWith('left')) return { x: n.x, y: n.y + n.height / 2 }
        if (port.startsWith('bottom')) return { x: n.x + n.width / 2, y: n.y + n.height }
        return { x: n.x + n.width / 2, y: n.y }
      }
      const labels = edges.map((e) => {
        const a = anchor(e.source.node, e.source.port)
        const b = anchor(e.target.node, e.target.port)
        const points = (e.points as { x: number; y: number }[] | undefined) ?? []
        // As dou-ui puts it: between the middle two points, or halfway along.
        const m = (points.length - 1) / 2
        const mid = points.length
          ? {
              x: (points[Math.floor(m)]!.x + points[Math.ceil(m)]!.x) / 2,
              y: (points[Math.floor(m)]!.y + points[Math.ceil(m)]!.y) / 2,
            }
          : { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }
        const width = Math.min(200, [...e.label!].length * 13 + 14)
        return { id: e.id, x: mid.x - width / 2, y: mid.y - 10, width, height: 20 }
      })
      for (const label of labels) {
        for (const n of nodes) expect(hit(label, rect(n)), `${label.id} on ${n.id}`).toBe(false)
        for (const other of labels) {
          if (other !== label) expect(hit(label, other), `${label.id} on ${other.id}`).toBe(false)
        }
      }
      // Boxes grow to their text: wider for a long line, taller once it wraps.
      expect(at.get('reader')!.width).toBeGreaterThan(160)
      // Connectors spanning ranks run through the gaps between.
      expect(edges.some((e) => Array.isArray(e.points) && e.points.length > 0)).toBe(true)
    }
  })

  test("an update changes only what it names and keeps the user's drawings", async () => {
    const { artifactId } = await ok('create_whiteboard', {
      title: '草图',
      items: [
        // Places given for a new board are not used: it is laid out whole.
        { id: 'a', text: 'A', x: 900, y: 900 },
        { id: 'b', text: 'B', x: 400, y: 0 },
      ],
      connectors: [{ from: 'a', to: 'b' }],
    })
    const id = String(artifactId)
    const laid = Object.fromEntries(board(id).nodes.map((n) => [n.id, n]))
    expect(laid.a).toMatchObject({ x: 0, y: 0 })
    expect(laid.b!.y).toBeGreaterThan(laid.a!.height!)
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
    // The new item sits below what it is connected from, as the board runs top to bottom.
    expect(at.c).toMatchObject({ x: at.b!.x })
    expect(at.c!.y).toBeGreaterThan(at.b!.y + at.b!.height!)
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

describe('live dashboards', () => {
  test('a source fetches the data; refreshing stores a new version; null makes it a snapshot', async () => {
    let calls = 0
    ctx.dashboardSources = new Map([
      [
        'counter',
        {
          kind: 'counter',
          title: { 'zh-CN': '计数' },
          description: 'Counts calls; returns { totals: { n } }',
          settings: {
            type: 'object',
            properties: {
              step: { type: 'number', title: { 'zh-CN': '步长' } },
              token: { type: 'string', title: { 'zh-CN': '令牌' }, secret: true },
            },
          },
          fetch: async (settings: { step?: number }) => {
            calls += settings.step ?? 1
            return { totals: { n: calls } }
          },
        },
      ],
    ]) as ToolContext['dashboardSources']
    expect(JSON.parse((await run('list_dashboard_sources', {})).content)[0].kind).toBe('counter')
    expect(
      (await run('create_dashboard', { title: 'x', widgets: [], source: { kind: 'nope' } }))
        .isError,
    ).toBe(true)
    // Settings are checked against the source's schema; a secret cannot be stored in the log.
    for (const settings of [{ steps: 2 }, { step: '2' }, { token: 'x' }]) {
      const result = await run('create_dashboard', {
        title: 'x',
        widgets: [],
        source: { kind: 'counter', settings },
      })
      expect(result.isError).toBe(true)
    }
    expect(calls).toBe(0)
    const { artifactId } = await ok('create_dashboard', {
      title: '计数',
      widgets: [{ id: 'n', type: 'stat', props: { source: 'totals', field: 'n' } }],
      source: { kind: 'counter', settings: { step: 2 } },
    })
    const read = () => JSON.parse(app.getArtifact(artifactId as string).content) as DashboardContent
    expect(read().data).toEqual({ totals: { n: 2 } })
    expect(read().source).toEqual({ kind: 'counter', settings: { step: 2 } })

    const refreshed = await ok('refresh_dashboard', { artifactId })
    expect(refreshed.version).toBe(2)
    expect(read().data).toEqual({ totals: { n: 4 } })
    expect(app.listArtifactVersions(artifactId as string)[0]?.reason).toBe('刷新了数据')

    // Editing keeps it live; source: null turns it back into a snapshot.
    await ok('update_dashboard', { artifactId, baseVersion: 2, reason: '改标题', title: '计数器' })
    expect(read().source?.kind).toBe('counter')
    await ok('update_dashboard', { artifactId, baseVersion: 3, reason: '定格', source: null })
    expect(read().source).toBeUndefined()
    expect((await run('refresh_dashboard', { artifactId })).isError).toBe(true)
  })

  test('a source that hangs or returns junk is given up on', async () => {
    const hang = {
      kind: 'hang',
      title: { 'zh-CN': '挂起' },
      description: '',
      settings: { type: 'object' as const, properties: {} },
      fetch: () => new Promise<Record<string, unknown>>(() => {}),
    }
    await expect(
      fetchDashboardData(hang as never, {}, { projectId: 'p', timeoutMs: 20 }),
    ).rejects.toThrow(/in time/)
    const junk = {
      kind: 'junk',
      title: { 'zh-CN': '垃圾' },
      description: '',
      settings: { type: 'object' as const, properties: {} },
      fetch: async () => [1, 2] as never,
    }
    await expect(fetchDashboardData(junk as never, {}, { projectId: 'p' })).rejects.toThrow(
      /no data sets/,
    )
  })
})
