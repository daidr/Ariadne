import { Ariadne } from '@ariadne/core'
import type { ChatMessage } from '@ariadne/plugin-sdk'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { Scheduler } from './scheduler'
import { answered, call, fakeModel, lastUser, say } from './test-model'

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
})
