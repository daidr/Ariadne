// @vitest-environment happy-dom
import { editorKit } from '@dou-ui/editor'
import { blocksToMarkdown, markdownToBlocks } from '@dou-ui/editor/markdown'
import { describe, expect, test } from 'vitest'
import * as Y from 'yjs'
import { askDocumentBlock, normalizeAskProps } from './ask-block'
import {
  applyRecommendedDocumentAsks,
  readSharedDocumentAsks,
  summarizeDocumentAsks,
} from './document-asks'
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
})
