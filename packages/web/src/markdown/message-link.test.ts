// @vitest-environment happy-dom
import type { ArtifactSummary, ThreadView } from '@ariadne/core'
import { mount } from '@vue/test-utils'
import { afterEach, expect, test, vi } from 'vitest'
import { h } from 'vue'
import type { ProjectDetail } from '../api'
import * as store from '../store'
import MessageLink from './MessageLink.vue'

afterEach(() => {
  store.state.currentId = null
  store.state.detail = null
  store.state.artifacts = []
  vi.restoreAllMocks()
})

test('renders thread, artifact and file references as inline anchors so long titles wrap with surrounding text', () => {
  store.state.currentId = 'prj_1'
  store.state.detail = {
    threads: [{ id: 'thr_1', title: 'Long thread title', stage: 'queued' } as ThreadView],
  } as ProjectDetail
  store.state.artifacts = [{ id: 'art_1', kind: 'document', title: 'Spec' } as ArtifactSummary]

  const threadWrapper = mount(MessageLink, {
    props: {
      href: 'thread:thr_1',
      external: false,
      bare: false,
      children: h('span', 'Long thread title'),
    },
  })
  const threadLink = threadWrapper.find('a.inline-ref')
  expect(threadLink.exists()).toBe(true)
  expect(threadWrapper.find('button.inline-ref').exists()).toBe(false)
  expect(threadLink.attributes('href')).toBe('/projects/prj_1?tab=thread%3Athr_1')

  const artifactWrapper = mount(MessageLink, {
    props: {
      href: 'artifact:art_1',
      external: false,
      bare: false,
      children: h('span', 'Spec'),
    },
  })
  const artifactLink = artifactWrapper.find('a.inline-ref')
  expect(artifactLink.exists()).toBe(true)
  expect(artifactLink.attributes('href')).toBe('/projects/prj_1?tab=artifact%3Aart_1')

  const fileWrapper = mount(MessageLink, {
    props: {
      href: 'file:fil_1',
      external: false,
      bare: false,
      children: h('span', 'notes.txt'),
    },
  })
  const fileLink = fileWrapper.find('a.inline-ref')
  expect(fileLink.exists()).toBe(true)
  expect(fileLink.attributes('href')).toBe('/api/files/fil_1')
})

test('intercepts plain and alt clicks for in-app navigation while allowing modifier new-tab clicks', async () => {
  store.state.currentId = 'prj_1'
  store.state.detail = {
    threads: [{ id: 'thr_1', title: 'Thread', stage: 'queued' } as ThreadView],
  } as ProjectDetail
  const openSpy = vi.spyOn(store, 'openThreadFrom').mockResolvedValue(undefined)

  const wrapper = mount(MessageLink, {
    props: {
      href: 'thread:thr_1',
      external: false,
      bare: false,
      children: h('span', 'Thread'),
    },
  })
  const anchor = wrapper.get('a.inline-ref')

  const plainEvent = new MouseEvent('click', { bubbles: true, cancelable: true })
  anchor.element.dispatchEvent(plainEvent)
  expect(plainEvent.defaultPrevented).toBe(true)
  expect(openSpy).toHaveBeenCalledTimes(1)

  const altEvent = new MouseEvent('click', { bubbles: true, cancelable: true, altKey: true })
  anchor.element.dispatchEvent(altEvent)
  expect(altEvent.defaultPrevented).toBe(true)
  expect(openSpy).toHaveBeenCalledTimes(2)

  const metaEvent = new MouseEvent('click', { bubbles: true, cancelable: true, metaKey: true })
  anchor.element.dispatchEvent(metaEvent)
  expect(metaEvent.defaultPrevented).toBe(false)
  expect(openSpy).toHaveBeenCalledTimes(2)
})
