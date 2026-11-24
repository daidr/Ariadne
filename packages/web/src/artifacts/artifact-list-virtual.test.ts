// @vitest-environment happy-dom
import type { ArtifactSummary } from '@ariadne/core'
import { flushPromises, mount } from '@vue/test-utils'
import { DouConfigProvider } from 'dou-ui'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { api } from '../api'
import { douLocale, locale } from '../i18n'
import { state } from '../store'
import ArtifactList from './ArtifactList.vue'
import ArtifactsOverview from './ArtifactsOverview.vue'
import ArtifactThumb from './ArtifactThumb.vue'
import { clearBoardThumbnails } from './board-thumbnail'
import { ARTIFACT_VIRTUALIZE_THRESHOLD, chunkArtifactRows, gridColumnCount } from './card-grid'

vi.mock('@dou-ui/flow', () => ({
  renderFlowSVG: vi.fn(async () => '<svg viewBox="0 0 10 10"></svg>'),
}))
vi.mock('@dou-ui/flow/style.css', () => ({}))
vi.mock('./whiteboard-extensions', () => ({ whiteboardExtensions: () => [] }))

function makeDocArtifact(index: number): ArtifactSummary {
  return {
    id: `art_${index}`,
    kind: 'document',
    title: `Spec ${index}`,
    nodeId: null,
    version: 1,
    updatedAt: Date.now() - index * 1000,
    preview: { kind: 'document', lead: `# Spec ${index}`, excerpt: `Spec ${index}` },
  } as ArtifactSummary
}

function makeBoardArtifact(id: string): ArtifactSummary {
  return {
    id,
    kind: 'whiteboard',
    title: `Board ${id}`,
    nodeId: null,
    version: 1,
    updatedAt: 1_700_000_000_000,
    preview: { kind: 'whiteboard', items: 3, texts: ['Node A'] },
  } as ArtifactSummary
}

class FakeIntersectionObserver {
  static instances: FakeIntersectionObserver[] = []
  readonly callback: IntersectionObserverCallback
  readonly observed = new Set<Element>()
  disconnected = false

  constructor(callback: IntersectionObserverCallback) {
    this.callback = callback
    FakeIntersectionObserver.instances.push(this)
  }

  observe(el: Element) {
    this.observed.add(el)
  }

  unobserve(el: Element) {
    this.observed.delete(el)
  }

  disconnect() {
    this.disconnected = true
    this.observed.clear()
  }

  trigger(el: Element, isIntersecting: boolean) {
    this.callback([{ target: el, isIntersecting } as IntersectionObserverEntry], this as never)
  }
}

describe('card-grid helpers', () => {
  test('computes responsive column count matching CSS auto-fill minmax', () => {
    expect(gridColumnCount(0, 220, 12)).toBe(1)
    expect(gridColumnCount(200, 220, 12)).toBe(1)
    expect(gridColumnCount(452, 220, 12)).toBe(2)
    expect(gridColumnCount(684, 220, 12)).toBe(3)
  })

  test('chunks artifacts into rows keyed by their first card', () => {
    const items = [
      makeDocArtifact(1),
      makeDocArtifact(2),
      makeDocArtifact(3),
      makeDocArtifact(4),
      makeDocArtifact(5),
    ]
    const rows = chunkArtifactRows(items, 2)
    expect(rows.map((row) => [row.key, row.items.length])).toEqual([
      ['art_1', 2],
      ['art_3', 2],
      ['art_5', 1],
    ])
  })
})

describe('ArtifactList & ArtifactsOverview virtualization', () => {
  beforeEach(() => {
    locale.value = 'zh-CN'
    state.currentId = 'prj_1'
    state.artifactsLoaded = true
    state.artifacts = []
    state.plan = null
  })

  afterEach(() => {
    state.artifacts = []
    vi.restoreAllMocks()
  })

  test('ArtifactList uses plain grid up to threshold and DouVirtualList above threshold', async () => {
    state.artifacts = Array.from({ length: ARTIFACT_VIRTUALIZE_THRESHOLD }, (_, i) =>
      makeDocArtifact(i),
    )

    const Host = defineComponent({
      setup() {
        return () => h(DouConfigProvider, { locale: douLocale.value }, () => h(ArtifactList))
      },
    })
    const wrapper = mount(Host, { attachTo: document.body })
    expect(wrapper.find('.artifact-cards').exists()).toBe(true)
    expect(wrapper.find('.dou-virtual-list').exists()).toBe(false)
    expect(wrapper.findAll('button.artifact-card')).toHaveLength(ARTIFACT_VIRTUALIZE_THRESHOLD)

    state.artifacts = Array.from({ length: 65 }, (_, i) => makeDocArtifact(i))
    await nextTick()

    expect(wrapper.find('.dou-virtual-list').exists()).toBe(true)
    expect(wrapper.find('.artifact-cards').exists()).toBe(false)
    // Only visible rows + overscan are mounted instead of all 65 cards.
    const mountedCards = wrapper.findAll('button.artifact-card').length
    expect(mountedCards).toBeGreaterThan(0)
    expect(mountedCards).toBeLessThan(65)
    wrapper.unmount()
  })

  test('ArtifactsOverview virtualizes long lists and switches back when filtered', async () => {
    state.artifacts = Array.from({ length: 55 }, (_, i) => makeDocArtifact(i))

    const Host = defineComponent({
      setup() {
        return () => h(DouConfigProvider, { locale: douLocale.value }, () => h(ArtifactsOverview))
      },
    })
    const wrapper = mount(Host, { attachTo: document.body })
    expect(wrapper.find('.dou-virtual-list').exists()).toBe(true)
    expect(wrapper.findAll('button.artifact-card').length).toBeLessThan(55)

    // Filter down via search query to 1 item -> switches to plain grid.
    const input = wrapper.find('.overview-search input')
    await input.setValue('Spec 42')
    await nextTick()
    expect(wrapper.find('.overview-grid').exists()).toBe(true)
    expect(wrapper.findAll('button.artifact-card')).toHaveLength(1)

    // Filter to 0 matches -> shows empty state.
    await input.setValue('does-not-exist')
    await nextTick()
    expect(wrapper.find('.overview-empty').exists()).toBe(true)
    wrapper.unmount()
  })
})

describe('ArtifactThumb IntersectionObserver lazy rendering', () => {
  const originalIO = globalThis.IntersectionObserver

  beforeEach(() => {
    clearBoardThumbnails()
    FakeIntersectionObserver.instances = []
    globalThis.IntersectionObserver =
      FakeIntersectionObserver as unknown as typeof IntersectionObserver
  })

  afterEach(() => {
    clearBoardThumbnails()
    globalThis.IntersectionObserver = originalIO
    vi.restoreAllMocks()
  })

  test('defers whiteboard thumbnail rendering until visible and shares one observer', async () => {
    const getArtifact = vi.spyOn(api, 'getArtifact').mockResolvedValue({
      artifact: {
        content: JSON.stringify({
          version: 1,
          nodes: [{ id: 'n1', type: 'rect', x: 0, y: 0 }],
          edges: [],
        }),
      },
    } as Awaited<ReturnType<typeof api.getArtifact>>)

    const first = mount(ArtifactThumb, { props: { artifact: makeBoardArtifact('wb_1') } })
    const second = mount(ArtifactThumb, { props: { artifact: makeBoardArtifact('wb_2') } })
    await flushPromises()

    // Neither offscreen whiteboard has fetched its content yet, and both share 1 observer.
    expect(FakeIntersectionObserver.instances).toHaveLength(1)
    expect(getArtifact).not.toHaveBeenCalled()
    expect(first.find('img.thumb-board').exists()).toBe(false)

    // Reveal the first whiteboard card only.
    const observer = FakeIntersectionObserver.instances[0]!
    observer.trigger(first.element, true)
    await flushPromises()

    expect(getArtifact).toHaveBeenCalledTimes(1)
    expect(getArtifact).toHaveBeenCalledWith('wb_1', expect.objectContaining({ versions: false }))
    expect(first.find('img.thumb-board').exists()).toBe(true)
    expect(second.find('img.thumb-board').exists()).toBe(false)

    first.unmount()
    second.unmount()
    expect(observer.disconnected).toBe(true)
  })

  test('does not eagerly fetch offscreen whiteboard after items clear then refill', async () => {
    const getArtifact = vi.spyOn(api, 'getArtifact').mockResolvedValue({
      artifact: {
        content: JSON.stringify({
          version: 1,
          nodes: [{ id: 'n1', type: 'rect', x: 0, y: 0 }],
          edges: [],
        }),
      },
    } as Awaited<ReturnType<typeof api.getArtifact>>)

    const initial = makeBoardArtifact('wb_clear')
    const wrapper = mount(ArtifactThumb, { props: { artifact: initial } })
    await flushPromises()

    const firstObserver = FakeIntersectionObserver.instances[0]!
    firstObserver.trigger(wrapper.element, true)
    await flushPromises()
    expect(getArtifact).toHaveBeenCalledTimes(1)
    expect(wrapper.find('img.thumb-board').exists()).toBe(true)

    // Board becomes empty (items: 0) -> resets visibility and drops thumbnail.
    await wrapper.setProps({
      artifact: {
        ...initial,
        updatedAt: initial.updatedAt + 1_000,
        preview: { kind: 'whiteboard', items: 0, connectors: 0, texts: [] },
      },
    })
    await flushPromises()
    expect(wrapper.find('img.thumb-board').exists()).toBe(false)

    // Board regains items while offscreen -> must wait for IntersectionObserver again.
    await wrapper.setProps({
      artifact: {
        ...initial,
        updatedAt: initial.updatedAt + 2_000,
        preview: { kind: 'whiteboard', items: 3, connectors: 1, texts: ['A'] },
      },
    })
    await flushPromises()
    expect(getArtifact).toHaveBeenCalledTimes(1)

    const activeObserver = FakeIntersectionObserver.instances.at(-1)!
    activeObserver.trigger(wrapper.element, true)
    await flushPromises()
    expect(getArtifact).toHaveBeenCalledTimes(2)
    expect(wrapper.find('img.thumb-board').exists()).toBe(true)

    wrapper.unmount()
  })

  test('re-checks visibility instead of eagerly fetching when ArtifactThumb switches artifact id', async () => {
    const getArtifact = vi.spyOn(api, 'getArtifact').mockResolvedValue({
      artifact: {
        content: JSON.stringify({
          version: 1,
          nodes: [{ id: 'n1', type: 'rect', x: 0, y: 0 }],
          edges: [],
        }),
      },
    } as Awaited<ReturnType<typeof api.getArtifact>>)

    const wrapper = mount(ArtifactThumb, { props: { artifact: makeBoardArtifact('wb_a') } })
    await flushPromises()

    FakeIntersectionObserver.instances[0]!.trigger(wrapper.element, true)
    await flushPromises()
    expect(getArtifact).toHaveBeenCalledTimes(1)
    expect(getArtifact).toHaveBeenLastCalledWith(
      'wb_a',
      expect.objectContaining({ versions: false }),
    )

    // Switch the same component instance to a different whiteboard ID while offscreen.
    await wrapper.setProps({ artifact: makeBoardArtifact('wb_b') })
    await flushPromises()
    expect(getArtifact).toHaveBeenCalledTimes(1)
    expect(wrapper.find('img.thumb-board').exists()).toBe(false)

    // Only once the new element is reported visible should wb_b be fetched.
    FakeIntersectionObserver.instances.at(-1)!.trigger(wrapper.element, true)
    await flushPromises()
    expect(getArtifact).toHaveBeenCalledTimes(2)
    expect(getArtifact).toHaveBeenLastCalledWith(
      'wb_b',
      expect.objectContaining({ versions: false }),
    )
    expect(wrapper.find('img.thumb-board').exists()).toBe(true)

    wrapper.unmount()
  })
})
