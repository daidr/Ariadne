import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { Ariadne } from './ariadne'
import type { Author } from './events'
import { previewOf } from './artifact-content'
import { searchTextOf, snippetOf } from './search'

let app: Ariadne
let clock = 1_000

beforeEach(() => {
  clock = 1_000
  app = new Ariadne({ path: ':memory:', now: () => ++clock })
})

afterEach(() => app.close())

function say(projectId: string, conversationId: string, content: string, author: Author = 'user') {
  return app.postMessage(projectId, { conversationId, author, content })
}

describe('message pages', () => {
  test('start from the newest messages and go back page by page', () => {
    const project = app.createProject('P')
    const coordinator = project.coordinatorId
    const ids = Array.from({ length: 7 }, (_, i) => say(project.id, coordinator, `第 ${i} 条`).id)

    const newest = app.pageMessages(coordinator, { limit: 3 })
    expect(newest.messages.map((m) => m.id)).toEqual(ids.slice(4))
    expect(newest.more).toBe(true)

    const earlier = app.pageMessages(coordinator, { before: ids[4], limit: 3 })
    expect(earlier.messages.map((m) => m.id)).toEqual(ids.slice(1, 4))
    expect(earlier.more).toBe(true)

    const first = app.pageMessages(coordinator, { before: ids[1], limit: 3 })
    expect(first.messages.map((m) => m.id)).toEqual(ids.slice(0, 1))
    expect(first.more).toBe(false)

    // A page already shown, with what came since.
    const later = say(project.id, coordinator, '新的').id
    const shown = app.pageMessages(coordinator, { from: ids[4] })
    expect(shown.messages.map((m) => m.id)).toEqual([...ids.slice(4), later])
    expect(shown.more).toBe(true)
    expect(app.pageMessages(coordinator, { from: ids[0] }).more).toBe(false)
  })

  test('carry the notices that fall among their messages', () => {
    const project = app.createProject('P')
    const coordinator = project.coordinatorId
    const a = say(project.id, coordinator, 'a').id
    app.saveMemory(project.id, { text: '早的', author: coordinator })
    const b = say(project.id, coordinator, 'b').id
    app.saveMemory(project.id, { text: '晚的', author: coordinator })

    const newest = app.pageMessages(coordinator, { limit: 1 })
    expect(newest.messages.map((m) => m.id)).toEqual([b])
    expect(newest.notices.map((n) => n.kind === 'memory' && n.text)).toEqual(['晚的'])

    const earlier = app.pageMessages(coordinator, { before: b, limit: 1 })
    expect(earlier.messages.map((m) => m.id)).toEqual([a])
    expect(earlier.notices.map((n) => n.kind === 'memory' && n.text)).toEqual(['早的'])

    // A conversation shown whole has every notice.
    expect(app.pageMessages(coordinator).notices).toHaveLength(2)
  })
})

describe('changes since an event', () => {
  test('bring only what was posted or changed after it, edits and undone notices too', () => {
    const project = app.createProject('P')
    const coordinator = project.coordinatorId
    const a = say(project.id, coordinator, 'a', 'coordinator').id
    const b = say(project.id, coordinator, 'b').id
    const notice = app.saveMemory(project.id, { text: '记一下', author: coordinator })
    const seen = app.lastSeq()
    expect(app.pageMessages(coordinator, { since: seen })).toEqual({
      messages: [],
      notices: [],
      more: false,
    })

    app.editMessage(a, { author: 'coordinator', conversationId: coordinator }, 'a2', '改错字')
    const c = say(project.id, coordinator, 'c').id
    const changed = app.pageMessages(coordinator, { since: seen })
    expect(changed.messages.map((m) => [m.id, m.content])).toEqual([
      [a, 'a2'],
      [c, 'c'],
    ])
    // Only from the first message shown on.
    expect(
      app.pageMessages(coordinator, { since: seen, from: b }).messages.map((m) => m.id),
    ).toEqual([c])

    const before = app.lastSeq()
    const id = app.pageMessages(coordinator).notices[0]!.id
    app.undoMemoryNotice(project.id, id)
    expect(
      app.pageMessages(coordinator, { since: before }).notices.find((n) => n.id === id),
    ).toMatchObject({ undone: true })
    expect(notice).toBeTruthy()
  })
})

describe('transcript read in parts', () => {
  test('adds what came after an event; an edit says to read it again', () => {
    const project = app.createProject('P')
    const coordinator = project.coordinatorId
    const thread = app.createThread(project.id, { title: 't' })
    say(project.id, coordinator, '第一句')
    const first = app.listTranscript(coordinator)
    const seen = app.lastSeq()
    say(project.id, coordinator, '第二句')
    const added = app.listTranscript(coordinator, { after: seen })
    expect(added.map((e) => e.kind === 'message' && e.message.content)).toEqual(['第二句'])
    expect([...first, ...added]).toEqual(app.listTranscript(coordinator))

    expect(app.messagesChangedSince(coordinator, seen)).toBe(false)
    // No path edits what an agent was told today; the check keeps a later one honest.
    const told = say(project.id, thread.id, '去做')
    const later = app.lastSeq()
    app.editMessage(told.id, { author: 'user', conversationId: thread.id }, '去做吧', '语气')
    expect(app.messagesChangedSince(thread.id, later)).toBe(true)
    expect(app.messagesChangedSince(coordinator, later)).toBe(false)
  })
})

describe('triage', () => {
  test('is all the coordinator has waiting when the user only wrote inside threads', () => {
    const project = app.createProject('P')
    const coordinator = project.coordinatorId
    const thread = app.createThread(project.id, { title: 't' })
    expect(app.pendingIsTriage(coordinator)).toBe(false)
    say(project.id, thread.id, '顺便把这个也改了')
    expect(app.pendingIsTriage(coordinator)).toBe(true)
    // The thread itself is not triage.
    expect(app.pendingIsTriage(thread.id)).toBe(false)
    say(project.id, coordinator, '进展怎么样')
    expect(app.pendingIsTriage(coordinator)).toBe(false)
  })
})

describe('project search', () => {
  test('finds messages, threads and artifacts, in Chinese too', () => {
    const project = app.createProject('P')
    const other = app.createProject('Q')
    const thread = app.createThread(project.id, { title: '实现会话分页' })
    say(project.id, project.coordinatorId, '长对话要能分页加载')
    say(project.id, thread.id, '先取最近 40 条，往上滚再加载 earlier pages', 'thread')
    say(other.id, other.coordinatorId, '别的项目也说分页')
    app.createArtifact(project.id, {
      kind: 'document',
      title: '分页设计',
      author: 'user',
      content: '# 设计\n\n用 FTS5 trigram 做搜索',
    })

    const found = app.search(project.id, '分页')
    expect(found.threads.map((t) => t.title)).toEqual(['实现会话分页'])
    expect(found.artifacts.map((a) => a.title)).toEqual(['分页设计'])
    expect(found.messages.map((m) => m.snippet)).toEqual(['长对话要能分页加载'])
    expect(found.messages[0]).toMatchObject({ conversationId: project.coordinatorId, where: null })

    // Three characters or more go through the trigram index, ignoring case.
    const pages = app.search(project.id, 'EARLIER')
    expect(pages.messages).toMatchObject([{ conversationId: thread.id, where: '实现会话分页' }])
    expect(app.search(project.id, 'trigram').artifacts).toHaveLength(1)

    // Every word must be there.
    expect(app.search(project.id, '分页 加载').messages).toHaveLength(1)
    expect(app.search(project.id, '分页 不存在的词').messages).toHaveLength(0)
    expect(app.search(project.id, '   ').messages).toHaveLength(0)
  })

  test('follows edits, reports and revisions, and survives a rebuild', () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: '调研' })
    const message = say(project.id, thread.id, '旧的说法', 'thread')
    app.editMessage(message.id, { author: 'thread', conversationId: thread.id }, '新的说法', '改')
    app.reportThread(thread.id, { kind: 'done', summary: 's', headline: '可以审阅合并请求了' })
    const artifact = app.createArtifact(project.id, {
      kind: 'whiteboard',
      title: '白板',
      author: 'user',
      content: JSON.stringify({
        version: 1,
        nodes: [{ id: 'a', type: 'sticky', x: 0, y: 0, data: { text: '便签上的字' } }],
        edges: [],
      }),
    })
    app.reviseArtifact(artifact.id, {
      title: '改名的白板',
      content: JSON.stringify({
        version: 1,
        nodes: [{ id: 'a', type: 'sticky', x: 0, y: 0, data: { text: '改过的便签' } }],
        edges: [],
      }),
      author: 'user',
      reason: '改',
    })

    const check = () => {
      expect(app.search(project.id, '旧的').messages).toHaveLength(0)
      expect(app.search(project.id, '新的').messages).toHaveLength(1)
      expect(app.search(project.id, '合并请求').threads.map((t) => t.snippet)).toEqual([
        '可以审阅合并请求了',
      ])
      expect(app.search(project.id, '便签上').artifacts).toHaveLength(0)
      expect(app.search(project.id, '改过的').artifacts).toMatchObject([
        { title: '改名的白板', artifactKind: 'whiteboard', snippet: '改过的便签' },
      ])
    }
    check()
    app.rebuildProjections()
    check()
  })

  test('takes literal characters and forgets a deleted project', () => {
    const project = app.createProject('P')
    say(project.id, project.coordinatorId, '100% 完成 a_b "引号"')
    expect(app.search(project.id, '%').messages).toHaveLength(1)
    expect(app.search(project.id, '_').messages).toHaveLength(1)
    expect(app.search(project.id, '"引号"').messages).toHaveLength(1)
    expect(app.search(project.id, 'OR').messages).toHaveLength(0)
    app.deleteProject(project.id)
    const count = app.store.db
      .query<{ n: number }, []>('SELECT count(*) AS n FROM search_text')
      .get()
    expect(count?.n).toBe(0)
  })
})

describe('snippets', () => {
  test('show the text around the first match on one line', () => {
    const long = `${'前'.repeat(60)}关键词${'后'.repeat(200)}`
    const snippet = snippetOf(long, ['关键词'])
    expect(snippet.startsWith('…')).toBe(true)
    expect(snippet.endsWith('…')).toBe(true)
    expect(snippet).toContain('关键词')
    expect(snippetOf('第一行\n\n第二行', ['第二'])).toBe('第一行 第二行')
  })
})

describe('snippets of Markdown', () => {
  test('read as text', () => {
    expect(snippetOf('# 标题\n\n**会话分页**见 [设计](artifact:art_1)，`code`', ['分页'])).toBe(
      '标题 会话分页见 设计，code',
    )
  })
})

describe('whiteboards in a document', () => {
  const board = {
    height: 320,
    board: {
      version: 1,
      nodes: [{ id: 'a', type: 'shape', x: 0, y: 0, data: { shape: 'rect', text: '有库存？' } }],
      edges: [],
    },
  }
  const doc = `# 流程\n\n\`\`\`dou-whiteboard\n${JSON.stringify(board)}\n\`\`\`\n\n结尾`

  test('search finds the words on the board, not its data', () => {
    const text = searchTextOf('document', doc)
    expect(text).toContain('有库存？')
    expect(text).not.toContain('"nodes"')
  })

  test('the preview leaves the board out', () => {
    const preview = previewOf('document', doc)
    expect(preview.kind === 'document' && preview.lead).toBe('# 流程\n\n\n\n结尾')
  })
})
