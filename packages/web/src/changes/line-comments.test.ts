// @vitest-environment happy-dom
import { DouCodeDiff } from '@dou-ui/code'
import { mount } from '@vue/test-utils'
import { describe, expect, test } from 'vitest'
import { h, nextTick } from 'vue'
import { useLineComments } from './line-comments'

describe('line comments in the diff', () => {
  test('the plus on every line leaves unchanged lines folded', async () => {
    const original = Array.from({ length: 200 }, (_, i) => `line ${i + 1}`).join('\n')
    const modified = original.replace('line 100', 'line 100 changed')
    const { gutter } = useLineComments(
      () => 't1',
      () => 'a.ts',
      () => ({ original, modified }),
    )
    const wrapper = mount(() =>
      h(DouCodeDiff, { original, modelValue: modified, gutters: [gutter.value] }),
    )
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(wrapper.findAll('.dou-code-diff__bar').length).toBeGreaterThan(0)
    wrapper.unmount()
  })
})
