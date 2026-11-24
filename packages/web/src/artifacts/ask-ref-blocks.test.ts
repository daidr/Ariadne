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
  readSharedDocumentAsks,
  summarizeDocumentAsks,
} from './document-asks'
import { fallbackAskId } from './document-block-data'
import RefBlockView from './RefBlockView.vue'
import { refDocumentBlock } from './ref-block'

const extensions = [editorKit(), askDocumentBlock, refDocumentBlock]

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
    })
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

describe('ArtifactFeedbackNotice empty verify verdicts', () => {
  test('renders cleared-verdicts message when verify item has empty verdicts', async () => {
    setLocale('zh-CN')
    const wrapper = mount(ArtifactFeedbackNotice, {
      props: {
        notice: {
          kind: 'artifact-feedback',
          id: 'ntc_1',
          conversationId: 'conv_1',
          artifactId: 'art_board_1',
          title: '视觉验收',
          round: 1,
          done: false,
          summary: null,
          items: [{ kind: 'verify', verdicts: [] }],
          createdAt: Date.now(),
        },
      },
    })
    await wrapper.get('.feedback-toggle').trigger('click')
    const items = wrapper.findAll('.feedback-items li')
    expect(items).toHaveLength(1)
    expect(items[0]?.text()).toContain('清空了验收结论')
    wrapper.unmount()
  })
})
