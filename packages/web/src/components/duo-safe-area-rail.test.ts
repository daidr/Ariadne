// @vitest-environment happy-dom
import type { ProjectView } from '@ariadne/core'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { DouConfigProvider } from 'dou-ui'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import type { api } from '../api'
import { douLocale, locale } from '../i18n'
import { openMain, showWorkspace, state } from '../store'
import type { WorkspaceLayoutMode } from '../workspace-layout'
import DuoSafeAreaRail from './DuoSafeAreaRail.vue'

type ProjectResponse = Awaited<ReturnType<typeof api.getProject>>

function makeProjectSummary(id: string, name: string, needsMe = 0): ProjectView {
  return {
    id,
    name,
    coordinatorId: `crd_${id}`,
    needsMe,
    runningTurns: 0,
    hasWork: false,
    updatedAt: Date.now(),
  } as unknown as ProjectView
}

function makeProjectDetail(id: string, name: string): ProjectResponse {
  return {
    project: {
      id,
      name,
      coordinatorId: `crd_${id}`,
      source: null,
      fileSources: [],
      references: [],
      mcp: [],
      settings: {
        autopilot: { enabled: false },
      },
    },
    threads: [],
    needsMe: [],
    hasTracker: false,
    sync: { lastSeq: 0, cursor: '' },
  } as unknown as ProjectResponse
}

const wrappers: VueWrapper[] = []

function mountRail(
  options: {
    layoutMode?: WorkspaceLayoutMode
    projectsCollapsed?: boolean
    sideCollapsed?: boolean
    compactHeight?: boolean
  } = {},
) {
  const layoutMode = ref<WorkspaceLayoutMode>(options.layoutMode ?? 'narrow')
  const projectsCollapsed = ref(options.projectsCollapsed ?? false)
  const sideCollapsed = ref(options.sideCollapsed ?? false)
  const compactHeight = ref(options.compactHeight ?? false)
  const onOpenProjects = vi.fn()
  const onToggleProjects = vi.fn()
  const onToggleSide = vi.fn(() => {
    sideCollapsed.value = !sideCollapsed.value
  })
  const onOpenSettings = vi.fn()

  const Host = defineComponent({
    setup() {
      return () =>
        h(DouConfigProvider, { locale: douLocale.value }, () =>
          h(DuoSafeAreaRail, {
            layoutMode: layoutMode.value,
            projectsCollapsed: projectsCollapsed.value,
            sideCollapsed: sideCollapsed.value,
            compactHeight: compactHeight.value,
            onOpenProjects,
            onToggleProjects,
            onToggleSide,
            onOpenSettings,
          }),
        )
    },
  })

  const wrapper = mount(Host, { attachTo: document.body })
  wrappers.push(wrapper)
  return {
    wrapper,
    layoutMode,
    projectsCollapsed,
    sideCollapsed,
    compactHeight,
    onOpenProjects,
    onToggleProjects,
    onToggleSide,
    onOpenSettings,
  }
}

describe('DuoSafeAreaRail', () => {
  beforeEach(() => {
    locale.value = 'zh-CN'
    state.projects = [
      makeProjectSummary('prj_1', 'Ariadne', 1),
      makeProjectSummary('prj_2', 'Sandbox', 2),
    ]
    state.currentId = 'prj_1'
    state.detail = makeProjectDetail('prj_1', 'Ariadne')
    state.starting = false
    state.planWide = false
    state.main = { kind: 'conversation' }
    state.workspaceLevel = 'coordinator'
    state.searchOpen = false
    state.projectSettingsOpen = false
  })

  afterEach(() => {
    for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  })

  test('navigates between side, coordinator and projects in narrow folded layout', async () => {
    const { wrapper } = mountRail({ layoutMode: 'narrow' })

    const sideButton = wrapper.get('[data-duo-rail-side]')
    expect(sideButton.attributes('aria-pressed')).toBe('false')
    await sideButton.trigger('click')
    expect(state.workspaceLevel).toBe('side')

    await nextTick()
    expect(wrapper.get('[data-duo-rail-side]').attributes('aria-pressed')).toBe('true')

    const backButton = wrapper.get('[data-duo-rail-back]')
    expect(backButton.attributes('aria-label')).toBe('返回协调器')
    await backButton.trigger('click')
    expect(state.workspaceLevel).toBe('coordinator')

    await nextTick()
    expect(wrapper.get('[data-duo-rail-back]').attributes('aria-label')).toBe('返回项目列表')
    await wrapper.get('[data-duo-rail-back]').trigger('click')
    expect(state.workspaceLevel).toBe('projects')

    await nextTick()
    expect(wrapper.find('[data-duo-rail-side]').exists()).toBe(false)
    expect((wrapper.get('[data-duo-rail-back]').element as HTMLButtonElement).disabled).toBe(true)
  })

  test('returns from main sub-views such as plan and trace before leaving the workspace level', async () => {
    openMain({ kind: 'trace', conversationId: 'crd_prj_1' })
    const { wrapper } = mountRail({ layoutMode: 'medium' })

    expect(wrapper.find('[data-duo-rail-side]').exists()).toBe(false)
    const backButton = wrapper.get('[data-duo-rail-back]')
    expect((backButton.element as HTMLButtonElement).disabled).toBe(false)

    await backButton.trigger('click')
    expect(state.main.kind).toBe('conversation')
  })

  test('opens the quick menu popover with project switcher, search, and settings in medium layout', async () => {
    const { wrapper, onOpenProjects, onToggleSide, onOpenSettings } = mountRail({
      layoutMode: 'medium',
      compactHeight: true,
    })

    expect(wrapper.get('[data-duo-safe-rail]').classes()).toContain('is-compact-height')
    expect(wrapper.get('.duo-rail-badge').text()).toBe('3')

    await wrapper.get('[data-duo-rail-side]').trigger('click')
    expect(onToggleSide).toHaveBeenCalledTimes(1)

    await wrapper.get('[data-duo-rail-menu]').trigger('click')
    await flushPromises()

    const menuButtons = [...document.querySelectorAll<HTMLButtonElement>('.duo-rail-menu-item')]
    const findByText = (text: string) =>
      menuButtons.find((button) => button.textContent?.includes(text))

    findByText('切换项目')?.click()
    expect(onOpenProjects).toHaveBeenCalledTimes(1)

    await wrapper.get('[data-duo-rail-menu]').trigger('click')
    await flushPromises()
    const reopenButtons = [...document.querySelectorAll<HTMLButtonElement>('.duo-rail-menu-item')]
    reopenButtons.find((button) => button.textContent?.includes('搜索'))?.click()
    expect(state.searchOpen).toBe(true)

    await wrapper.get('[data-duo-rail-menu]').trigger('click')
    await flushPromises()
    const settingsButtons = [...document.querySelectorAll<HTMLButtonElement>('.duo-rail-menu-item')]
    settingsButtons
      .find(
        (button) =>
          button.textContent?.includes('设置') && !button.textContent?.includes('项目设置'),
      )
      ?.click()
    expect(onOpenSettings).toHaveBeenCalledTimes(1)

    showWorkspace('projects')
    await nextTick()
  })
})
