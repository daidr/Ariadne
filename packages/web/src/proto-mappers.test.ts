import {
  MessageViewSchema,
  NoticeViewSchema,
  ProjectWorktreesSchema,
  ReviewRoundViewSchema,
  ThreadViewSchema,
} from '@ariadne/proto'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import {
  fromProtoMessageView,
  fromProtoNoticeView,
  fromProtoProjectWorktrees,
  fromProtoReviewRound,
  fromProtoThreadView,
  toProtoBlockAnswer,
} from './proto-mappers'

describe('Domain 3 Protobuf mappers', () => {
  it('maps ThreadView fields and optional nested objects', () => {
    const proto = create(ThreadViewSchema, {
      id: 'thr_1',
      projectId: 'prj_1',
      title: '讨论串',
      stage: 'review',
      archived: false,
      auto: true,
      createdAt: 1000n,
      updatedAt: 2000n,
      activeAt: 3000n,
      usage: {
        input: 10n,
        output: 20n,
        cached: 0n,
        cacheWrite: 0n,
        reasoning: 5n,
        cost: { USD: 0.1 },
      },
      workspace: {
        label: 'main',
        branch: 'ariadne/test',
        base: 'main',
        removed: false,
        parked: true,
      },
      pullRequest: {
        number: 42,
        url: 'https://example.com/pr/42',
        title: 'PR 42',
        state: 'open',
        conflicted: false,
        headSha: 'abc123',
        checks: { state: 'passed', failed: [] },
      },
    })
    const view = fromProtoThreadView(proto)
    expect(view.id).toBe('thr_1')
    expect(view.createdAt).toBe(1000)
    expect(view.usage).toEqual({
      input: 10,
      output: 20,
      cached: 0,
      cacheWrite: 0,
      reasoning: 5,
      cost: { USD: 0.1 },
    })
    expect(view.workspace).toEqual({
      label: 'main',
      branch: 'ariadne/test',
      base: 'main',
      removed: false,
      parked: true,
    })
    expect(view.pullRequest?.checks).toEqual({ state: 'passed', failed: [] })
  })

  it('maps MessageView blocks, ask, reactions, and BlockAnswer round-trip', () => {
    const proto = create(MessageViewSchema, {
      id: 'msg_1',
      conversationId: 'thr_1',
      author: 'agent',
      kind: 'chat',
      content: 'hello',
      createdAt: 1234n,
      blocks: [
        {
          block: {
            case: 'table',
            value: {
              title: 'Summary',
              columns: [{ key: 'k', title: 'Key', align: 'end' }],
              rows: [{ cells: { k: { value: { case: 'numberValue', value: 7 } } } }],
            },
          },
        },
      ],
      ask: {
        attentionId: 'att_1',
        block: {
          block: {
            case: 'form',
            value: {
              title: 'Details',
              fields: [
                {
                  key: 'tags',
                  label: 'Tags',
                  kind: 'multiselect',
                  required: true,
                  defaultValue: {
                    value: { case: 'stringListValue', value: { values: ['a', 'b'] } },
                  },
                },
              ],
            },
          },
        },
        answer: {
          answer: {
            case: 'form',
            value: {
              values: {
                tags: { value: { case: 'stringListValue', value: { values: ['a'] } } },
              },
            },
          },
        },
      },
      reactions: [{ emoji: '👍', by: 'user' }],
    })
    const view = fromProtoMessageView(proto)
    expect(view.blocks).toEqual([
      {
        type: 'table',
        title: 'Summary',
        columns: [{ key: 'k', title: 'Key', align: 'end' }],
        rows: [{ k: 7 }],
      },
    ])
    expect(view.ask?.answer).toEqual({ type: 'form', values: { tags: ['a'] } })
    expect(view.reactions).toEqual([{ emoji: '👍', by: 'user' }])

    const encoded = toProtoBlockAnswer({
      type: 'form',
      values: { name: 'demo', count: 3, ok: true, list: ['x'], empty: null },
    })
    expect(encoded.answer?.case).toBe('form')
  })

  it('maps NoticeView oneof payloads, ReviewRoundView, and ProjectWorktrees', () => {
    const notice = fromProtoNoticeView(
      create(NoticeViewSchema, {
        id: 'not_1',
        conversationId: 'crd_1',
        createdAt: 500n,
        payload: {
          case: 'memory',
          value: { action: 'saved', memoryId: 'mem_1', text: 'remember', undone: false },
        },
      }),
    )
    expect(notice).toEqual({
      id: 'not_1',
      conversationId: 'crd_1',
      createdAt: 500,
      kind: 'memory',
      action: 'saved',
      memoryId: 'mem_1',
      text: 'remember',
      previous: null,
      undone: false,
    })

    const round = fromProtoReviewRound(
      create(ReviewRoundViewSchema, {
        threadId: 'thr_1',
        reviewerId: 'rev_1',
        round: 2,
        requestedAt: 100n,
        verdict: 'approved',
        abandoned: false,
        replies: { items: [{ comment: 1, outcome: 'fixed', note: 'done' }] },
      }),
    )
    expect(round.replies).toEqual([{ comment: 1, outcome: 'fixed', note: 'done' }])

    const wt = fromProtoProjectWorktrees(
      create(ProjectWorktreesSchema, {
        id: 'prj_1',
        name: 'Demo',
        shared: true,
        worktrees: [{ threadId: 'thr_1', path: '/tmp/wt', bytes: 2048n, orphaned: false }],
      }),
    )
    expect(wt.worktrees[0]?.bytes).toBe(2048)
    expect(wt.shared).toBe(true)
  })
})
