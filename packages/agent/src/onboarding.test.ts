import { Ariadne } from '@ariadne/core'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { coordinatorPrompt } from './prompts'
import type { ResolvedModel } from './runner'
import { Scheduler } from './scheduler'
import { answered, call, fakeModel, say } from './test-model'
import type { EverywhereMemory } from './tools'

let app: Ariadne
let scheduler: Scheduler | null = null
let lines: string[]

beforeEach(() => {
  app = new Ariadne({ path: ':memory:' })
  lines = []
})

afterEach(async () => {
  await scheduler?.stop()
  scheduler = null
  app.close()
})

const everywhere: EverywhereMemory = {
  add(line) {
    if (lines.includes(line)) return false
    lines.push(line)
    return true
  },
  remove(line) {
    if (!lines.includes(line)) return false
    lines = lines.filter((existing) => existing !== line)
    return true
  },
}

function run(model: ResolvedModel, options: { everywhere?: EverywhereMemory } = {}) {
  scheduler = new Scheduler({
    app,
    resolveModel: () => model,
    locale: () => 'zh-CN',
    retryBaseMs: 1,
    ...options,
  })
  scheduler.start()
  return scheduler.idle()
}

const toolNames = (request: { tools: { name: string }[] }) => request.tools.map((t) => t.name)

describe('a project started from one message', () => {
  test('the coordinator names it, and is not asked to again', async () => {
    const model = fakeModel((request) =>
      answered(request)
        ? say('好。')
        : [
            call('n1', 'name_project', { name: ' 周报\n生成器 ' }),
            { type: 'finish', reason: 'tool-calls' },
          ],
    )
    const project = app.createProject(null, { message: '做一个每周自动汇总提交记录的周报工具' })
    expect(project.unnamed).toBe(true)
    await run(model)

    expect(app.getProject(project.id)).toMatchObject({ name: '周报 生成器', unnamed: false })
    const [first] = model.requests
    expect(toolNames(first!)).toContain('name_project')
    expect(first!.messages[0]?.content).toContain('name_project')

    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '继续',
    })
    await scheduler!.idle()
    const later = model.requests.at(-1)!
    expect(toolNames(later)).not.toContain('name_project')
    expect(later.messages[0]?.content).toContain('周报 生成器')
  })

  test('a project the user named has no naming tool', async () => {
    const model = fakeModel(() => say('好。'))
    const project = app.createProject('我起的名字', { message: '开始' })
    await run(model)
    expect(toolNames(model.requests[0]!)).not.toContain('name_project')
    expect(app.getProject(project.id).name).toBe('我起的名字')
    expect(
      coordinatorPrompt({ projectName: 'P', locale: 'zh-CN', threads: [] } as never),
    ).not.toContain('name_project')
  })
})

describe('memory for every project', () => {
  test('goes to the user’s instructions with a notice, and is forgotten by its text', async () => {
    const results: string[] = []
    let turn = 0
    const model = fakeModel((request) => {
      const last = request.messages.at(-1)
      if (last?.role === 'tool') {
        results.push(last.content)
        return say('好。')
      }
      turn++
      return [
        turn === 1
          ? call('m1', 'remember', { text: '回复都用中文', scope: 'everywhere' })
          : call('m2', 'forget', { text: '回复都用中文' }),
        { type: 'finish', reason: 'tool-calls' },
      ]
    })
    const project = app.createProject('P')
    const say1 = (content: string) =>
      app.postMessage(project.id, {
        conversationId: project.coordinatorId,
        author: 'user',
        content,
      })
    say1('以后回复都用中文')
    await run(model, { everywhere })

    expect(lines).toEqual(['回复都用中文'])
    // Not in this project's own memory.
    expect(app.listMemories(project.id)).toEqual([])
    expect(app.listNotices(project.coordinatorId)).toMatchObject([
      { kind: 'memory-everywhere', action: 'saved', text: '回复都用中文', undone: false },
    ])

    say1('算了，不用了')
    await scheduler!.idle()
    expect(lines).toEqual([])
    expect(app.listNotices(project.coordinatorId).at(-1)).toMatchObject({
      kind: 'memory-everywhere',
      action: 'removed',
    })
    expect(results).toHaveLength(2)
  })

  test('without a place for it the tool says so', async () => {
    const results: { content: string; isError?: boolean }[] = []
    const model = fakeModel((request) => {
      const last = request.messages.at(-1)
      if (last?.role === 'tool') {
        results.push(last)
        return say('好。')
      }
      return [
        call('m1', 'remember', { text: 'x', scope: 'everywhere' }),
        { type: 'finish', reason: 'tool-calls' },
      ]
    })
    const project = app.createProject('P', { message: '记住 x' })
    await run(model)
    expect(results[0]?.isError).toBe(true)
    expect(app.listNotices(project.coordinatorId)).toEqual([])
  })
})
