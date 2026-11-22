// @vitest-environment happy-dom
import type { DockInstance } from 'dou-ui'
import { afterEach, expect, test, vi } from 'vitest'
import { dragArtifact, dragThread, sideDock } from './dock'

afterEach(() => {
  sideDock.value = null
  vi.restoreAllMocks()
})

function pointerDownOn(target: EventTarget, init: PointerEventInit = {}) {
  const event = new PointerEvent('pointerdown', { button: 0, pointerType: 'mouse', ...init })
  Object.defineProperty(event, 'currentTarget', { value: target })
  return event
}

test('dragThread and dragArtifact forward primary pointerdown to sideDock.startDrop', () => {
  const startDrop = vi.fn()
  sideDock.value = { startDrop } as unknown as DockInstance

  const threadEvent = pointerDownOn(document.createElement('a'))
  dragThread(threadEvent, 'thr_1')
  expect(startDrop).toHaveBeenCalledWith(threadEvent, { value: 'thread:thr_1' })

  const artifactEvent = pointerDownOn(document.createElement('button'))
  dragArtifact(artifactEvent, 'art_1')
  expect(startDrop).toHaveBeenCalledWith(artifactEvent, { value: 'artifact:art_1' })
})

test('ignores secondary buttons and modifier keys when starting dock drag', () => {
  const startDrop = vi.fn()
  sideDock.value = { startDrop } as unknown as DockInstance
  const el = document.createElement('a')

  for (const init of [
    { button: 2 },
    { button: 0, altKey: true },
    { button: 0, ctrlKey: true },
    { button: 0, metaKey: true },
    { button: 0, shiftKey: true },
  ]) {
    dragThread(pointerDownOn(el, init), 'thr_1')
    dragArtifact(pointerDownOn(el, init), 'art_1')
  }

  expect(startDrop).not.toHaveBeenCalled()
})

test('ignores rapid consecutive pointerdowns on the same target to preserve multi-click selection', () => {
  const startDrop = vi.fn()
  sideDock.value = { startDrop } as unknown as DockInstance
  const link = document.createElement('a')
  let now = 1000
  vi.spyOn(performance, 'now').mockImplementation(() => now)

  // First press starts listening for drag.
  dragThread(pointerDownOn(link, { clientX: 50, clientY: 20 }), 'thr_1')
  expect(startDrop).toHaveBeenCalledTimes(1)

  // Second and third presses within the double-click window at the same spot are ignored
  // even when browser pointerdown reports detail === 0.
  now += 180
  dragThread(pointerDownOn(link, { clientX: 52, clientY: 21 }), 'thr_1')
  now += 180
  dragThread(pointerDownOn(link, { clientX: 51, clientY: 20 }), 'thr_1')
  expect(startDrop).toHaveBeenCalledTimes(1)

  // Pressing again after the multi-click window allows dragging again.
  now += 500
  dragThread(pointerDownOn(link, { clientX: 51, clientY: 20 }), 'thr_1')
  expect(startDrop).toHaveBeenCalledTimes(2)

  // Pressing quickly at a distant position on the same element also allows dragging.
  now += 100
  dragThread(pointerDownOn(link, { clientX: 120, clientY: 20 }), 'thr_1')
  expect(startDrop).toHaveBeenCalledTimes(3)
})
