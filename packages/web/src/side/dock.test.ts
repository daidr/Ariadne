// @vitest-environment happy-dom
import { DouDock, type DockInstance } from 'dou-ui'
import { mount } from '@vue/test-utils'
import { afterEach, expect, test, vi } from 'vitest'
import { nextTick } from 'vue'
import { dragArtifact, dragThread, sideDock } from './dock'

afterEach(() => {
  sideDock.value = null
  document.querySelectorAll('.dou-tabs-ghost').forEach((el) => el.remove())
  document.documentElement.classList.remove('dou-tabs-dragging')
  vi.restoreAllMocks()
})

function pointerDownOn(target: EventTarget, init: PointerEventInit = {}) {
  const event = new PointerEvent('pointerdown', { button: 0, pointerType: 'mouse', ...init })
  Object.defineProperty(event, 'currentTarget', { value: target })
  return event
}

test('dragThread and dragArtifact forward pointerdown to sideDock.startDrop', () => {
  const startDrop = vi.fn()
  sideDock.value = { startDrop } as unknown as DockInstance

  const threadEvent = pointerDownOn(document.createElement('a'))
  dragThread(threadEvent, 'thr_1')
  expect(startDrop).toHaveBeenCalledWith(threadEvent, { value: 'thread:thr_1' })

  const artifactEvent = pointerDownOn(document.createElement('button'))
  dragArtifact(artifactEvent, 'art_1')
  expect(startDrop).toHaveBeenCalledWith(artifactEvent, { value: 'artifact:art_1' })
})

test('DouDock.startDrop natively ignores modifier keys, secondary buttons and rapid repeats', async () => {
  const wrapper = mount(DouDock, {
    props: {
      modelValue: { type: 'group', id: 'g1', tabs: ['plan'], active: 'plan' },
      panels: [
        { value: 'plan', label: 'Plan', pinned: true },
        { value: 'thread:thr_1', label: 'Thread One' },
      ],
    },
    attachTo: document.body,
  })
  const source = document.createElement('a')
  document.body.append(source)
  vi.spyOn(source, 'getBoundingClientRect').mockReturnValue(new DOMRect(100, 100, 120, 24))
  try {
    sideDock.value = wrapper.vm as unknown as DockInstance
    for (const init of [
      { button: 2 },
      { button: 0, altKey: true },
      { button: 0, ctrlKey: true },
      { button: 0, metaKey: true },
      { button: 0, shiftKey: true },
    ]) {
      dragThread(pointerDownOn(source, { clientX: 120, clientY: 110, ...init }), 'thr_1')
      window.dispatchEvent(new PointerEvent('pointermove', { clientX: 160, clientY: 110 }))
      await nextTick()
      expect(document.querySelector('.dou-tabs-ghost')).toBeNull()
    }

    let now = 1000
    vi.spyOn(Date, 'now').mockImplementation(() => now)

    dragThread(pointerDownOn(source, { clientX: 120, clientY: 110 }), 'thr_1')
    window.dispatchEvent(new PointerEvent('pointerup', { clientX: 120, clientY: 110 }))

    // Rapid second press at the same spot does not start a drag.
    now += 150
    dragThread(pointerDownOn(source, { clientX: 121, clientY: 110 }), 'thr_1')
    window.dispatchEvent(new PointerEvent('pointermove', { clientX: 180, clientY: 110 }))
    await nextTick()
    expect(document.querySelector('.dou-tabs-ghost')).toBeNull()
    window.dispatchEvent(new PointerEvent('pointerup', { clientX: 180, clientY: 110 }))

    // After the multi-click window, dragging works again.
    now += 500
    dragThread(pointerDownOn(source, { clientX: 121, clientY: 110 }), 'thr_1')
    window.dispatchEvent(new PointerEvent('pointermove', { clientX: 180, clientY: 110 }))
    await nextTick()
    expect(document.querySelector('.dou-tabs-ghost')?.textContent).toBe('Thread One')
    window.dispatchEvent(new PointerEvent('pointercancel'))
  } finally {
    source.remove()
    wrapper.unmount()
  }
})
