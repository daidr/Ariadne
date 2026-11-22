// @vitest-environment happy-dom
import type { DockInstance } from 'dou-ui'
import { afterEach, expect, test, vi } from 'vitest'
import { dragArtifact, dragThread, sideDock } from './dock'

afterEach(() => {
  sideDock.value = null
})

test('dragThread and dragArtifact forward primary pointerdown to sideDock.startDrop', () => {
  const startDrop = vi.fn()
  sideDock.value = { startDrop } as unknown as DockInstance

  const threadEvent = new PointerEvent('pointerdown', { button: 0 })
  dragThread(threadEvent, 'thr_1')
  expect(startDrop).toHaveBeenCalledWith(threadEvent, { value: 'thread:thr_1' })

  const artifactEvent = new PointerEvent('pointerdown', { button: 0 })
  dragArtifact(artifactEvent, 'art_1')
  expect(startDrop).toHaveBeenCalledWith(artifactEvent, { value: 'artifact:art_1' })
})

test('ignores secondary buttons, modifier keys and multi-clicks when starting dock drag', () => {
  const startDrop = vi.fn()
  sideDock.value = { startDrop } as unknown as DockInstance

  for (const init of [
    { button: 2 },
    { button: 0, altKey: true },
    { button: 0, ctrlKey: true },
    { button: 0, metaKey: true },
    { button: 0, shiftKey: true },
    { button: 0, detail: 2 },
  ]) {
    dragThread(new PointerEvent('pointerdown', init), 'thr_1')
    dragArtifact(new PointerEvent('pointerdown', init), 'art_1')
  }

  expect(startDrop).not.toHaveBeenCalled()
})
