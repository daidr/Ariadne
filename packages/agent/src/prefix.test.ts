import { Ariadne } from '@ariadne/core'
import type { ChatMessage } from '@ariadne/plugin-sdk'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { Scheduler } from './scheduler'
import { answered, call, fakeModel, lastUser, say, plain } from './test-model'

let app: Ariadne
let scheduler: Scheduler | null = null

beforeEach(() => {
  app = new Ariadne({ path: ':memory:' })
})

afterEach(async () => {
  await scheduler?.stop()
  scheduler = null
  app.close()
})

const same = (a: ChatMessage[], b: ChatMessage[]) => JSON.stringify(a) === JSON.stringify(b)
const isSummary = (messages: ChatMessage[]) =>
  messages[0]?.content.startsWith('You summarise') ?? false
const notes = (messages: ChatMessage[]) =>
  messages.find((m) => m.role === 'assistant' && m.content.startsWith('<your_notes>'))

// A provider caches a prompt up to a point and reads it back only while the next prompt
// starts with exactly the same bytes, so the history must only ever grow at its end.
describe('the prompt a conversation sends', () => {
  test('starts with the whole of the one before, across steps and turns', async () => {
    const model = fakeModel((request) => {
      if (answered(request)) return say('记下了')
      if (lastUser(request).includes('第 1 件')) {
        return [
          call('c1', 'update_todo', { items: [{ label: '整理', status: 'doing' }] }),
          { type: 'finish', reason: 'tool-calls' },
        ]
      }
      if (lastUser(request).includes('第 2 件')) {
        return [
          call('c2', 'update_todo', { items: [{ label: '整理', status: 'done' }] }),
          call('c3', 'list_threads', {}),
          { type: 'finish', reason: 'tool-calls' },
        ]
      }
      return say('好')
    })
    scheduler = new Scheduler({
      app,
      resolveModel: () => model,
      locale: () => 'zh-CN',
      retryBaseMs: 1,
    })
    scheduler.start()
    const project = app.createProject('P')
    for (const text of ['第 1 件', '第 2 件', '第 3 件']) {
      app.postMessage(project.id, {
        conversationId: project.coordinatorId,
        author: 'user',
        content: text,
      })
      await scheduler.idle()
    }

    const sent = model.requests.map((request) => request.messages)
    expect(sent.length).toBeGreaterThanOrEqual(5)
    for (const [index, messages] of sent.entries()) {
      const previous = sent[index - 1]
      if (!previous) continue
      expect(messages[0]).toEqual(previous[0])
      expect(messages.length).toBeGreaterThan(previous.length)
      expect(same(messages.slice(0, previous.length), previous)).toBe(true)
    }
  })

  test('after a summary too, while the todo list keeps changing', async () => {
    const model = fakeModel((request) => {
      if (request.messages[0]?.content.startsWith('You summarise')) return plain('- 几份需求')
      if (answered(request)) return say('记下了')
      const n = /第 (\d+) 份/.exec(lastUser(request))?.[1] ?? '?'
      return [
        call(`c${n}`, 'update_todo', { items: [{ label: `第 ${n} 份`, status: 'doing' }] }),
        { type: 'finish', reason: 'tool-calls' },
      ]
    })
    scheduler = new Scheduler({
      app,
      resolveModel: () => model,
      locale: () => 'zh-CN',
      retryBaseMs: 1,
      compactAt: 600,
    })
    scheduler.start()
    const project = app.createProject('P')
    for (const n of [1, 2, 3, 4, 5, 6]) {
      app.postMessage(project.id, {
        conversationId: project.coordinatorId,
        author: 'user',
        content: `第 ${n} 份需求：${'很长的描述。'.repeat(40)}`,
      })
      await scheduler.idle()
    }

    let previous: ChatMessage[] | null = null
    let compared = 0
    let summaries = 0
    for (const { messages } of model.requests) {
      // A new summary starts the conversation over; until the next, it only grows.
      if (isSummary(messages)) {
        previous = null
        summaries++
        continue
      }
      if (previous && notes(previous)) {
        expect(same(messages.slice(0, previous.length), previous)).toBe(true)
        compared++
      }
      previous = messages
    }
    expect(summaries).toBeGreaterThan(0)
    expect(compared).toBeGreaterThan(2)
    const steps = model.requests.map((r) => r.messages).filter((m) => !isSummary(m))
    // The notes keep the todo list as it stood when they were written.
    expect(notes(steps.at(-1)!)?.content).toContain('<state_at_summary>')
  })

  test('when its instructions change, which it reads at the end', async () => {
    const model = fakeModel((request) => {
      if (answered(request)) return say('记下了')
      if (lastUser(request).includes('以后')) {
        return [
          call('m1', 'remember', { text: '回答用中文' }),
          { type: 'finish', reason: 'tool-calls' },
        ]
      }
      return say('好')
    })
    scheduler = new Scheduler({
      app,
      resolveModel: () => model,
      locale: () => 'zh-CN',
      retryBaseMs: 1,
    })
    scheduler.start()
    const project = app.createProject('P')
    for (const text of ['你好', '以后都用中文回答', '再说一句']) {
      app.postMessage(project.id, {
        conversationId: project.coordinatorId,
        author: 'user',
        content: text,
      })
      await scheduler.idle()
    }
    expect(app.listMemories(project.id)).toHaveLength(1)

    const sent = model.requests
    for (const [index, request] of sent.entries()) {
      const previous = sent[index - 1]
      if (!previous) continue
      expect(JSON.stringify(request.tools)).toBe(JSON.stringify(previous.tools))
      expect(same(request.messages.slice(0, previous.messages.length), previous.messages)).toBe(
        true,
      )
    }
    // The memory went with the result of the call that saved it, once.
    const last = sent.at(-1)!.messages
    const told = last.filter((m) => /<project_memory_[0-9a-f]{12}>/.test(m.content))
    expect(told).toHaveLength(1)
    expect(told[0]).toMatchObject({ role: 'tool' })
    expect(told[0]!.content).toContain('回答用中文')
  })
})
