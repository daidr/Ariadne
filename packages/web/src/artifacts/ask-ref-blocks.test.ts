// @vitest-environment happy-dom
import type { ArtifactSummary, ArtifactView } from '@ariadne/core'
import { editorKit } from '@dou-ui/editor'
import { blocksToMarkdown, markdownToBlocks } from '@dou-ui/editor/markdown'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, test, vi } from 'vitest'
import * as Y from 'yjs'
import { api } from '../api'
import ArtifactFeedbackNotice from '../components/ArtifactFeedbackNotice.vue'
import { setLocale } from '../i18n'
import { state } from '../store'
import { askDocumentBlock, normalizeAskProps } from './ask-block'
import {
  applyRecommendedDocumentAsks,
  isAskAnswerOnlyTransaction,
  readSharedDocumentAsks,
  snapshotDocumentAsks,
  summarizeDocumentAsks,
} from './document-asks'
import { fallbackAskId, type AskBlockProps } from './document-block-data'
import RefBlockView from './RefBlockView.vue'
import { refDocumentBlock } from './ref-block'

const extensions = [editorKit(), askDocumentBlock, refDocumentBlock]

function makeAsk(overrides: Partial<AskBlockProps>): AskBlockProps {
  return {
    id: 'ask-1',
    question: '缓存策略？',
    description: '',
    pick: 'one',
    options: [
      { id: 'redis', label: 'Redis', recommended: true },
      { id: 'mem', label: '内存' },
    ],
    allowNote: true,
    selected: [],
    note: '',
    confirmed: false,
    ...overrides,
  }
}

describe('document ask and ref blocks', () => {
  test('round-trips fenced ask and ref blocks through markdown', () => {
    const source = [
      '# 技术方案',
      '```ask\n{"id":"ask-auth","question":"登录方式选哪种？","pick":"one","options":[{"id":"jwt","label":"JWT","recommended":true},{"id":"session","label":"Session"}],"selected":["jwt"],"note":"优先无状态"}\n```',
      '```ref\n{"artifactId":"art_board_1","groupId":"grp-1","label":"导航方案对比"}\n```',
    ].join('\n\n')

    const blocks = markdownToBlocks(source, extensions)
    const ask = blocks.find((b) => b.type === 'ask')
    const ref = blocks.find((b) => b.type === 'ref')
    expect(ask?.props).toMatchObject({
      id: 'ask-auth',
      question: '登录方式选哪种？',
      pick: 'one',
      selected: ['jwt'],
      note: '优先无状态',
      confirmed: false,
    })
    expect(ref?.props).toEqual({
      artifactId: 'art_board_1',
      groupId: 'grp-1',
      optionId: '',
      label: '导航方案对比',
    })

    const out = blocksToMarkdown(blocks as never, extensions)
    expect(out).toContain('```ask\n')
    expect(out).toContain('"id":"ask-auth"')
    expect(out).toContain('```ref\n')
    expect(out).toContain('"artifactId":"art_board_1"')
  })

  test('parses inline <ask> and <ref> HTML tags', () => {
    const source = [
      '<ask id="ask-1" question="是否开启审计日志？" options=\'[{"id":"yes","label":"开启","recommended":true},{"id":"no","label":"暂不"}]\'></ask>',
      '<ref artifact="artifact:art_123" option="opt-a" label="方案 A"></ref>',
    ].join('\n\n')

    const blocks = markdownToBlocks(source, extensions)
    const ask = blocks.find((b) => b.type === 'ask')
    const ref = blocks.find((b) => b.type === 'ref')
    expect(ask?.props).toMatchObject({
      id: 'ask-1',
      question: '是否开启审计日志？',
      pick: 'one',
      options: [
        { id: 'yes', label: '开启', recommended: true },
        { id: 'no', label: '暂不' },
      ],
    })
    expect(ref?.props).toEqual({
      artifactId: 'art_123',
      groupId: '',
      optionId: 'opt-a',
      label: '方案 A',
    })
  })

  test('summarizes active asks and applies recommended picks on Yjs doc', () => {
    const doc = new Y.Doc()
    const root = doc.getMap('document')

    const putAsk = (key: string, raw: Record<string, unknown>) => {
      const block = new Y.Map<unknown>()
      block.set('type', 'ask')
      const props = new Y.Map<unknown>()
      const normalized = normalizeAskProps(raw)!
      for (const [k, v] of Object.entries(normalized)) props.set(k, v)
      block.set('props', props)
      root.set(key, block)
    }

    putAsk('b1', {
      id: 'ask-done',
      question: '已结项问题',
      pick: 'one',
      options: [{ id: 'a', label: 'A', recommended: true }],
      selected: ['a'],
      confirmed: true,
    })
    putAsk('b2', {
      id: 'ask-rec',
      question: '缓存策略',
      pick: 'one',
      options: [
        { id: 'redis', label: 'Redis', recommended: true },
        { id: 'mem', label: '内存' },
      ],
    })
    putAsk('b3', {
      id: 'ask-multi',
      question: '需要哪些指标？',
      pick: 'many',
      options: [
        { id: 'p95', label: 'P95 延迟', recommended: true },
        { id: 'err', label: '错误率', recommended: true },
      ],
    })
    putAsk('b4', {
      id: 'ask-text',
      question: '其他约束？',
      pick: 'text',
    })

    const before = summarizeDocumentAsks(readSharedDocumentAsks(root).map((e) => e.ask))
    expect(before).toEqual({
      totalAsks: 3,
      answeredAsks: 0,
      recommendedFallbackAsks: 2,
      remainingAsks: 1,
      hasActiveAsks: true,
      hasUnsentChanges: false,
      needsSubmit: true,
    })

    const applied = applyRecommendedDocumentAsks(doc)
    expect(applied).toBe(2)

    const afterEntries = readSharedDocumentAsks(root)
    const afterById = new Map(afterEntries.map((e) => [e.ask.id, e.ask]))
    expect(afterById.get('ask-rec')?.selected).toEqual(['redis'])
    expect(afterById.get('ask-multi')?.selected).toEqual(['p95', 'err'])
    expect(afterById.get('ask-text')?.selected).toEqual([])

    const after = summarizeDocumentAsks(afterEntries.map((e) => e.ask))
    expect(after).toEqual({
      totalAsks: 3,
      answeredAsks: 2,
      recommendedFallbackAsks: 0,
      remainingAsks: 1,
      hasActiveAsks: true,
      hasUnsentChanges: true,
      needsSubmit: true,
    })
  })

  test('computes needsSubmit and hasUnsentChanges across single, multi, text, and partial asks', () => {
    // Single ask: unanswered -> needsSubmit=true even with empty baseline.
    const emptyBaseline = [{ id: 'ask-1', selected: [], note: '' }]
    expect(summarizeDocumentAsks([makeAsk({})], emptyBaseline)).toMatchObject({
      totalAsks: 1,
      answeredAsks: 0,
      hasUnsentChanges: false,
      needsSubmit: true,
    })

    // Single ask: answered before submit -> hasUnsentChanges=true, needsSubmit=true.
    const answeredOne = makeAsk({ selected: ['redis'], note: '  多实例共享  ' })
    expect(summarizeDocumentAsks([answeredOne], emptyBaseline)).toMatchObject({
      totalAsks: 1,
      answeredAsks: 1,
      hasUnsentChanges: true,
      needsSubmit: true,
    })

    // Single ask: submitted & unchanged -> needsSubmit=false (submit button hidden).
    const submittedOne = snapshotDocumentAsks([answeredOne])
    expect(submittedOne).toEqual([{ id: 'ask-1', selected: ['redis'], note: '多实例共享' }])
    expect(summarizeDocumentAsks([answeredOne], submittedOne)).toMatchObject({
      totalAsks: 1,
      answeredAsks: 1,
      hasUnsentChanges: false,
      needsSubmit: false,
    })

    // Whitespace-only note edit matches trimmed baseline -> still hidden.
    expect(
      summarizeDocumentAsks(
        [makeAsk({ selected: ['redis'], note: '多实例共享   ' })],
        submittedOne,
      ),
    ).toMatchObject({
      hasUnsentChanges: false,
      needsSubmit: false,
    })

    // Modifying selection or note -> needsSubmit=true again.
    expect(
      summarizeDocumentAsks([makeAsk({ selected: ['mem'], note: '多实例共享' })], submittedOne),
    ).toMatchObject({
      hasUnsentChanges: true,
      needsSubmit: true,
    })
    expect(
      summarizeDocumentAsks([makeAsk({ selected: ['redis'], note: '补充集群要求' })], submittedOne),
    ).toMatchObject({
      hasUnsentChanges: true,
      needsSubmit: true,
    })

    // Multi-ask: multi-select order invariance + text ask.
    const multiA = makeAsk({
      id: 'ask-multi',
      pick: 'many',
      options: [
        { id: 'a', label: 'A' },
        { id: 'b', label: 'B' },
      ],
      selected: ['b', 'a'],
    })
    const textAsk = makeAsk({
      id: 'ask-text',
      pick: 'text',
      options: [],
      selected: [],
      note: '使用 PostgreSQL',
    })
    const multiBaseline = snapshotDocumentAsks([multiA, textAsk])
    expect(
      summarizeDocumentAsks(
        [
          { ...multiA, selected: ['a', 'b'] },
          { ...textAsk, note: ' 使用 PostgreSQL ' },
        ],
        multiBaseline,
      ),
    ).toMatchObject({
      totalAsks: 2,
      answeredAsks: 2,
      hasUnsentChanges: false,
      needsSubmit: false,
    })

    // Multi-ask partial submit: 1/2 submitted, 2nd still unanswered -> stays visible (needsSubmit=true)
    // but hasUnsentChanges=false so the submit button stays disabled until user answers or edits.
    const partialBaseline = [
      { id: 'ask-multi', selected: ['a'], note: '' },
      { id: 'ask-text', selected: [], note: '' },
    ]
    expect(
      summarizeDocumentAsks(
        [
          { ...multiA, selected: ['a'] },
          { ...textAsk, note: '' },
        ],
        partialBaseline,
      ),
    ).toMatchObject({
      totalAsks: 2,
      answeredAsks: 1,
      remainingAsks: 1,
      hasUnsentChanges: false,
      needsSubmit: true,
    })
  })

  test('detects ask-answer-only Yjs transactions accurately', () => {
    const doc = new Y.Doc()
    const root = doc.getMap('document')
    const askBlock = new Y.Map<unknown>()
    askBlock.set('type', 'ask')
    const askProps = new Y.Map<unknown>()
    askProps.set('id', 'ask-1')
    askProps.set('selected', [])
    askProps.set('note', '')
    askBlock.set('props', askProps)
    root.set('b1', askBlock)

    const paraBlock = new Y.Map<unknown>()
    paraBlock.set('type', 'paragraph')
    const paraText = new Y.Text('hello')
    paraBlock.set('text', paraText)
    root.set('b2', paraBlock)

    const results: boolean[] = []
    doc.on('update', (_u, _o, d, tr) => {
      results.push(isAskAnswerOnlyTransaction(d, tr))
    })

    doc.transact(() => askProps.set('selected', ['redis']))
    doc.transact(() => askProps.set('note', '备注'))
    doc.transact(() => askProps.set('question', '改题目'))
    doc.transact(() => paraText.insert(5, ' world'))
    doc.transact(() => {
      askProps.set('selected', ['mem'])
      paraText.insert(0, '!')
    })
    doc.transact(() => root.delete('b1'))

    expect(results).toEqual([true, true, false, false, false, false])
  })

  test('computes the same UTF-8 FNV-1a fallback ask ID as the Go backend', () => {
    // Must match TestFallbackAskIDDeterministicUTF8FNV1a in internal/core/artifact_feedback_p1_test.go.
    expect(fallbackAskId('Choose auth strategy')).toBe('ask-b7d87cc3')
    expect(fallbackAskId('登录态采用哪种方案？')).toBe('ask-689d2ac1')
    expect(fallbackAskId('发布目标 🚀')).toBe('ask-02560760')
    expect(normalizeAskProps({ question: '登录态采用哪种方案？' })?.id).toBe('ask-689d2ac1')
  })
})

function boardDetail(id: string, version: number, label: string) {
  return {
    artifact: {
      id,
      projectId: 'proj_1',
      kind: 'whiteboard' as const,
      title: '设计画板',
      nodeId: null,
      version,
      createdBy: 'agent',
      updatedBy: 'agent',
      createdAt: version * 1000,
      updatedAt: version * 1000,
      content: JSON.stringify({
        version: 1,
        nodes: [
          {
            id: 'grp-1',
            type: 'group',
            x: 0,
            y: 0,
            width: 400,
            height: 240,
            data: { label, round: 1, mode: 'choose' },
          },
        ],
        edges: [],
      }),
    } as ArtifactView,
    versions: [],
    comments: [],
  }
}

describe('RefBlockView resilience and race protection', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    state.artifacts = []
  })

  test('evicts failed requests from detail cache so later renders recover', async () => {
    const id = 'art_retry_case'
    state.artifacts = [
      {
        id,
        kind: 'whiteboard',
        title: '设计画板',
        nodeId: null,
        version: 1,
        updatedAt: 1000,
      } as ArtifactSummary,
    ]

    const spy = vi
      .spyOn(api, 'getArtifact')
      .mockRejectedValueOnce(new Error('transient network error'))
      .mockResolvedValueOnce(boardDetail(id, 1, '恢复后的分组标题'))

    const block = {
      id: 'blk-1',
      type: 'ref',
      props: { artifactId: id, groupId: 'grp-1', optionId: '', label: '' },
      content: [],
      children: [],
    }

    const first = mount(RefBlockView, {
      props: {
        block,
        editor: {} as never,
        updateProps: vi.fn(),
        selected: false,
        editable: true,
      } as never,
    })
    await flushPromises()
    expect(spy).toHaveBeenCalledTimes(1)
    first.unmount()

    const second = mount(RefBlockView, {
      props: {
        block,
        editor: {} as never,
        updateProps: vi.fn(),
        selected: false,
        editable: true,
      } as never,
    })
    await flushPromises()
    expect(spy).toHaveBeenCalledTimes(2)
    expect(second.text()).toContain('恢复后的分组标题')
    second.unmount()
  })

  test('ignores stale responses when referenced artifact version advances out of order', async () => {
    const id = 'art_race_case'
    type DetailRes = ReturnType<typeof boardDetail>
    let resolveV1!: (res: DetailRes) => void
    let resolveV2!: (res: DetailRes) => void
    const p1 = new Promise<DetailRes>((resolve) => {
      resolveV1 = resolve
    })
    const p2 = new Promise<DetailRes>((resolve) => {
      resolveV2 = resolve
    })

    state.artifacts = [
      {
        id,
        kind: 'whiteboard',
        title: '设计画板',
        nodeId: null,
        version: 1,
        updatedAt: 1000,
      } as ArtifactSummary,
    ]
    const spy = vi.spyOn(api, 'getArtifact').mockReturnValueOnce(p1).mockReturnValueOnce(p2)

    const block = {
      id: 'blk-2',
      type: 'ref',
      props: { artifactId: id, groupId: 'grp-1', optionId: '', label: '' },
      content: [],
      children: [],
    }
    const wrapper = mount(RefBlockView, {
      props: {
        block,
        editor: {} as never,
        updateProps: vi.fn(),
        selected: false,
        editable: true,
      } as never,
    })
    await flushPromises()
    expect(spy).toHaveBeenCalledTimes(1)

    // Advance referenced artifact to v2 before v1 finishes.
    state.artifacts = [
      {
        id,
        kind: 'whiteboard',
        title: '设计画板',
        nodeId: null,
        version: 2,
        updatedAt: 2000,
      } as ArtifactSummary,
    ]
    await flushPromises()
    expect(spy).toHaveBeenCalledTimes(2)

    // v2 resolves first, then stale v1 resolves later.
    resolveV2(boardDetail(id, 2, '新版分组 v2'))
    await flushPromises()
    expect(wrapper.text()).toContain('新版分组 v2')

    resolveV1(boardDetail(id, 1, '旧版分组 v1'))
    await flushPromises()
    expect(wrapper.text()).toContain('新版分组 v2')
    expect(wrapper.text()).not.toContain('旧版分组 v1')
    wrapper.unmount()
  })
})

describe('ArtifactFeedbackNotice inline ask answers', () => {
  test('renders answered inline questions when expanded', async () => {
    setLocale('zh-CN')
    const wrapper = mount(ArtifactFeedbackNotice, {
      props: {
        notice: {
          kind: 'artifact-feedback',
          id: 'ntc_1',
          conversationId: 'conv_1',
          artifactId: 'art_doc_1',
          title: '架构设计',
          round: 1,
          done: false,
          summary: null,
          items: [
            {
              kind: 'answer',
              answers: [
                {
                  id: 'ask-auth',
                  question: '登录方式选哪种？',
                  values: ['JWT'],
                  note: '无状态部署',
                },
              ],
            },
          ],
          createdAt: Date.now(),
        },
      },
    })
    await wrapper.get('.feedback-toggle').trigger('click')
    const items = wrapper.findAll('.feedback-items li')
    expect(items).toHaveLength(1)
    expect(items[0]?.text()).toContain('登录方式选哪种？')
    expect(items[0]?.text()).toContain('JWT')
    expect(items[0]?.text()).toContain('无状态部署')
    wrapper.unmount()
  })
})
