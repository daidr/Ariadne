// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { describe, expect, test } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import DiffVirtualList, { type DiffVirtualListExpose } from './DiffVirtualList.vue'

interface Row {
  path: string
  height: number
}

describe('DiffVirtualList', () => {
  test('renders items in normal flow with padding spacers instead of transforms so sticky headers work', async () => {
    const items: Row[] = Array.from({ length: 12 }, (_, i) => ({
      path: `src/file-${i}.ts`,
      height: 200,
    }))

    const listRef = ref<DiffVirtualListExpose | null>(null)
    const Host = defineComponent({
      setup() {
        return () =>
          h(
            DiffVirtualList<Row>,
            {
              ref: listRef,
              items,
              itemKey: 'path',
              estimateSize: (item: Row) => item.height,
              overscan: 1,
            },
            {
              default: ({ item }: { item: Row }) => h('div', { class: 'row' }, item.path),
            },
          )
      },
    })

    const wrapper = mount(Host)

    const container = wrapper.find('.diff-virtual-list').element as HTMLElement
    Object.defineProperty(container, 'clientHeight', { value: 400, configurable: true })

    // Initial render at scrollTop = 0: covers [0..1] + overscan 1 => [0..2]
    let rendered = wrapper.findAll('.diff-virtual-list-item')
    expect(rendered.length).toBeGreaterThan(0)
    expect(rendered.length).toBeLessThan(items.length)
    for (const node of rendered) {
      expect((node.element as HTMLElement).style.transform).toBe('')
    }

    // Jump to item 6 at start (6 * 200 = 1200px)
    listRef.value!.scrollToIndex(6, 'start')
    await nextTick()

    expect(container.scrollTop).toBe(1200)
    const content = wrapper.find('.diff-virtual-list-content').element as HTMLElement
    // overscan = 1 => from = 5 => topPadding = 5 * 200 = 1000px
    expect(content.style.paddingTop).toBe('1000px')
    rendered = wrapper.findAll('.diff-virtual-list-item')
    const indices = rendered.map((node) => Number(node.attributes('data-index')))
    expect(indices).toContain(5)
    expect(indices).toContain(6)

    wrapper.unmount()
  })
})
