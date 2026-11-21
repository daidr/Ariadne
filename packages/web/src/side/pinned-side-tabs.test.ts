// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { expect, test, vi } from 'vitest'
import { h, nextTick } from 'vue'
import { MessagesIcon, PackageIcon, WorkflowIcon } from '@dou-ui/icons'
import PinnedSideTabs from './PinnedSideTabs.vue'

test('renders the fixed tabs before the scrolling list and supports selection and ordering', async () => {
  const select = vi.fn()
  const reorder = vi.fn()
  const wrapper = mount(
    {
      render: () =>
        h('div', { class: 'dou-tabs__header' }, [
          h('div', { class: 'dou-tabs__list' }, [
            h('button', { 'aria-controls': 'panel-plan' }, [
              h('span', { 'data-pinned-label': 'plan' }, 'Plan'),
            ]),
            h('button', 'Scrollable tab'),
          ]),
          h('div', { class: 'dou-tabs__extra' }, [
            h(PinnedSideTabs, {
              active: 'plan',
              dragAcrossGroups: false,
              tabs: [
                { key: 'plan', label: 'Plan', icon: WorkflowIcon },
                { key: 'threads', label: 'Threads', icon: MessagesIcon },
                { key: 'artifacts', label: 'Artifacts', icon: PackageIcon },
              ],
              onSelect: select,
              onReorder: reorder,
            }),
          ]),
        ]),
    },
    { attachTo: document.body },
  )
  try {
    await nextTick()
    await nextTick()
    expect(wrapper.element.firstElementChild?.className).toBe('side-pinned-host')
    expect(wrapper.get('[role="tab"][aria-label="Plan"]').attributes('aria-controls')).toBe(
      'panel-plan',
    )
    const threads = wrapper.get('[role="tab"][aria-label="Threads"]')
    await threads.trigger('click')
    expect(select).toHaveBeenCalledWith('threads')
    await threads.trigger('keydown', { altKey: true, key: 'ArrowLeft' })
    expect(reorder).toHaveBeenCalledWith('threads', 0)

    const bar = wrapper.get<HTMLElement>('.side-pinned-tabs')
    vi.spyOn(bar.element, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 120, 36))
    bar.findAll('button').forEach((button, index) => {
      vi.spyOn(button.element, 'getBoundingClientRect').mockReturnValue(
        new DOMRect(index * 40, 0, 40, 36),
      )
    })
    reorder.mockClear()
    await threads.trigger('pointerdown', { pointerId: 7, button: 0, clientX: 60, clientY: 18 })
    window.dispatchEvent(
      new PointerEvent('pointermove', { pointerId: 7, clientX: 10, clientY: 18 }),
    )
    await nextTick()
    expect(wrapper.get('[aria-label="Plan"]').classes()).toContain('drop-before')
    window.dispatchEvent(new PointerEvent('pointerup', { pointerId: 7, clientX: 10, clientY: 18 }))
    await nextTick()
    expect(reorder).toHaveBeenCalledWith('threads', 0)
  } finally {
    wrapper.unmount()
  }
})
