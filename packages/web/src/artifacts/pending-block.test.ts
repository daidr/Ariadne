// @vitest-environment happy-dom
import { DASHBOARD_WIDGET_TYPES, PENDING, pendingBlock, WHITEBOARD_NODE_TYPES } from '@ariadne/core'
import { blockElement, defineEditorExtension, editorKit, textBlock } from '@dou-ui/editor'
import { blocksToMarkdown, markdownToBlocks } from '@dou-ui/editor/markdown'
import { expect, test } from 'vitest'
import { pendingDocumentBlock } from './pending-block'
import { pendingNode } from './pending-node'
import { pendingWidget } from './pending-widget'

const TEXT = [
  '# 方案',
  '<pending>三个问题：花了多少、花在哪、**差在哪**</pending>',
  '一段正文，后面<pending>跟在段落里的占位</pending>',
  '<pending>第一个</pending>\n<pending>紧挨着的第二个</pending>',
].join('\n\n')

/** Blocks without their ids, which each reading makes anew. */
const plain = (blocks: unknown) =>
  JSON.parse(JSON.stringify(blocks, (key, value) => (key === 'id' ? undefined : value)))

test('the page reads and writes a placeholder block as the server does', () => {
  const server = [editorKit(), pendingBlock({ defineEditorExtension, textBlock, blockElement })]
  const page = [editorKit(), pendingDocumentBlock]
  const onServer = markdownToBlocks(TEXT, server)
  const onPage = markdownToBlocks(TEXT, page)
  expect(plain(onPage)).toEqual(plain(onServer))
  expect(onPage.filter((block) => block.type === PENDING)).toHaveLength(4)
  expect(blocksToMarkdown(onServer as never, page)).toBe(
    blocksToMarkdown(onServer as never, server),
  )
})

test('the placeholder node and widget carry the type the content check knows', () => {
  expect(pendingNode.type).toBe(PENDING)
  expect(pendingWidget().type).toBe(PENDING)
  expect(WHITEBOARD_NODE_TYPES).toContain(pendingNode.type)
  expect(DASHBOARD_WIDGET_TYPES).toContain(pendingWidget().type)
})
