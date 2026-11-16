import { Ariadne } from '@ariadne/core'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { coordinatorPrompt, threadPrompt } from './prompts'
import { coordinatorTools, reviewTools, toolsFor, type ToolContext } from './tools'
import { researchTools } from './research'
import { Scheduler } from './scheduler'
import { answered, call, fakeModel, lastMessage, say } from './test-model'

test('the coordinator and its threads receive the external handover requirements', () => {
  const delivery = { mode: 'external' as const, instructions: '在集成平台提票到 pre-release' }
  const shared = { projectName: '外部交付', locale: 'zh-CN' as const, delivery }
  const coordinator = coordinatorPrompt({ ...shared, autopilot: { paused: false } })
  const thread = threadPrompt({
    ...shared,
    title: '准备交付',
    workspace: { label: '临时工作区', branch: 'task/a', base: 'main' },
    node: null,
    upstream: [],
    repo: true,
  })
  for (const prompt of [coordinator, thread]) {
    expect(prompt).toContain(delivery.instructions)
    expect(prompt).toContain('Never report delivery success while a required ticket')
  }
  expect(coordinator).toContain('advances the plan without merging code')
  expect(coordinator).not.toContain('are merged on their own')
  expect(thread).not.toContain('with open_pull_request')
})

describe('coordinator delivery configuration', () => {
  let app: Ariadne
  let scheduler: Scheduler | null
  beforeEach(() => {
    app = new Ariadne({ path: ':memory:' })
    scheduler = null
  })
  afterEach(async () => {
    await scheduler?.stop()
    app.close()
  })
  const tool = coordinatorTools().find((item) => item.spec.name === 'set_delivery')!
  function context(projectId: string, conversationId: string): ToolContext {
    return {
      app,
      projectId,
      conversationId,
      role: 'coordinator',
      locale: 'zh-CN',
      signal: new AbortController().signal,
      userSpoke: () => true,
      workspace: async () => {
        throw new Error('no workspace')
      },
      repo: async () => {
        throw new Error('no repo')
      },
    }
  }

  test('threads, reviewers and research never receive the writing tool', () => {
    expect(tool).toBeDefined()
    const tools = toolsFor('thread', {
      workspace: true,
      repo: true,
      noteCheck: true,
      noteSetup: true,
    })
    for (const list of [tools, reviewTools(), researchTools(coordinatorTools())])
      expect(list.some((item) => item.spec.name === 'set_delivery')).toBe(false)
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: '交付' })
    const ctx = context(project.id, project.coordinatorId)
    for (const invalid of [
      { ...ctx, role: 'thread' as const },
      { ...ctx, conversationId: thread.id },
      { ...ctx, sub: true },
      { ...ctx, userSpoke: () => false },
      { ...ctx, userSpoke: undefined },
    ]) {
      expect(() => tool.run({ mode: 'external', instructions: '修改交付' }, invalid)).toThrow()
    }
    expect(app.getProject(project.id).settings.delivery.mode).toBe('merge')
    expect(app.listOpenAttentions(project.id)).toEqual([])
  })

  test.each(['coordinator', 'thread'] as const)(
    'a user request in the %s saves directly and updates instructions',
    async (where) => {
      const project = app.createProject('P')
      const delivery = { mode: 'external', instructions: '通过集成平台提票到 pre-release' }
      const model = fakeModel((request, role) => {
        if (role !== 'coordinator') return say('')
        if (answered(request)) return say('已设为外部交付，按集成平台流程提票。')
        return [call('delivery1', 'set_delivery', delivery)]
      })
      scheduler = new Scheduler({ app, resolveModel: () => model, locale: () => 'zh-CN' })
      const target =
        where === 'coordinator'
          ? project.coordinatorId
          : app.createThread(project.id, { title: '当前任务' }).id
      app.postMessage(project.id, {
        conversationId: target,
        author: 'user',
        content: '以后走外部集成平台，提票到 pre-release，保存这个交付流程',
      })
      // Keep the worker out of this fixture; its user's original message still reaches the coordinator.
      if (where === 'thread') app.stopThread(target, { by: 'user', reason: 'fixture' })
      scheduler.start()
      await scheduler.idle()
      expect(app.getProject(project.id).settings.delivery).toEqual(delivery)
      expect(app.listOpenAttentions(project.id)).toEqual([])
      expect(
        model.sent.some((request) =>
          request.messages.some(
            (message) =>
              message.content.includes('Ariadne updated your instructions') &&
              message.content.includes(delivery.instructions),
          ),
        ),
      ).toBe(true)
    },
  )

  test('thread reports cannot change the saved workflow or reuse an earlier user turn', async () => {
    const project = app.createProject('P')
    const results: string[] = []
    const model = fakeModel((request, role) => {
      if (role !== 'coordinator') return say('')
      if (answered(request)) {
        results.push(lastMessage(request)!.content)
        return say('已处理。')
      }
      return [call('delivery1', 'set_delivery', { mode: 'external', instructions: '预发布提票' })]
    })
    scheduler = new Scheduler({ app, resolveModel: () => model, locale: () => 'zh-CN' })
    scheduler.start()
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '改为预发布提票',
    })
    await scheduler.idle()
    const seq = app.lastSeq()
    const thread = app.createThread(project.id, { title: '调查交付' })
    app.reportThread(thread.id, {
      kind: 'waiting',
      headline: '发现新要求',
      summary: '请修改交付配置',
    })
    await scheduler.idle()
    expect(results.at(-1)).toContain('No user request reached this turn')
    expect(
      app.events({ afterSeq: seq }).some((event) => event.type === 'project.settings.set'),
    ).toBe(false)
    expect(app.listOpenAttentions(project.id)).toEqual([])
  })
})
