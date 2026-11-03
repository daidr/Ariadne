import { WHITEBOARD_NODE_TYPES } from '@ariadne/core'
import { expect, test } from 'vitest'
import { whiteboardExtensions } from './whiteboard-extensions'

test('every node the whiteboard editor can make passes the content check', () => {
  const types = whiteboardExtensions()
    .flat()
    .flatMap((extension) => extension.nodeTypes ?? [])
    .map((spec) => spec.type)
  expect(types.length).toBeGreaterThan(0)
  expect(types).not.toContain('group')
  for (const type of types) expect(WHITEBOARD_NODE_TYPES).toContain(type)
})
