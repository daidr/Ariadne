import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { Ariadne } from './ariadne'
import { DomainError } from './errors'
import { LiveCommands, LIVE_TAIL, screenText } from './live'

let app: Ariadne
let clock = 1_000

beforeEach(() => {
  clock = 1_000
  app = new Ariadne({ path: ':memory:', now: () => ++clock })
})

afterEach(() => app.close())

describe('mentions', () => {
  test('take titles from the project and keep files as paths', () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: '修复导出' })
    const artifact = app.createArtifact(project.id, {
      kind: 'document',
      title: '发布计划',
      author: 'user',
      content: '# 计划',
    })
    const message = app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '看看 @修复导出 和 @发布计划',
      mentions: [
        { kind: 'thread', id: thread.id, title: 'forged' },
        { kind: 'artifact', id: artifact.id },
        { kind: 'file', path: './src/a.ts' },
        { kind: 'file', path: 'src/a.ts' },
      ],
    })
    expect(message.mentions).toEqual([
      { kind: 'thread', id: thread.id, title: '修复导出' },
      { kind: 'artifact', id: artifact.id, title: '发布计划' },
      { kind: 'file', path: 'src/a.ts' },
    ])
    expect(app.listMessages(project.coordinatorId)[0]?.mentions).toHaveLength(3)
  })

  test('refuse another project, absolute paths and agents', () => {
    const project = app.createProject('P')
    const other = app.createProject('Q')
    const foreign = app.createThread(other.id, { title: 'x' })
    const post = (mentions: unknown[], author: 'user' | 'coordinator' = 'user') =>
      app.postMessage(project.id, {
        conversationId: project.coordinatorId,
        author,
        content: 'hi',
        mentions,
      })
    expect(() => post([{ kind: 'thread', id: foreign.id }])).toThrow(DomainError)
    expect(() => post([{ kind: 'file', path: '/etc/passwd' }])).toThrow(DomainError)
    expect(() => post([{ kind: 'nope' }])).toThrow(DomainError)
    expect(() => post([{ kind: 'file', path: 'a' }], 'coordinator')).toThrow(DomainError)
  })
})

describe('line comments', () => {
  test('go to a thread alone, without text and without the coordinator looking', () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: 't' })
    const message = app.postMessage(project.id, {
      conversationId: thread.id,
      author: 'user',
      content: '',
      comments: [{ path: ' src/a.ts ', line: 3, quote: 'const a = 1', body: ' 改成 2 ' }],
    })
    expect(message.comments).toEqual([
      { path: 'src/a.ts', line: 3, quote: 'const a = 1', body: '改成 2' },
    ])
    const coordinator = app.listTranscript(project.coordinatorId)
    expect(coordinator.some((entry) => entry.kind === 'note')).toBe(false)
    expect(app.listTranscript(thread.id).some((entry) => entry.kind === 'message')).toBe(true)
  })

  test('stay listed for the thread, oldest first, each with its message', () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: 't' })
    const send = (body: string) =>
      app.postMessage(project.id, {
        conversationId: thread.id,
        author: 'user',
        content: '',
        comments: [
          { path: 'a.ts', line: 1, quote: 'x', body },
          { path: 'b.ts', line: 2, quote: 'y', body: `${body}!`, side: 'original' },
        ],
      })
    const first = send('一')
    app.postMessage(project.id, { conversationId: thread.id, author: 'user', content: '没有批注' })
    const second = send('二')
    const listed = app.listLineComments(thread.id)
    expect(listed.map((c) => [c.messageId, c.body, c.side])).toEqual([
      [first.id, '一', undefined],
      [first.id, '一!', 'original'],
      [second.id, '二', undefined],
      [second.id, '二!', 'original'],
    ])
    expect(new Set(listed.map((c) => c.id)).size).toBe(4)
  })

  test('are refused when malformed or sent to the coordinator', () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: 't' })
    const post = (conversationId: string, comment: Record<string, unknown>) =>
      app.postMessage(project.id, {
        conversationId,
        author: 'user',
        content: '',
        comments: [{ path: 'a', line: 1, quote: '', body: 'b', ...comment }],
      })
    expect(() => post(thread.id, { line: 0 })).toThrow(DomainError)
    expect(() => post(thread.id, { line: 1.5 })).toThrow(DomainError)
    expect(() => post(thread.id, { body: '  ' })).toThrow(DomainError)
    expect(() => post(thread.id, { path: '' })).toThrow(DomainError)
    expect(() => post(project.coordinatorId, {})).toThrow(DomainError)
    expect(post(thread.id, { quote: 'x'.repeat(900) }).comments[0]?.quote).toHaveLength(500)
  })
})

describe('turn changes', () => {
  test('are a notice of the turn, with totals over every file', () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: 't' })
    const turnId = app.startTurn(thread.id, 'm')
    const files = Array.from({ length: 205 }, (_, i) => ({
      path: `f${i}`,
      previousPath: null,
      status: 'modified' as const,
      added: 2,
      removed: i === 0 ? null : 1,
    }))
    app.recordTurnChanges(thread.id, turnId, { from: 'a'.repeat(40), to: 'b'.repeat(40), files })
    app.recordTurnChanges(thread.id, turnId, { from: 'a', to: 'a', files: [] })
    app.finishTurn(thread.id, turnId, 'completed')
    const notices = app.listNotices(thread.id).filter((n) => n.kind === 'turn-changes')
    expect(notices).toHaveLength(1)
    expect(notices[0]).toMatchObject({
      kind: 'turn-changes',
      turnId,
      from: 'a'.repeat(40),
      total: { files: 205, added: 410, removed: 204 },
    })
    expect(notices[0]!.kind === 'turn-changes' && notices[0]!.files).toHaveLength(200)
  })

  test('keep what the thread changed so far on the thread, from the newest turn that says', () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: 't' })
    const file = {
      path: 'a',
      previousPath: null,
      status: 'modified' as const,
      added: 1,
      removed: 0,
    }
    expect(app.getThread(thread.id).changes).toBeNull()
    const first = app.startTurn(thread.id, 'm')
    app.recordTurnChanges(thread.id, first, {
      from: 'a',
      to: 'b',
      files: [file],
      branch: { files: 2, added: 9, removed: 3 },
    })
    app.finishTurn(thread.id, first, 'completed')
    // A turn whose branch could not be read leaves what was known.
    const second = app.startTurn(thread.id, 'm')
    app.recordTurnChanges(thread.id, second, { from: 'b', to: 'c', files: [file] })
    app.finishTurn(thread.id, second, 'completed')
    expect(app.getThread(thread.id).changes).toEqual({ files: 2, added: 9, removed: 3 })
  })
})

describe('live commands', () => {
  test('screen text drops colours and keeps a redrawn line as last drawn', () => {
    expect(screenText('\x1b[32mok\x1b[0m\n10%\r50%\r100%\ndone\r\n')).toBe('ok\n100%\ndone\n')
  })

  test('start, print at most a few times a second, and end', () => {
    vi.useFakeTimers()
    try {
      const live = new LiveCommands(() => 5)
      const seen: unknown[] = []
      live.subscribe((update) => seen.push(update.command?.tail ?? null))
      const handle = live.start({ projectId: 'p', conversationId: 'c', command: 'x', cwd: '.' })
      handle.output('a')
      handle.output('b')
      expect(live.list('p')).toHaveLength(1)
      vi.advanceTimersByTime(300)
      handle.output('y'.repeat(LIVE_TAIL * 3))
      vi.advanceTimersByTime(300)
      handle.end()
      handle.output('late')
      vi.advanceTimersByTime(300)
      expect(seen[0]).toBe('')
      expect(seen[1]).toBe('ab')
      expect((seen[2] as string).length).toBe(LIVE_TAIL)
      expect(seen.at(-1)).toBeNull()
      expect(seen).toHaveLength(4)
      expect(live.list()).toEqual([])
    } finally {
      vi.useRealTimers()
    }
  })

  test('a newer command in the same conversation takes over', () => {
    const live = new LiveCommands()
    const first = live.start({ projectId: 'p', conversationId: 'c', command: 'a', cwd: '.' })
    live.start({ projectId: 'p', conversationId: 'c', command: 'b', cwd: '.' })
    first.end()
    expect(live.list().map((c) => c.command)).toEqual(['b'])
  })
})
