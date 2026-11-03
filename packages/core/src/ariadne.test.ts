import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { Ariadne } from './ariadne'
import { DomainError } from './errors'
import type { AnyStoredEvent } from './events'

let app: Ariadne
let clock = 1_000

beforeEach(() => {
  clock = 1_000
  app = new Ariadne({ path: ':memory:', now: () => ++clock })
})

afterEach(() => app.close())

describe('projects', () => {
  test('creates a project with its own coordinator', () => {
    const project = app.createProject('  Ariadne  ')
    expect(project.name).toBe('Ariadne')
    expect(project.coordinatorId).toMatch(/^crd_/)
    expect(app.listProjects().map((p) => p.id)).toEqual([project.id])
  })

  test('rejects an empty name', () => {
    expect(() => app.createProject('   ')).toThrow(DomainError)
  })

  test('lists the most recently active project first', () => {
    const a = app.createProject('A')
    app.createProject('B')
    app.renameProject(a.id, 'A2')
    expect(app.listProjects().map((p) => p.name)).toEqual(['A2', 'B'])
  })
})

describe('thread stages', () => {
  test('follow what the thread waits on', () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: '调研' })
    expect(thread.stage).toBe('queued')
    expect(app.startThread(thread.id).stage).toBe('working')
    expect(app.reportThread(thread.id, 'waiting', '本轮结束').stage).toBe('idle')
    expect(app.reportThread(thread.id, 'ready', '可以审阅').stage).toBe('review')

    const attention = app.openAttention(project.id, {
      threadId: thread.id,
      question: '用哪个方案？',
      options: [
        { label: 'A', consequence: '更快' },
        { label: 'B', consequence: '更稳' },
      ],
      recommended: 1,
    })
    expect(app.getThread(thread.id).stage).toBe('answer')
    expect(app.getProject(project.id).needsMe).toBe(1)

    app.answerAttention(attention.id, { choice: 1 })
    expect(app.getThread(thread.id).stage).toBe('review')
    expect(app.reportThread(thread.id, 'done', '完成').stage).toBe('done')
    expect(app.getProject(project.id).needsMe).toBe(0)
  })

  test('counts coordinator questions and waiting threads in "needs me"', () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: 't' })
    app.startThread(thread.id)
    app.reportThread(thread.id, 'blocked', '缺少权限')
    app.openAttention(project.id, { question: '要开始吗？', options: [] })
    expect(app.getProject(project.id).needsMe).toBe(2)
  })

  test('rejects a recommendation outside the options', () => {
    const project = app.createProject('P')
    expect(() =>
      app.openAttention(project.id, { question: 'q', options: [], recommended: 0 }),
    ).toThrow(DomainError)
  })
})

describe('messages', () => {
  test('only the author may edit, and edits after reading are highlighted once', () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: 't' })
    const message = app.postMessage(project.id, {
      conversationId: thread.id,
      author: 'thread',
      content: 'Done.',
    })

    expect(() => app.editMessage(message.id, 'coordinator', '完成。', '改成中文')).toThrow(
      DomainError,
    )

    const unread = app.editMessage(message.id, 'thread', '完成了。', '用户偏好中文')
    expect(unread.editedAt).not.toBeNull()
    expect(unread.editedSinceRead).toBe(false)

    app.markRead(thread.id, message.id)
    const edited = app.editMessage(message.id, 'thread', '已完成。', '措辞')
    expect(edited.editedSinceRead).toBe(true)
    expect(edited.editReason).toBe('措辞')

    app.markRead(thread.id, message.id)
    expect(app.listMessages(thread.id)[0]?.editedSinceRead).toBe(false)
  })

  test('only the coordinator relays into a thread, and relays cannot be edited', () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: 't' })
    expect(() =>
      app.postMessage(project.id, {
        conversationId: thread.id,
        author: 'user',
        kind: 'relay',
        content: 'x',
      }),
    ).toThrow(DomainError)
    const relay = app.postMessage(project.id, {
      conversationId: thread.id,
      author: 'coordinator',
      kind: 'relay',
      content: '请用中文回复',
    })
    expect(relay.kind).toBe('relay')
    expect(() => app.editMessage(relay.id, 'coordinator', 'y', 'z')).toThrow(DomainError)
  })

  test('rejects a conversation from another project', () => {
    const a = app.createProject('A')
    const b = app.createProject('B')
    expect(() =>
      app.postMessage(a.id, { conversationId: b.coordinatorId, author: 'user', content: 'hi' }),
    ).toThrow(DomainError)
  })
})

describe('todo', () => {
  test('replaces the whole list on every update', () => {
    const project = app.createProject('P')
    expect(app.getTodo(project.coordinatorId).items).toEqual([])
    app.updateTodo(project.id, project.coordinatorId, [{ label: '拆解需求', status: 'doing' }])
    const todo = app.updateTodo(project.id, project.coordinatorId, [
      { label: '拆解需求', status: 'done' },
      { label: '等你确认计划', status: 'waiting', detail: '计划图第一版' },
    ])
    expect(todo.items).toHaveLength(2)
    expect(todo.items[1]?.status).toBe('waiting')
  })
})

describe('event log', () => {
  test('notifies subscribers after commit and replays into the same state', () => {
    const seen: AnyStoredEvent[] = []
    app.subscribe((events) => seen.push(...events))
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: 't' })
    app.startThread(thread.id)
    app.postMessage(project.id, { conversationId: thread.id, author: 'thread', content: 'a' })

    expect(seen.map((e) => e.type)).toEqual([
      'project.created',
      'thread.created',
      'thread.started',
      'message.posted',
    ])
    expect(app.events({ projectId: project.id, afterSeq: 2 }).map((e) => e.seq)).toEqual([3, 4])

    const before = { threads: app.listThreads(project.id), messages: app.listMessages(thread.id) }
    app.rebuildProjections()
    expect({ threads: app.listThreads(project.id), messages: app.listMessages(thread.id) }).toEqual(
      before,
    )
  })

  test('rolls back the whole append when a projection fails', () => {
    const project = app.createProject('P')
    const before = app.store.lastSeq()
    // The same thread id twice violates the threads primary key inside the transaction.
    const data = { threadId: 'thr_x', title: 't', nodeId: null }
    expect(() =>
      app.store.append([
        { type: 'thread.created', projectId: project.id, data },
        { type: 'thread.created', projectId: project.id, data },
      ]),
    ).toThrow()
    expect(app.store.lastSeq()).toBe(before)
    expect(app.listThreads(project.id)).toEqual([])
  })
})
