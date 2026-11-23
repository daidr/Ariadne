// @vitest-environment happy-dom
import type { ConversationView, MessageView, ProjectView } from '@ariadne/core'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { api } from './api'
import {
  buildOptimisticMessage,
  nextOptimisticTimestamp,
  optimisticAttachmentsOf,
  reconcilePendingMessages,
  type PendingUserMessage,
} from './optimistic-messages'
import { emptyConversation, renderKeyOf, sendMessage, state, upsertMessage } from './store'

function makeMessage(
  id: string,
  content: string,
  overrides: Partial<MessageView> = {},
): MessageView {
  return {
    id,
    conversationId: 'crd_1',
    author: 'user',
    kind: 'chat',
    content,
    blocks: [],
    ask: null,
    attachments: [],
    mentions: [],
    comments: [],
    cites: [],
    relayedTo: [],
    replyTo: null,
    reactions: [],
    createdAt: 1000,
    editedAt: null,
    editReason: null,
    originalContent: null,
    editedSinceRead: false,
    ...overrides,
  }
}

const project = {
  id: 'prj_1',
  name: 'Project 1',
  coordinatorId: 'crd_1',
} as unknown as ProjectView

const conversation = {
  id: 'crd_1',
  projectId: 'prj_1',
  turn: null,
  pendingSeq: 0,
  lastOutcome: 'completed',
  lastError: null,
  wait: null,
  contextTokens: 0,
  compactions: 0,
  usage: { input: 0, output: 0, cached: 0, cacheWrite: 0, reasoning: 0, costUsd: 0, costCny: 0 },
} as unknown as ConversationView

describe('optimistic message helpers', () => {
  test('nextOptimisticTimestamp stays after all existing messages, notices, and pending entries', () => {
    const pending: PendingUserMessage = {
      clientId: 'pending:1',
      projectId: 'prj_1',
      conversationId: 'crd_1',
      baselineIds: [],
      message: buildOptimisticMessage({
        clientId: 'pending:1',
        conversationId: 'crd_1',
        content: 'first',
        createdAt: 5000,
      }),
    }
    expect(
      nextOptimisticTimestamp(2000, [{ createdAt: 4000 }], [{ createdAt: 4500 }], [pending]),
    ).toBe(5001)
    expect(nextOptimisticTimestamp(9000, [{ createdAt: 4000 }], [], [pending])).toBe(9000)
  })

  test('optimisticAttachmentsOf prefers uploaded previews and falls back to raw extras', () => {
    expect(
      optimisticAttachmentsOf(
        { attachments: [{ fileId: 'file_1', name: 'shot.png' }] },
        {
          attachments: [{ fileId: 'file_1', name: 'shot.png', mime: 'image/png', size: 2048 }],
        },
      ),
    ).toEqual([{ fileId: 'file_1', name: 'shot.png', mime: 'image/png', size: 2048 }])

    expect(
      optimisticAttachmentsOf({ attachments: [{ fileId: 'file_2', name: 'note.txt' }] }),
    ).toEqual([{ fileId: 'file_2', name: 'note.txt', mime: 'text/plain', size: 0 }])
  })

  test('reconcilePendingMessages skips baseline messages and matches duplicates 1-to-1', () => {
    const older = makeMessage('msg_001', 'same text')
    const p1: PendingUserMessage = {
      clientId: 'pending:1',
      projectId: 'prj_1',
      conversationId: 'crd_1',
      baselineIds: ['msg_001'],
      message: buildOptimisticMessage({
        clientId: 'pending:1',
        conversationId: 'crd_1',
        content: 'same text',
        createdAt: 2000,
      }),
    }
    const p2: PendingUserMessage = {
      clientId: 'pending:2',
      projectId: 'prj_1',
      conversationId: 'crd_1',
      baselineIds: ['msg_001'],
      message: buildOptimisticMessage({
        clientId: 'pending:2',
        conversationId: 'crd_1',
        content: 'same text',
        createdAt: 2001,
      }),
    }

    // Only the pre-existing baseline message is present: both pending entries stay visible.
    const before = reconcilePendingMessages([p1, p2], [older])
    expect(before.visible.map((item) => item.clientId)).toEqual(['pending:1', 'pending:2'])
    expect(before.matchedKeys.size).toBe(0)

    // One confirmed message arrives: matches p1 only, leaving p2 visible.
    const confirmed1 = makeMessage('msg_002', 'same text', { createdAt: 2002 })
    const mid = reconcilePendingMessages([p1, p2], [older, confirmed1])
    expect(mid.visible.map((item) => item.clientId)).toEqual(['pending:2'])
    expect(mid.matchedKeys.get('msg_002')).toBe('pending:1')

    // Second confirmed message arrives: matches p2.
    const confirmed2 = makeMessage('msg_003', 'same text', { createdAt: 2003 })
    const after = reconcilePendingMessages([p1, p2], [older, confirmed1, confirmed2])
    expect(after.visible).toEqual([])
    expect(after.matchedKeys.get('msg_002')).toBe('pending:1')
    expect(after.matchedKeys.get('msg_003')).toBe('pending:2')
  })

  test('reconcilePendingMessages matches trimmed line comments', () => {
    const pending: PendingUserMessage = {
      clientId: 'pending:3',
      projectId: 'prj_1',
      conversationId: 'thr_1',
      baselineIds: [],
      message: buildOptimisticMessage({
        clientId: 'pending:3',
        conversationId: 'thr_1',
        content: '',
        comments: [{ path: ' a.ts ', line: 12, quote: 'const x = 1', body: '  改这里  ' }],
        createdAt: 3000,
      }),
    }
    const confirmed = makeMessage('msg_010', '', {
      conversationId: 'thr_1',
      comments: [{ path: 'a.ts', line: 12, quote: 'const x = 1', body: '改这里' }],
    })
    const result = reconcilePendingMessages([pending], [confirmed])
    expect(result.visible).toEqual([])
    expect(result.matchedKeys.get('msg_010')).toBe('pending:3')
  })
})

describe('store optimistic send and upsertMessage', () => {
  beforeEach(() => {
    state.currentId = 'prj_1'
    state.detail = { project, threads: [], needsMe: [], hasTracker: false }
    state.coordinator = { ...emptyConversation(), conversation, seq: 1 }
    state.threads = {}
    state.pendingMessages = {}
  })

  afterEach(() => {
    vi.restoreAllMocks()
    state.pendingMessages = {}
  })

  test('upsertMessage inserts and replaces without mutating seq', () => {
    const first = makeMessage('msg_002', 'hello')
    const afterInsert = upsertMessage(state.coordinator, first)
    expect(afterInsert.messages).toEqual([first])
    expect(afterInsert.seq).toBe(1)
    expect(afterInsert.from).toBe('msg_002')

    const updated = makeMessage('msg_002', 'hello edited')
    const afterReplace = upsertMessage(afterInsert, updated)
    expect(afterReplace.messages).toEqual([updated])
    expect(afterReplace.seq).toBe(1)
  })

  test('sendMessage queues an optimistic message immediately and reconciles on resolution', async () => {
    let resolvePost!: (value: { message: MessageView }) => void
    vi.spyOn(api, 'postMessage').mockImplementation(
      () =>
        new Promise((resolve) => {
          resolvePost = resolve
        }),
    )

    const sending = sendMessage(
      'crd_1',
      '立即上屏测试',
      { attachments: [{ fileId: 'file_1', name: 'img.png' }] },
      { attachments: [{ fileId: 'file_1', name: 'img.png', mime: 'image/png', size: 512 }] },
    )

    const queued = state.pendingMessages['crd_1']
    expect(queued).toHaveLength(1)
    const clientId = queued![0]!.clientId
    expect(queued![0]!.message.content).toBe('立即上屏测试')
    expect(queued![0]!.message.attachments).toEqual([
      { fileId: 'file_1', name: 'img.png', mime: 'image/png', size: 512 },
    ])

    const confirmed = makeMessage('msg_confirmed_1', '立即上屏测试', {
      attachments: [{ fileId: 'file_1', name: 'img.png', mime: 'image/png', size: 512 }],
    })
    resolvePost({ message: confirmed })
    expect(await sending).toBe(true)

    expect(state.pendingMessages['crd_1']).toBeUndefined()
    expect(state.coordinator.messages.map((item) => item.id)).toEqual(['msg_confirmed_1'])
    expect(renderKeyOf('msg_confirmed_1')).toBe(clientId)
  })

  test('sendMessage removes the optimistic message when postMessage fails', async () => {
    vi.spyOn(api, 'postMessage').mockRejectedValue(new Error('network error'))

    const sending = sendMessage('crd_1', '失败消息')
    expect(state.pendingMessages['crd_1']).toHaveLength(1)

    expect(await sending).toBe(false)
    expect(state.pendingMessages['crd_1']).toBeUndefined()
    expect(state.coordinator.messages).toEqual([])
  })
})
