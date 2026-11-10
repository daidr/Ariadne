import type { MessageView } from '@ariadne/core'
import { describe, expect, test } from 'vitest'
import { relayIdOf, shownAs } from './relays'

const base: MessageView = {
  id: 'msg_r',
  conversationId: 'thr_a',
  author: 'coordinator',
  kind: 'relay',
  content: '请处理',
  blocks: [],
  ask: null,
  attachments: [],
  mentions: [],
  comments: [],
  cites: [],
  relayedTo: [],
  createdAt: 10,
  editedAt: null,
  editReason: null,
  originalContent: null,
  editedSinceRead: false,
}

describe('a relay in the thread panel', () => {
  test("without the user's messages it shows as it is", () => {
    expect(shownAs(base)).toEqual([base])
  })

  test("with them, only the user's words show, the first under the relay's id", () => {
    const shown = shownAs({
      ...base,
      cites: [
        { messageId: 'msg_1', conversationId: 'crd_x', content: '一', at: 1 },
        { messageId: 'msg_2', conversationId: 'thr_b', content: '二', at: 2 },
        // Written in this thread: already shown above.
        { messageId: 'msg_3', conversationId: 'thr_a', content: '三', at: 3 },
      ],
    })
    expect(shown.map((m) => [m.id, m.author, m.kind, m.content, m.createdAt])).toEqual([
      ['msg_r', 'user', 'chat', '一', 10],
      ['msg_r:1', 'user', 'chat', '二', 10],
    ])
    expect(shown.map((m) => relayIdOf(m.id))).toEqual(['msg_r', 'msg_r'])
  })

  test("passing back only what was written in this thread, it is the coordinator's message", () => {
    const back = {
      ...base,
      cites: [{ messageId: 'msg_3', conversationId: 'thr_a', content: '三', at: 3 }],
    }
    expect(shownAs(back)).toEqual([back])
  })
})
