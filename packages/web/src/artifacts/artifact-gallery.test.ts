// @vitest-environment happy-dom
import type { GalleryArtifactSummary, GalleryCounts } from '@ariadne/core'
import {
  StoredEventSchema,
  StreamEventsResponseSchema,
  StreamReadySchema,
  type StreamEventsResponse,
} from '@ariadne/proto'
import { create } from '@bufbuild/protobuf'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { DouConfigProvider } from 'dou-ui'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { api } from '../api'
import { previewFile } from '../files/preview'
import { douLocale, locale } from '../i18n'
import * as store from '../store'
import ArtifactGallery from './ArtifactGallery.vue'
import ArtifactThumb from './ArtifactThumb.vue'
import { clearBoardThumbnails } from './board-thumbnail'
import { formatGalleryUpdater, groupGalleryItems, resolveGalleryTimeBucket } from './gallery-groups'
import { galleryState, resetGalleryStateForTests } from './gallery-state'

vi.mock('@dou-ui/flow', () => ({
  renderFlowSVG: vi.fn(
    async (board: { nodes?: { data?: { label?: string } }[] }) =>
      `<svg data-label="${board.nodes?.[0]?.data?.label ?? ''}"></svg>`,
  ),
}))
vi.mock('@dou-ui/flow/style.css', () => ({}))
vi.mock('./whiteboard-extensions', () => ({ whiteboardExtensions: () => [] }))

function makeBoardJson(label: string) {
  return JSON.stringify({
    version: 1,
    nodes: [
      {
        id: 'n1',
        type: 'shape',
        position: { x: 0, y: 0 },
        width: 140,
        height: 60,
        data: { shape: 'rectangle', label },
      },
    ],
    edges: [],
  })
}

function makeItem(
  id: string,
  overrides: Partial<{
    projectId: string
    projectName: string
    kind: 'document' | 'whiteboard' | 'dashboard'
    title: string
    updatedAt: number
    hasPrototype: boolean
    pendingActions: number
    pendingFeedback: number
    openComments: number
    previewImage: string | null
    updatedBy: string
    updatedByLabel: string | null
    updatedByCoordinator: boolean
    nodeTitle: string | null
  }> = {},
): GalleryArtifactSummary {
  const kind = overrides.kind ?? 'document'
  const hasPrototype = overrides.hasPrototype ?? false
  const preview =
    kind === 'document'
      ? { kind: 'document' as const, excerpt: '文档摘要正文', lead: '# 标题\n\n文档摘要正文' }
      : kind === 'whiteboard'
        ? {
            kind: 'whiteboard' as const,
            items: 5,
            connectors: 2,
            texts: ['桌面端原型', '方案 A'],
            prototypeCount: hasPrototype ? 1 : 0,
            optionCount: hasPrototype ? 2 : 0,
            previewImage: overrides.previewImage ?? null,
            minimap: [
              { x: 0.05, y: 0.05, w: 0.4, h: 0.35, kind: hasPrototype ? 'prototype' : 'shape' },
              { x: 0.55, y: 0.1, w: 0.35, h: 0.3, kind: 'sticky' },
            ],
          }
        : {
            kind: 'dashboard' as const,
            widgets: 4,
            takenAt: 1800000000000,
            titles: ['P95 延迟', '错误率'],
            tiles: [{ type: 'stat', x: 0, y: 0, w: 6, h: 2, title: 'P95 延迟' }],
            isLive: true,
          }
  return {
    artifact: {
      id,
      projectId: overrides.projectId ?? 'prj_a',
      kind,
      title: overrides.title ?? `产物 ${id}`,
      nodeId: overrides.nodeTitle ? 'node_1' : null,
      version: 3,
      createdBy: 'user',
      updatedBy: overrides.updatedBy ?? 'user',
      createdAt: (overrides.updatedAt ?? 1800000000000) - 60_000,
      updatedAt: overrides.updatedAt ?? 1800000000000,
      preview,
      openComments: overrides.openComments ?? 0,
      commentedAt: 0,
    },
    projectName: overrides.projectName ?? 'Ariadne 主工程',
    updatedByLabel: overrides.updatedByLabel ?? null,
    updatedByCoordinator: overrides.updatedByCoordinator ?? false,
    hasPrototype,
    pendingActions: overrides.pendingActions ?? 0,
    pendingFeedback: overrides.pendingFeedback ?? 0,
    nodeTitle: overrides.nodeTitle ?? null,
  }
}

const sampleCounts: GalleryCounts = {
  total: 3,
  documents: 1,
  whiteboards: 1,
  dashboards: 1,
  prototypes: 1,
  needsMe: 1,
  feedbackPending: 1,
  projects: [
    { projectId: 'prj_a', projectName: 'Ariadne 主工程', count: 2 },
    { projectId: 'prj_b', projectName: 'DouUI 组件库', count: 1 },
  ],
}

const wrappers: VueWrapper[] = []

function mountGallery(layoutMode: 'wide' | 'medium' | 'narrow' = 'wide') {
  const Host = defineComponent({
    props: {
      layoutMode: {
        type: String as () => 'wide' | 'medium' | 'narrow',
        default: layoutMode,
      },
    },
    setup(props) {
      return () =>
        h(DouConfigProvider, { locale: douLocale.value }, () =>
          h(ArtifactGallery, { layoutMode: props.layoutMode }),
        )
    },
  })
  const wrapper = mount(Host, { attachTo: document.body })
  wrappers.push(wrapper)
  return wrapper
}

describe('gallery-groups helpers', () => {
  test('buckets timestamps into today, thisWeek, and earlier', () => {
    const now = new Date(2026, 10, 26, 15, 0, 0).getTime()
    const todayTs = new Date(2026, 10, 26, 9, 30, 0).getTime()
    const weekTs = new Date(2026, 10, 23, 12, 0, 0).getTime()
    const earlierTs = new Date(2026, 10, 10, 12, 0, 0).getTime()
    expect(resolveGalleryTimeBucket(todayTs, now)).toBe('today')
    expect(resolveGalleryTimeBucket(weekTs, now)).toBe('thisWeek')
    expect(resolveGalleryTimeBucket(earlierTs, now)).toBe('earlier')

    const items = [
      makeItem('a', { updatedAt: todayTs, projectId: 'prj_a', projectName: '主工程' }),
      makeItem('b', { updatedAt: weekTs, projectId: 'prj_b', projectName: '组件库' }),
      makeItem('c', { updatedAt: earlierTs, projectId: 'prj_a', projectName: '主工程' }),
    ]
    const byTime = groupGalleryItems(items, 'time', now)
    expect(byTime.map((s) => s.timeBucket)).toEqual(['today', 'thisWeek', 'earlier'])

    const byProject = groupGalleryItems(items, 'project', now)
    expect(byProject.map((s) => [s.projectId, s.items.length])).toEqual([
      ['prj_a', 2],
      ['prj_b', 1],
    ])
  })

  test('formats updater label across thread title, coordinator, and user', () => {
    const labels = { coordinator: '协调器', user: '你' }
    expect(formatGalleryUpdater(makeItem('1', { updatedByLabel: '设计讨论串' }), labels)).toBe(
      '设计讨论串',
    )
    expect(formatGalleryUpdater(makeItem('2', { updatedByCoordinator: true }), labels)).toBe(
      '协调器',
    )
    expect(formatGalleryUpdater(makeItem('3', { updatedBy: 'user' }), labels)).toBe('你')
  })
})

describe('ArtifactThumb prototype cover and minimap', () => {
  afterEach(() => {
    while (wrappers.length) wrappers.pop()!.unmount()
    clearBoardThumbnails()
    vi.restoreAllMocks()
  })

  test('renders prototype preview image directly without fetching full board content', async () => {
    const getArtifactSpy = vi.spyOn(api, 'getArtifact')
    const item = makeItem('proto', {
      kind: 'whiteboard',
      hasPrototype: true,
      previewImage: '/api/files/fil_cover_demo',
    })
    const wrapper = mount(ArtifactThumb, { props: { artifact: item.artifact } })
    wrappers.push(wrapper)
    await flushPromises()
    const img = wrapper.find('img.thumb-cover')
    expect(img.exists()).toBe(true)
    expect(img.attributes('src')).toBe('/api/files/fil_cover_demo')
    expect(getArtifactSpy).not.toHaveBeenCalled()
  })

  test('renders minimap boxes immediately for whiteboards without a cover image', async () => {
    vi.spyOn(api, 'getArtifact').mockImplementation(() => new Promise(() => {}))
    const item = makeItem('board', {
      kind: 'whiteboard',
      hasPrototype: false,
      previewImage: null,
    })
    const wrapper = mount(ArtifactThumb, { props: { artifact: item.artifact } })
    wrappers.push(wrapper)
    await nextTick()
    expect(wrapper.findAll('.minimap-box')).toHaveLength(2)
  })

  test('re-renders visible whiteboard SVG when updatedAt changes without losing visibility', async () => {
    const originalIO = globalThis.IntersectionObserver
    const observed = new Set<Element>()
    let notify: IntersectionObserverCallback | null = null
    class FakeIO {
      constructor(cb: IntersectionObserverCallback) {
        notify = cb
      }
      observe(el: Element) {
        observed.add(el)
      }
      unobserve(el: Element) {
        observed.delete(el)
      }
      disconnect() {
        observed.clear()
      }
      takeRecords() {
        return []
      }
    }
    globalThis.IntersectionObserver = FakeIO as unknown as typeof IntersectionObserver

    const itemV1 = makeItem('board_refresh', {
      kind: 'whiteboard',
      hasPrototype: false,
      previewImage: null,
      updatedAt: 1_800_000_001_000,
    })

    const getArtifactSpy = vi.spyOn(api, 'getArtifact').mockImplementation(async (id) => ({
      artifact: {
        ...itemV1.artifact,
        id,
        content: makeBoardJson(
          id === 'board_refresh' ? `rev-${getArtifactSpy.mock.calls.length}` : 'other',
        ),
      },
      versions: [],
      comments: [],
      nextVersionPageToken: '',
      hasMoreVersions: false,
      askBaseline: null,
    }))

    try {
      const wrapper = mount(ArtifactThumb, { props: { artifact: itemV1.artifact } })
      wrappers.push(wrapper)
      await flushPromises()
      await nextTick()

      // Before becoming visible, no full board fetch or SVG image yet.
      expect(observed.size).toBe(1)
      expect(getArtifactSpy).not.toHaveBeenCalled()
      expect(wrapper.find('img.thumb-board').exists()).toBe(false)

      // Enter viewport once: observeThumbnailVisibility fires callback and unobserves the element.
      const el = [...observed][0]!
      notify!(
        [{ target: el, isIntersecting: true } as IntersectionObserverEntry],
        {} as IntersectionObserver,
      )
      await vi.waitFor(() => expect(wrapper.find('img.thumb-board').exists()).toBe(true))
      expect(observed.size).toBe(0)
      expect(getArtifactSpy).toHaveBeenCalledTimes(1)
      const firstSrc = wrapper.find('img.thumb-board').attributes('src')
      expect(firstSrc).toContain('data:image/svg+xml')

      // Same artifact gets a new updatedAt (silent refresh): must re-render SVG immediately
      // without needing another IntersectionObserver notification.
      await wrapper.setProps({
        artifact: { ...itemV1.artifact, updatedAt: 1_800_000_002_000 },
      })
      await vi.waitFor(() => expect(getArtifactSpy).toHaveBeenCalledTimes(2))
      await vi.waitFor(() => {
        const nextSrc = wrapper.find('img.thumb-board').attributes('src')
        expect(nextSrc).toContain('data:image/svg+xml')
        expect(nextSrc).not.toBe(firstSrc)
      })

      // Switching to a different artifact id resets visibility and re-observes the element.
      const otherItem = makeItem('board_other', {
        kind: 'whiteboard',
        hasPrototype: false,
        previewImage: null,
        updatedAt: 1_800_000_003_000,
      })
      await wrapper.setProps({ artifact: otherItem.artifact })
      await flushPromises()
      await nextTick()
      expect(observed.size).toBe(1)
      expect(getArtifactSpy).toHaveBeenCalledTimes(2)
    } finally {
      globalThis.IntersectionObserver = originalIO
    }
  })
})

describe('ArtifactGallery interactions', () => {
  const sampleItems = [
    makeItem('art_doc', {
      kind: 'document',
      title: '全项目画廊方案',
      projectId: 'prj_a',
      projectName: 'Ariadne 主工程',
      pendingActions: 1,
      nodeTitle: '画廊节点',
    }),
    makeItem('art_proto', {
      kind: 'whiteboard',
      title: '画廊交互设计稿',
      projectId: 'prj_a',
      projectName: 'Ariadne 主工程',
      hasPrototype: true,
      previewImage: '/api/files/fil_cover_proto',
    }),
    makeItem('art_dash', {
      kind: 'dashboard',
      title: '推理延迟看板',
      projectId: 'prj_b',
      projectName: 'DouUI 组件库',
      pendingFeedback: 1,
      openComments: 2,
    }),
  ]

  beforeEach(() => {
    locale.value = 'zh-CN'
    resetGalleryStateForTests()
    vi.spyOn(api, 'listGalleryArtifacts').mockResolvedValue({
      items: sampleItems,
      nextPageToken: '',
      hasMore: false,
      counts: sampleCounts,
    })
    vi.spyOn(api, 'getArtifact').mockImplementation(async (id) => {
      const found = sampleItems.find((entry) => entry.artifact.id === id) ?? sampleItems[0]!
      return {
        artifact: { ...found.artifact, content: '# 详细内容\n\n正文预览' },
        versions: [
          {
            version: 3,
            title: found.artifact.title,
            author: 'user',
            reason: '补充细节',
            createdAt: 1800000000000,
          },
        ],
        comments: [],
        nextVersionPageToken: '',
        hasMoreVersions: false,
        askBaseline: null,
      }
    })
  })

  afterEach(() => {
    while (wrappers.length) wrappers.pop()!.unmount()
    resetGalleryStateForTests()
    vi.restoreAllMocks()
  })

  test('loads cross-project items, opens artifact on card click, and opens quick-view on button or Space', async () => {
    const openArtifactSpy = vi.spyOn(store, 'openProjectArtifact').mockResolvedValue()
    const wrapper = mountGallery('wide')
    await flushPromises()

    expect(wrapper.findAll('[data-artifact-id]')).toHaveLength(3)
    expect(wrapper.text()).toContain('全项目画廊方案')
    expect(wrapper.text()).toContain('画廊交互设计稿')
    expect(wrapper.text()).toContain('推理延迟看板')

    // Click card directly opens artifact in its project.
    await wrapper.find('[data-artifact-id="art_doc"]').trigger('click')
    expect(openArtifactSpy).toHaveBeenCalledWith('prj_a', 'art_doc')

    // Click quick-view button opens docked non-blocking side panel without leaving gallery.
    openArtifactSpy.mockClear()
    await wrapper.find('[data-artifact-id="art_proto"] .quick-view-btn').trigger('click')
    await flushPromises()
    expect(openArtifactSpy).not.toHaveBeenCalled()
    expect(galleryState.quickViewId).toBe('art_proto')
    expect(wrapper.find('aside.quick-pane').exists()).toBe(true)
    expect(wrapper.find('aside.quick-pane').text()).toContain('画廊交互设计稿')

    // Pressing Space on another card switches quick-view target.
    await wrapper.find('[data-artifact-id="art_dash"]').trigger('keydown', { key: ' ' })
    await flushPromises()
    expect(galleryState.quickViewId).toBe('art_dash')
    expect(wrapper.find('aside.quick-pane').text()).toContain('推理延迟看板')
  })

  test('virtual prototype tab queries with onlyPrototypes=true and status chips filter cleanly', async () => {
    const wrapper = mountGallery('wide')
    await flushPromises()

    const listSpy = vi.mocked(api.listGalleryArtifacts)
    listSpy.mockClear()

    // Click prototype segment option.
    const protoRadio = wrapper
      .findAll('button, label, input')
      .find((el) => el.text().includes('交互原型'))
    expect(protoRadio).toBeTruthy()
    await protoRadio!.trigger('click')
    await flushPromises()

    expect(listSpy).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: '',
        onlyPrototypes: true,
        includeCounts: true,
      }),
    )

    // Click "has comments" status chip and verify status='has_comments' is sent.
    listSpy.mockClear()
    const commentsChip = wrapper.findAll('button').find((el) => el.text().includes('有未解决评论'))
    expect(commentsChip).toBeTruthy()
    await commentsChip!.trigger('click')
    await flushPromises()

    expect(listSpy).toHaveBeenCalledWith(
      expect.objectContaining({
        status: 'has_comments',
        includeCounts: true,
      }),
    )
  })

  test('narrow layout shows filter sheet with dedicated field labels and bottom sheet quick-view drawer', async () => {
    const desktopWrapper = mountGallery('wide')
    await flushPromises()
    const desktopLabels = desktopWrapper
      .findAll('.gallery-select-wrap [aria-label]')
      .map((el) => el.attributes('aria-label'))
    expect(desktopLabels).toContain('排序')
    expect(desktopLabels).toContain('分组')
    expect(desktopLabels).not.toContain('最近更新')
    expect(desktopLabels).not.toContain('不分组')
    desktopWrapper.unmount()

    galleryState.groupBy = 'project'
    galleryState.viewMode = 'list'
    galleryState.density = 'compact'

    const wrapper = mountGallery('narrow')
    await flushPromises()

    // Narrow kind strip holds toggle buttons (DouCheckTag), so it uses role="group".
    expect(wrapper.find('.narrow-kind-strip').attributes('role')).toBe('group')

    // Count === 0: #leading icon alone renders DouButton as icon-only with no label wrapper.
    const filterBtn = wrapper.find('.mobile-filter-trigger')
    expect(filterBtn.exists()).toBe(true)
    expect(filterBtn.attributes('aria-label')).toBe('筛选与排序')
    expect(filterBtn.text()).not.toContain('筛选与排序')
    expect(filterBtn.classes()).toContain('is-icon-only')
    expect(filterBtn.find('.dou-button__icon svg').exists()).toBe(true)
    expect(filterBtn.find('.dou-button__label').exists()).toBe(false)

    // Count > 0: #leading icon stays in .dou-button__icon while count sits in .dou-button__label.
    galleryState.sort = 'title_asc'
    await nextTick()
    expect(filterBtn.classes()).not.toContain('is-icon-only')
    expect(filterBtn.find('.dou-button__icon svg').exists()).toBe(true)
    expect(filterBtn.find('.dou-button__label .mobile-filter-count').text()).toBe('1')

    await filterBtn.trigger('click')
    await nextTick()
    expect(document.body.textContent).toContain('筛选与排序')
    expect(document.body.querySelector('.mobile-filter-sheet')).not.toBeNull()

    const fieldHeadings = [
      ...document.body.querySelectorAll('.mobile-filter-sheet .sheet-field > :first-child'),
    ].map((el) => el.textContent?.trim())
    expect(fieldHeadings).toEqual(['项目', '状态', '排序', '分组', '视图', '密度'])
    expect(
      document.body.querySelectorAll('.mobile-filter-sheet .dou-segmented-control').length,
    ).toBe(2)

    // Leaving narrow resets mobileFilterOpen so returning to narrow does not re-open the drawer.
    await wrapper.setProps({ layoutMode: 'medium' })
    await nextTick()
    expect(document.body.querySelector('.mobile-filter-sheet')).toBeNull()

    await wrapper.setProps({ layoutMode: 'narrow' })
    await nextTick()
    expect(document.body.querySelector('.mobile-filter-sheet')).toBeNull()

    // Narrow list row keeps the kind label in metadata so the title keeps full width.
    const docRow = wrapper.find('[data-artifact-id="art_doc"]')
    expect(docRow.find('.list-meta-row').text()).toContain('文档')
    expect(docRow.find('.list-end .list-time').exists()).toBe(false)

    await docRow.find('.list-quick-btn').trigger('click')
    await flushPromises()
    expect(wrapper.find('aside.quick-pane').exists()).toBe(false)
    expect(galleryState.quickViewId).toBe('art_doc')
  })

  test('quick preview sanitizes markdown URLs, blocks external images, sets target="_blank", and routes internal links', async () => {
    const markdown = [
      '![外链追踪图](https://evil.example/pixel.png)',
      '![相对图](assets/local.png)',
      '![站内截图](file:fil_safe_123)',
      '[官网](https://example.com/docs)',
      '[不安全链接](http://evil.example/phish)',
      '[相对路径](/internal/secret)',
      '[关联画板](artifact:art_proto)',
      '[关联讨论](thread:thr_42)',
      '[附件文件](file:fil_attach_9)',
    ].join('\n\n')

    vi.mocked(api.getArtifact).mockResolvedValueOnce({
      artifact: { ...sampleItems[0]!.artifact, content: markdown },
      versions: [],
      comments: [],
      nextVersionPageToken: '',
      hasMoreVersions: false,
      askBaseline: null,
    })

    const openArtifactSpy = vi.spyOn(store, 'openProjectArtifact').mockResolvedValue()
    const openThreadSpy = vi.spyOn(store, 'openProjectThread').mockResolvedValue()
    previewFile.value = null

    // Desktop aside quick view
    const desktopWrapper = mountGallery('wide')
    await flushPromises()
    await desktopWrapper.find('[data-artifact-id="art_doc"] .quick-view-btn').trigger('click')
    await flushPromises()

    const pane = desktopWrapper.find('aside.quick-pane .quick-markdown')
    expect(pane.exists()).toBe(true)

    const imgs = pane.findAll('img')
    expect(imgs).toHaveLength(1)
    expect(imgs[0]!.attributes('src')).toBe('/api/files/fil_safe_123')

    const extLink = pane.find('a[href="https://example.com/docs"]')
    expect(extLink.exists()).toBe(true)
    expect(extLink.attributes('target')).toBe('_blank')
    expect(extLink.attributes('rel')).toContain('noopener')

    expect(pane.find('a[href^="http://"]').exists()).toBe(false)
    expect(pane.find('a[href="/internal/secret"]').exists()).toBe(false)

    const artifactLink = pane.findAll('a').find((a) => a.text().includes('关联画板'))
    expect(artifactLink).toBeTruthy()
    await artifactLink!.trigger('click')
    expect(openArtifactSpy).toHaveBeenCalledWith('prj_a', 'art_proto')

    const threadLink = pane.findAll('a').find((a) => a.text().includes('关联讨论'))
    expect(threadLink).toBeTruthy()
    await threadLink!.trigger('click')
    expect(openThreadSpy).toHaveBeenCalledWith('prj_a', 'thr_42')

    const fileLink = pane
      .findAll('a, button, [role="button"]')
      .find((el) => el.text().includes('附件文件'))
    expect(fileLink).toBeTruthy()
    await fileLink!.trigger('click')
    expect(previewFile.value).toMatchObject({ id: 'fil_attach_9' })

    desktopWrapper.unmount()

    // Narrow drawer quick view applies the same URL sanitization
    vi.mocked(api.getArtifact).mockResolvedValueOnce({
      artifact: { ...sampleItems[0]!.artifact, content: markdown },
      versions: [],
      comments: [],
      nextVersionPageToken: '',
      hasMoreVersions: false,
      askBaseline: null,
    })
    resetGalleryStateForTests()
    const narrowWrapper = mountGallery('narrow')
    await flushPromises()
    await narrowWrapper.find('[data-artifact-id="art_doc"] .quick-view-btn').trigger('click')
    await flushPromises()

    const drawerMarkdown = document.body.querySelector('.quick-sheet-body .quick-markdown')
    expect(drawerMarkdown).toBeTruthy()
    const drawerImgs = [...drawerMarkdown!.querySelectorAll('img')]
    expect(drawerImgs).toHaveLength(1)
    expect(drawerImgs[0]!.getAttribute('src')).toBe('/api/files/fil_safe_123')
    const drawerExt = drawerMarkdown!.querySelector('a[href="https://example.com/docs"]')
    expect(drawerExt?.getAttribute('target')).toBe('_blank')
    expect(drawerMarkdown!.querySelector('a[href^="http://"]')).toBeNull()
  })

  test('virtualized mode paginates via DouVirtualList @end-reached without eager page 2 fetch and restores scroll position', async () => {
    const page1 = Array.from({ length: 48 }, (_, i) =>
      makeItem(`art_p1_${i}`, { title: `第 1 页产物 ${i}` }),
    )
    const page2 = Array.from({ length: 48 }, (_, i) =>
      makeItem(`art_p2_${i}`, { title: `第 2 页产物 ${i}` }),
    )
    const page3 = Array.from({ length: 10 }, (_, i) =>
      makeItem(`art_p3_${i}`, { title: `第 3 页产物 ${i}` }),
    )

    const observeSpy = vi.fn()
    const originalIO = globalThis.IntersectionObserver
    globalThis.IntersectionObserver = class {
      observe = observeSpy
      disconnect = vi.fn()
      unobserve = vi.fn()
      takeRecords = () => []
      root = null
      rootMargin = ''
      thresholds = []
    } as unknown as typeof IntersectionObserver

    try {
      const listSpy = vi.mocked(api.listGalleryArtifacts)
      listSpy.mockReset()
      listSpy.mockImplementation(async (params) => {
        if (!params?.pageToken) {
          return { items: page1, nextPageToken: 'tok_2', hasMore: true, counts: sampleCounts }
        }
        if (params?.pageToken === 'tok_2') {
          return { items: page2, nextPageToken: 'tok_3', hasMore: true, counts: null }
        }
        return { items: page3, nextPageToken: '', hasMore: false, counts: null }
      })

      const wrapper = mountGallery('wide')
      await flushPromises()
      await nextTick()

      // Enters virtualized mode (> 40 items) and does NOT attach an external IntersectionObserver or eagerly fetch page 2.
      const virtualList = wrapper.findComponent({ name: 'DouVirtualList' })
      expect(virtualList.exists()).toBe(true)
      expect(observeSpy).not.toHaveBeenCalled()
      expect(listSpy).toHaveBeenCalledTimes(1)

      // Trigger page 2 and page 3 via DouVirtualList's endReached event.
      virtualList.vm.$emit('endReached')
      await flushPromises()
      expect(listSpy).toHaveBeenCalledTimes(2)
      expect(listSpy).toHaveBeenLastCalledWith(expect.objectContaining({ pageToken: 'tok_2' }))
      expect(galleryState.items).toHaveLength(96)

      virtualList.vm.$emit('endReached')
      await flushPromises()
      expect(listSpy).toHaveBeenCalledTimes(3)
      expect(listSpy).toHaveBeenLastCalledWith(expect.objectContaining({ pageToken: 'tok_3' }))
      expect(galleryState.items).toHaveLength(106)
      expect(galleryState.hasMore).toBe(false)

      // Record virtual list scrollTop and verify restoration after remounting gallery.
      const scrollEl = virtualList.element as HTMLElement
      scrollEl.scrollTop = 420
      scrollEl.dispatchEvent(new Event('scroll'))
      expect(galleryState.scrollTop).toBe(420)

      wrapper.unmount()

      const remounted = mountGallery('wide')
      await flushPromises()
      await nextTick()
      const restoredList = remounted.findComponent({ name: 'DouVirtualList' })
      expect(restoredList.exists()).toBe(true)
      expect((restoredList.element as HTMLElement).scrollTop).toBe(420)
    } finally {
      globalThis.IntersectionObserver = originalIO
    }
  })

  test('marks gallery stale on live events while unmounted and reloads on re-entry', async () => {
    vi.spyOn(api, 'listProjects').mockResolvedValue({ projects: [], lastSeq: 1 })
    vi.spyOn(api, 'getModelSettings').mockResolvedValue({
      models: [],
      providers: [],
      roles: { thread: { model: '' } },
      showCost: false,
    } as never)
    vi.spyOn(api, 'getInputSettings').mockResolvedValue({
      submitOn: 'enter',
      snippets: [],
      keys: {},
    } as never)

    let pushFrame!: (frame: StreamEventsResponse) => void
    vi.spyOn(api, 'streamEvents').mockImplementationOnce((_opts, signal) =>
      (async function* () {
        yield create(StreamEventsResponseSchema, {
          payload: { case: 'ready', value: create(StreamReadySchema, { lastSeq: 1n }) },
        })
        for (;;) {
          if (signal?.aborted) return
          const frame = await new Promise<StreamEventsResponse | null>((resolve) => {
            pushFrame = resolve
            signal?.addEventListener('abort', () => resolve(null), { once: true })
          })
          if (!frame) return
          yield frame
        }
      })(),
    )

    await store.start(async () => {})
    await flushPromises()

    const listSpy = vi.mocked(api.listGalleryArtifacts)
    const firstWrapper = mountGallery('wide')
    await flushPromises()
    expect(listSpy).toHaveBeenCalledTimes(1)
    expect(galleryState.stale).toBe(false)

    // Leave the gallery (component unmounts).
    firstWrapper.unmount()

    // While outside /gallery, an artifact revision arrives over the live event stream.
    const encoder = new TextEncoder()
    pushFrame(
      create(StreamEventsResponseSchema, {
        payload: {
          case: 'event',
          value: create(StoredEventSchema, {
            seq: 2n,
            id: 'evt_2',
            projectId: 'prj_a',
            type: 'artifact.revised',
            at: 1800000001000n,
            rawJson: encoder.encode(JSON.stringify({ artifactId: 'art_doc', version: 4 })),
          }),
        },
      }),
    )
    await flushPromises()
    expect(galleryState.stale).toBe(true)

    // Returning to /gallery automatically reloads fresh data.
    const secondWrapper = mountGallery('wide')
    await flushPromises()
    expect(listSpy).toHaveBeenCalledTimes(2)
    expect(galleryState.stale).toBe(false)

    secondWrapper.unmount()

    // Plan node rename while outside /gallery also invalidates cached nodeTitle.
    pushFrame(
      create(StreamEventsResponseSchema, {
        payload: {
          case: 'event',
          value: create(StoredEventSchema, {
            seq: 3n,
            id: 'evt_3',
            projectId: 'prj_a',
            type: 'plan.revised',
            at: 1800000002000n,
            rawJson: encoder.encode(JSON.stringify({})),
          }),
        },
      }),
    )
    await flushPromises()
    expect(galleryState.stale).toBe(true)
  })

  test('ignores global /, Escape, and Arrow shortcuts when a modal dialog or listbox overlay is open', async () => {
    const wrapper = mountGallery('wide')
    await flushPromises()

    // Open desktop quick view on the first card.
    await wrapper.find('[data-artifact-id="art_doc"] .quick-view-btn').trigger('click')
    await flushPromises()
    expect(galleryState.quickViewId).toBe('art_doc')

    const searchInput = wrapper.find('.gallery-search input').element as HTMLInputElement

    // Simulate an open modal dialog (e.g. settings modal / command palette).
    const modal = document.createElement('div')
    modal.setAttribute('role', 'dialog')
    modal.setAttribute('aria-modal', 'true')
    const modalBtn = document.createElement('button')
    modal.appendChild(modalBtn)
    document.body.appendChild(modal)
    modalBtn.focus()

    try {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
      expect(galleryState.quickViewId).toBe('art_doc')

      const downEv = new KeyboardEvent('keydown', {
        key: 'ArrowDown',
        bubbles: true,
        cancelable: true,
      })
      window.dispatchEvent(downEv)
      expect(downEv.defaultPrevented).toBe(false)
      expect(galleryState.quickViewId).toBe('art_doc')

      const slashEv = new KeyboardEvent('keydown', { key: '/', bubbles: true, cancelable: true })
      window.dispatchEvent(slashEv)
      expect(slashEv.defaultPrevented).toBe(false)
      expect(document.activeElement).not.toBe(searchInput)
    } finally {
      modal.remove()
    }

    // Simulate an open DouSelect listbox dropdown.
    const listbox = document.createElement('div')
    listbox.setAttribute('role', 'listbox')
    document.body.appendChild(listbox)
    try {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
      expect(galleryState.quickViewId).toBe('art_doc')

      const downEv = new KeyboardEvent('keydown', {
        key: 'ArrowDown',
        bubbles: true,
        cancelable: true,
      })
      window.dispatchEvent(downEv)
      expect(downEv.defaultPrevented).toBe(false)
      expect(galleryState.quickViewId).toBe('art_doc')
    } finally {
      listbox.remove()
    }

    // With no overlay open, ArrowDown steps quick view and Escape closes it.
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    expect(galleryState.quickViewId).toBe('art_proto')

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(galleryState.quickViewId).toBeNull()
  })

  test('isolates card Enter and Space shortcuts from nested project pill and quick-view buttons', async () => {
    const openArtifactSpy = vi.spyOn(store, 'openProjectArtifact').mockResolvedValue()
    const wrapper = mountGallery('wide')
    await flushPromises()

    const card = wrapper.find('[data-artifact-id="art_doc"]')
    const projectPill = card.find('button.project-pill')
    const quickBtn = card.find('button.quick-view-btn')

    // Pressing Enter or Space while focused on nested buttons must NOT trigger card shortcuts or preventDefault.
    for (const btn of [projectPill, quickBtn]) {
      const enterEv = new KeyboardEvent('keydown', {
        key: 'Enter',
        bubbles: true,
        cancelable: true,
      })
      btn.element.dispatchEvent(enterEv)
      expect(enterEv.defaultPrevented).toBe(false)
      expect(openArtifactSpy).not.toHaveBeenCalled()

      const spaceEv = new KeyboardEvent('keydown', {
        key: ' ',
        bubbles: true,
        cancelable: true,
      })
      btn.element.dispatchEvent(spaceEv)
      expect(spaceEv.defaultPrevented).toBe(false)
      expect(galleryState.quickViewId).toBeNull()
    }

    // Pressing Enter or Space on the card container itself still triggers open / quickView.
    await card.trigger('keydown', { key: 'Enter' })
    expect(openArtifactSpy).toHaveBeenCalledWith('prj_a', 'art_doc')

    await card.trigger('keydown', { key: ' ' })
    expect(galleryState.quickViewId).toBe('art_doc')
  })
})
