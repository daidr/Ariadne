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

describe('sent comments', () => {
  test('follow the line they quote, and say so when none reads that way now', () => {
    const at = { messageId: 'm', sentAt: 0, path: 'a.ts' }
    const sent = [
      { ...at, id: 'm:0', line: 2, quote: 'const b = 2', body: '一' },
      { ...at, id: 'm:1', line: 3, quote: 'gone()', body: '二' },
      { ...at, id: 'm:2', line: 4, quote: 'other()', body: '三' },
      { ...at, id: 'm:3', path: 'b.ts', line: 1, quote: 'x', body: '四' },
    ]
    const { gutter, widgets } = useLineComments(
      () => 'thr_sent',
      () => 'a.ts',
      () => ({ original: '', modified: 'const a = 1\nadded()\nconst b = 2\nother()' }),
      () => sent,
    )
    expect(gutter.value.markers?.map((m) => [m.line, m.class])).toEqual([
      [3, 'line-comment-sent-mark'],
      [4, 'line-comment-sent-mark'],
    ])
    const moved = widgets.value.map((w) => {
      const vnode = w.render() as unknown as { props: { moved: boolean } }
      return [w.line, vnode.props.moved]
    })
    expect(moved).toEqual([
      [3, true],
      [4, false],
    ])
  })
})
