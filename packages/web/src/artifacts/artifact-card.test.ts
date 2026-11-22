// @vitest-environment happy-dom
import type { ArtifactSummary } from '@ariadne/core'
import type { DockInstance } from 'dou-ui'
import { mount } from '@vue/test-utils'
import { afterEach, expect, test, vi } from 'vitest'
import { sideDock } from '../side/dock'
import ArtifactCard from './ArtifactCard.vue'

afterEach(() => {
  sideDock.value = null
})

const artifact = {
  id: 'art_1',
  kind: 'document',
  title: 'Architecture Spec',
  nodeId: null,
  version: 1,
  updatedAt: Date.now(),
  preview: { kind: 'document', lead: '# Spec', excerpt: 'Spec' },
} as ArtifactSummary

test('starts a dock drag on primary pointerdown without interfering with clicks', async () => {
  const startDrop = vi.fn()
  sideDock.value = { startDrop } as unknown as DockInstance
  const onClick = vi.fn()

  const wrapper = mount(ArtifactCard, {
    props: { artifact },
    attrs: { onClick },
  })
  const card = wrapper.get('button.artifact-card')

  await card.trigger('pointerdown', { button: 2 })
  await card.trigger('pointerdown', { button: 0, altKey: true })
  expect(startDrop).not.toHaveBeenCalled()

  await card.trigger('pointerdown', { button: 0 })
  expect(startDrop).toHaveBeenCalledTimes(1)
  expect(startDrop.mock.calls[0]?.[1]).toEqual({ value: 'artifact:art_1' })

  await card.trigger('click')
  expect(onClick).toHaveBeenCalledTimes(1)
  wrapper.unmount()
})
