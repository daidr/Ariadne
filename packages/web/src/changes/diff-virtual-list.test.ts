// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import DiffVirtualList, { type DiffVirtualListExpose } from './DiffVirtualList.vue'

interface Row {
  path: string
  height: number
}

const sfcSource = readFileSync(resolve(import.meta.dirname, 'DiffVirtualList.vue'), 'utf8')
const styleBlock = sfcSource.match(/<style[^>]*>([\s\S]*?)<\/style>/)?.[1] ?? ''

describe('DiffVirtualList', () => {
  test('disables native scroll anchoring to avoid double compensation with manual scroll shifts', () => {
    expect(styleBlock).toMatch(/\.diff-virtual-list\s*\{[^}]*overflow-anchor:\s*none;/s)
  })

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

  test('ignores stale ResizeObserver callbacks after items shrink or detach', async () => {
    const callbacks: ResizeObserverCallback[] = []
    class FakeResizeObserver {
      callback: ResizeObserverCallback
      constructor(callback: ResizeObserverCallback) {
        this.callback = callback
        callbacks.push(callback)
      }
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    vi.stubGlobal('ResizeObserver', FakeResizeObserver)

    try {
      const items = ref<Row[]>([
        { path: 'a.ts', height: 200 },
        { path: 'b.ts', height: 200 },
        { path: 'c.ts', height: 200 },
      ])
      const Host = defineComponent({
        setup() {
          return () =>
            h(
              DiffVirtualList<Row>,
              {
                items: items.value,
                itemKey: 'path',
                estimateSize: (item: Row) => item.height,
                overscan: 2,
              },
              {
                default: ({ item }: { item: Row }) => h('div', { class: 'row' }, item.path),
              },
            )
        },
      })

      const wrapper = mount(Host, { attachTo: document.body })
      const staleNode = wrapper.findAll('.diff-virtual-list-item')[2]!.element as HTMLElement
      Object.defineProperty(staleNode, 'offsetHeight', { value: 320, configurable: true })

      // Shrink list so index 2 is now out of bounds and detached.
      items.value = [{ path: 'a.ts', height: 200 }]
      await nextTick()

      expect(() => {
        for (const cb of callbacks) {
          cb([{ target: staleNode } as unknown as ResizeObserverEntry], {} as ResizeObserver)
        }
      }).not.toThrow()

      wrapper.unmount()
    } finally {
      vi.unstubAllGlobals()
    }
  })
})
