// @vitest-environment happy-dom
import type { GalleryArtifactSummary, GalleryCounts } from '@ariadne/core'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { DouConfigProvider } from 'dou-ui'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { api } from '../api'
import { douLocale, locale } from '../i18n'
import * as store from '../store'
import ArtifactGallery from './ArtifactGallery.vue'
import ArtifactThumb from './ArtifactThumb.vue'
import { formatGalleryUpdater, groupGalleryItems, resolveGalleryTimeBucket } from './gallery-groups'
import { galleryState, resetGalleryStateForTests } from './gallery-state'

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
    setup() {
      return () =>
        h(DouConfigProvider, { locale: douLocale.value }, () => h(ArtifactGallery, { layoutMode }))
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
    await wrapper.find('[data-artifact-id="art_dash"]').trigger('keydown.space')
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
  })

  test('narrow layout shows filter sheet trigger and bottom sheet quick-view drawer', async () => {
    const wrapper = mountGallery('narrow')
    await flushPromises()

    const filterBtn = wrapper.find('.mobile-filter-trigger')
    expect(filterBtn.exists()).toBe(true)
    await filterBtn.trigger('click')
    await nextTick()
    expect(document.body.textContent).toContain('筛选与排序')

    await wrapper.find('[data-artifact-id="art_doc"] .quick-view-btn').trigger('click')
    await flushPromises()
    expect(wrapper.find('aside.quick-pane').exists()).toBe(false)
    expect(galleryState.quickViewId).toBe('art_doc')
  })
})
