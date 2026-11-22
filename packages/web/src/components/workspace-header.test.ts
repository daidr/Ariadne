// @vitest-environment happy-dom
import type { ProjectView } from '@ariadne/core'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { DouConfigProvider } from 'dou-ui'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import { api } from '../api'
import { douLocale, locale } from '../i18n'
import { state } from '../store'
import type { WorkspaceLayoutMode } from '../workspace-layout'
import WorkspaceHeader from './WorkspaceHeader.vue'

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

function visiblePopovers() {
  return [...document.querySelectorAll<HTMLElement>('.dou-popover')].filter(
    (el) => el.style.display !== 'none',
  )
}

const wrappers: VueWrapper[] = []

function mountHeader(
  options: {
    layoutMode?: WorkspaceLayoutMode
    projectsCollapsed?: boolean
    sideCollapsed?: boolean
  } = {},
) {
  const layoutMode = ref<WorkspaceLayoutMode>(options.layoutMode ?? 'medium')
  const projectsCollapsed = ref(options.projectsCollapsed ?? false)
  const sideCollapsed = ref(options.sideCollapsed ?? false)
  const projectsPopoverOpen = ref(false)
  const onToggleSide = vi.fn(() => {
    sideCollapsed.value = !sideCollapsed.value
  })
  const onToggleProjects = vi.fn(() => {
    projectsCollapsed.value = !projectsCollapsed.value
  })

  const Host = defineComponent({
    setup() {
      return () =>
        h(DouConfigProvider, { locale: douLocale.value }, () =>
          h(WorkspaceHeader, {
            layoutMode: layoutMode.value,
            projectsCollapsed: projectsCollapsed.value,
            sideCollapsed: sideCollapsed.value,
            projectsPopoverOpen: projectsPopoverOpen.value,
            'onUpdate:projectsPopoverOpen': (value: boolean) => {
              projectsPopoverOpen.value = value
            },
            onToggleSide,
            onToggleProjects,
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
    projectsPopoverOpen,
    onToggleSide,
    onToggleProjects,
  }
}

describe('WorkspaceHeader medium layout', () => {
  let getProjectActivity: ReturnType<typeof vi.spyOn>
  let getProject: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    vi.useFakeTimers()
    locale.value = 'zh-CN'
    state.projects = [
      makeProjectSummary('prj_1', 'Ariadne', 0),
      makeProjectSummary('prj_2', 'Sandbox', 2),
    ]
    state.currentId = 'prj_1'
    state.detail = makeProjectDetail('prj_1', 'Ariadne')
    state.starting = false
    state.planWide = false
    state.workspaceLevel = 'coordinator'
    state.main = { kind: 'conversation' }

    getProjectActivity = vi.spyOn(api, 'getProjectActivity').mockResolvedValue({
      coordinator: { status: 'idle' },
      threads: [],
    })
    getProject = vi
      .spyOn(api, 'getProject')
      .mockImplementation(async (id) =>
        makeProjectDetail(id, id === 'prj_2' ? 'Sandbox' : 'Ariadne'),
      )
    vi.spyOn(api, 'getConversation').mockResolvedValue({
      conversation: null,
      messages: [],
      more: false,
      model: null,
    } as unknown as Awaited<ReturnType<typeof api.getConversation>>)
    vi.spyOn(api, 'getPlan').mockResolvedValue({ nodes: [], edges: [] } as unknown as Awaited<
      ReturnType<typeof api.getPlan>
    >)
    vi.spyOn(api, 'listArtifacts').mockResolvedValue({ artifacts: [] } as unknown as Awaited<
      ReturnType<typeof api.listArtifacts>
    >)
    vi.spyOn(api, 'listMemories').mockResolvedValue({ memories: [] } as unknown as Awaited<
      ReturnType<typeof api.listMemories>
    >)
  })

  afterEach(() => {
    while (wrappers.length) wrappers.pop()?.unmount()
    document.body.innerHTML = ''
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  test('opens project list in a popover, switches project, auto-closes and suppresses hover activity', async () => {
    const { wrapper, projectsPopoverOpen } = mountHeader({ layoutMode: 'medium' })

    const trigger = wrapper.find<HTMLButtonElement>('button.header-project-trigger')
    expect(trigger.exists()).toBe(true)
    expect(trigger.text()).toContain('Ariadne')
    expect(trigger.text()).toContain('2')

    await trigger.trigger('click')
    await nextTick()
    expect(projectsPopoverOpen.value).toBe(true)
    expect(visiblePopovers()).toHaveLength(1)

    const target = document.querySelector<HTMLButtonElement>('button[data-project-id="prj_2"]')
    expect(target).not.toBeNull()
    expect(target!.getAttribute('aria-expanded')).toBeNull()

    // Hovering inside the popover must not trigger a nested activity popover.
    target!.dispatchEvent(
      new PointerEvent('pointerenter', { bubbles: false, pointerType: 'mouse' }),
    )
    vi.advanceTimersByTime(600)
    await flushPromises()
    expect(getProjectActivity).not.toHaveBeenCalled()
    expect(visiblePopovers()).toHaveLength(1)

    // Selecting a project switches to it and closes the popover.
    target!.click()
    await nextTick()
    await flushPromises()

    expect(state.currentId).toBe('prj_2')
    expect(getProject).toHaveBeenCalledWith('prj_2', undefined)
    expect(projectsPopoverOpen.value).toBe(false)
    expect(visiblePopovers()).toHaveLength(0)
  })

  test('closes the project popover when starting a new project or leaving medium layout', async () => {
    const { wrapper, layoutMode, projectsPopoverOpen } = mountHeader({ layoutMode: 'medium' })
    const trigger = wrapper.find<HTMLButtonElement>('button.header-project-trigger')

    await trigger.trigger('click')
    await nextTick()
    expect(projectsPopoverOpen.value).toBe(true)

    const newBtn = document.querySelector<HTMLButtonElement>('button[aria-label="新建项目"]')
    expect(newBtn).not.toBeNull()
    newBtn!.click()
    await nextTick()
    expect(state.starting).toBe(true)
    expect(projectsPopoverOpen.value).toBe(false)

    // Reopen popover and resize to wide layout -> popover closes cleanly.
    state.starting = false
    await nextTick()
    await trigger.trigger('click')
    await nextTick()
    expect(projectsPopoverOpen.value).toBe(true)

    layoutMode.value = 'wide'
    await nextTick()
    expect(projectsPopoverOpen.value).toBe(false)
    expect(visiblePopovers()).toHaveLength(0)
    expect(wrapper.find('button.header-project-trigger').exists()).toBe(false)
  })

  test('toggles the side pane in medium mode and updates aria-expanded', async () => {
    const { wrapper, sideCollapsed, onToggleSide } = mountHeader({
      layoutMode: 'medium',
      sideCollapsed: false,
    })

    const sideBtn = wrapper.find<HTMLButtonElement>('button[aria-label="收起项目面板"]')
    expect(sideBtn.exists()).toBe(true)
    expect(sideBtn.attributes('aria-expanded')).toBe('true')

    await sideBtn.trigger('click')
    await nextTick()
    expect(onToggleSide).toHaveBeenCalledTimes(1)
    expect(sideCollapsed.value).toBe(true)

    const expandBtn = wrapper.find<HTMLButtonElement>('button[aria-label="展开项目面板"]')
    expect(expandBtn.exists()).toBe(true)
    expect(expandBtn.attributes('aria-expanded')).toBe('false')
  })
})
