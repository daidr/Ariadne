import type { PlanOp } from '@ariadne/core'
import { describe, expect, test } from 'vitest'
import { duplicateTitles } from './plan-tools'

describe('duplicateTitles', () => {
  test('a node added under a title the plan has, or twice in one change, is a duplicate', () => {
    expect(
      duplicateTitles(
        ['修复 Safari 登录'],
        ['修复 safari 登录。', '加导出', '加 导出', '写文档'].map((title, i): PlanOp => ({
          op: 'add',
          id: `n${i}`,
          title,
          kind: 'task',
          description: '',
          acceptance: '',
        })),
      ),
    ).toEqual(['修复 safari 登录。', '加 导出'])
  })
})
