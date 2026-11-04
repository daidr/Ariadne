import { describe, expect, test } from 'vitest'
import { checkHeadline, ToolInputError } from './tools'

describe('report headline', () => {
  test('keeps a plain line for the user', () => {
    expect(checkHeadline('  架构文档写好了，请审阅  ')).toBe('架构文档写好了，请审阅')
    expect(checkHeadline('改进建议写好了，两个 P0 需要你定')).toBe(
      '改进建议写好了，两个 P0 需要你定',
    )
    expect(checkHeadline('CI/CD 配好了，README.md 也更新了')).toBe(
      'CI/CD 配好了，README.md 也更新了',
    )
  })

  test('sends ids, paths, line numbers and long text back to the model', () => {
    const refused = [
      '',
      '文档 art_01a1a64e-32e7-7635-9424-5387f6ba93c1 已交付',
      '挂在 nod_01a1a64aa887773d 上',
      '修好了 router.go:97',
      '改了 packages/web/src/App.vue',
      '修改了packages/web/src/App.vue',
      '“packages/web/src/App.vue”已改',
      '改了src/App.vue',
      '提交 4ef0347 已推送',
      '第一行\n第二行',
      '很长'.repeat(31),
    ]
    for (const headline of refused) {
      expect(() => checkHeadline(headline), headline).toThrow(ToolInputError)
    }
  })
})
