// @vitest-environment happy-dom
import type { ProjectView } from '@ariadne/core'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { DouConfigProvider } from 'dou-ui'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import { api, type UpdateStatus } from '../api'
import { douLocale, locale } from '../i18n'
import { applyUpdateStatus, state } from '../store'
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
  const onOpenSettings = vi.fn()

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
    projectsPopoverOpen,
    onToggleSide,
    onToggleProjects,
    onOpenSettings,
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
    state.updateStatus = null

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

  test('merges update indicator into settings badge only on narrow project views', async () => {
    const baseUpdate: UpdateStatus = {
      version: '0.1.0-alpha.23',
      preferences: { channel: 'preview', automatic: true },
      state: 'ready',
      release: {
        version: '0.1.0-alpha.24',
        notes: '',
        url: 'https://example.test/release',
        prerelease: true,
        installable: true,
      },
      canInstall: true,
      canRestart: true,
      restarting: false,
      restartPhase: 'waiting_idle',
      activeTurns: 2,
      activeBackground: 0,
      downloadPercent: 0,
    }
    applyUpdateStatus(baseUpdate, { trackVersion: false })

    const { wrapper, layoutMode, onOpenSettings } = mountHeader({ layoutMode: 'narrow' })

    // Narrow project inner page: standalone indicator is hidden; settings button shows a primary dot badge.
    expect(wrapper.find('button.update-indicator').exists()).toBe(false)
    const settingsBtn = wrapper.find<HTMLButtonElement>('button[aria-label="设置 · 空闲后重启"]')
    expect(settingsBtn.exists()).toBe(true)
    expect(settingsBtn.find('.dou-badge__mark.is-dot.dou-badge--primary').exists()).toBe(true)

    await settingsBtn.trigger('click')
    expect(onOpenSettings).toHaveBeenCalledTimes(1)

    // Draining switches the settings dot badge to warning tone.
    applyUpdateStatus(
      { ...baseUpdate, restarting: true, restartPhase: 'draining' },
      { trackVersion: false },
    )
    await nextTick()
    const drainingSettingsBtn = wrapper.find<HTMLButtonElement>(
      'button[aria-label="设置 · 排空等待重启"]',
    )
    expect(drainingSettingsBtn.exists()).toBe(true)
    expect(drainingSettingsBtn.find('.dou-badge__mark.is-dot.dou-badge--warning').exists()).toBe(
      true,
    )

    // Narrow project list home: standalone indicator returns with label, settings badge hides.
    state.workspaceLevel = 'projects'
    await nextTick()
    const standaloneOnHome = wrapper.find<HTMLButtonElement>('button.update-indicator')
    expect(standaloneOnHome.exists()).toBe(true)
    expect(standaloneOnHome.text()).toContain('排空等待重启')
    const plainSettingsBtn = wrapper.find<HTMLButtonElement>('button[aria-label="设置"]')
    expect(plainSettingsBtn.exists()).toBe(true)
    expect(plainSettingsBtn.find('.dou-badge__mark').exists()).toBe(false)

    // Wide desktop inside project: standalone indicator is shown and settings badge stays hidden.
    state.workspaceLevel = 'coordinator'
    layoutMode.value = 'wide'
    await nextTick()
    expect(wrapper.find('button.update-indicator').exists()).toBe(true)
    expect(wrapper.find('button[aria-label="设置"] .dou-badge__mark').exists()).toBe(false)

    // Back to narrow project inner page with no pending update/restart: badge disappears.
    layoutMode.value = 'narrow'
    applyUpdateStatus(
      {
        ...baseUpdate,
        state: 'current',
        canRestart: false,
        restarting: false,
        restartPhase: '',
      },
      { trackVersion: false },
    )
    await nextTick()
    expect(wrapper.find('button.update-indicator').exists()).toBe(false)
    expect(wrapper.find('button[aria-label="设置"] .dou-badge__mark').exists()).toBe(false)
  })

  test('renders the pulse activity pill and toggles the pulse main view while hiding side-pane controls', async () => {
    state.detail = {
      project: {
        ...state.projects[0]!,
        settings: { autopilot: { enabled: false, method: 'squash' } },
      } as never,
      threads: [{ id: 'thr_run', title: '执行中', stage: 'working' } as never],
      needsMe: [],
      hasTracker: false,
    }
    const { wrapper } = mountHeader({ layoutMode: 'medium' })
    await nextTick()

    const pill = wrapper.get<HTMLButtonElement>('[data-pulse-pill]')
    expect(pill.attributes('aria-pressed')).toBe('false')
    expect(pill.text()).toContain('1 运行中')
    expect(wrapper.find('button[aria-label="收起项目面板"]').exists()).toBe(true)

    await pill.trigger('click')
    await nextTick()
    expect(state.main).toEqual({ kind: 'pulse' })
    expect(pill.attributes('aria-pressed')).toBe('true')
    expect(wrapper.find('button[aria-label="收起项目面板"]').exists()).toBe(false)

    await pill.trigger('click')
    await nextTick()
    expect(state.main).toEqual({ kind: 'conversation' })
    expect(wrapper.find('button[aria-label="收起项目面板"]').exists()).toBe(true)
  })
})
