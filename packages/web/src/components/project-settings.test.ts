// @vitest-environment happy-dom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { api, type ProjectDetail, type ProjectSettingsView } from '../api'
import { locale } from '../i18n'
import { state } from '../store'
import ProjectSettingsDialog from './ProjectSettings.vue'

const wrappers: VueWrapper[] = []

function makeProjectSettingsView(): ProjectSettingsView {
  return {
    settings: {
      instructions: '',
      delivery: { mode: 'merge', instructions: '' },
      tools: [],
      workspace: {
        retention: 'auto',
        setup: '',
        check: '',
        auto: { setup: true, check: true },
        shell: 'auto',
        branchPrefix: '',
      },
      routines: [],
      review: { enabled: false },
      autopilot: { enabled: false, method: 'squash' },
    },
    detected: { setup: null, check: null },
    env: [],
    tools: ['fetch', 'search', 'commands', 'issueWrites'],
    hostedMerge: false,
  }
}

function makeProjectDetail(): ProjectDetail {
  const view = makeProjectSettingsView()
  return {
    project: {
      id: 'prj_test',
      name: '示例项目',
      unnamed: false,
      coordinatorId: 'thr_coord',
      createdAt: 1,
      updatedAt: 1,
      needsMe: 0,
      source: null,
      references: [],
      slots: null,
      mcp: [],
      settings: view.settings,
      detected: view.detected,
      autopilotPaused: false,
      usage: {
        input: 0,
        output: 0,
        cached: 0,
        cacheWrite: 0,
        reasoning: 0,
        cost: { USD: 0, CNY: 0 },
      },
    },
    threads: [],
    needsMe: [],
    hasTracker: false,
  }
}

beforeEach(() => {
  locale.value = 'zh-CN'
  state.detail = makeProjectDetail()
  state.projectSettingsOpen = false
  state.projectSettingsTab = 'source'
})

afterEach(() => {
  state.projectSettingsOpen = false
  state.detail = null
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  vi.restoreAllMocks()
})

describe('ProjectSettings', () => {
  test('renders icons for every tab and adapts dialog fullscreen and tab placement by layoutMode', async () => {
    vi.spyOn(api, 'getProjectSettings').mockResolvedValue(makeProjectSettingsView())
    vi.spyOn(api, 'getMcpSettings').mockResolvedValue({ servers: [], status: [] })
    vi.spyOn(api, 'getConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })

    const wrapper = mount(ProjectSettingsDialog, {
      props: { layoutMode: 'wide' },
      global: {
        stubs: {
          FileSourcesForm: true,
          SourceNotes: true,
          MemoryList: true,
          ProjectUsage: true,
          SecretPairs: true,
          teleport: true,
        },
      },
    })
    wrappers.push(wrapper)

    state.projectSettingsOpen = true
    await flushPromises()

    const tabs = wrapper.findAll('[role="tab"]')
    expect(tabs).toHaveLength(5)
    for (const tab of tabs) {
      expect(tab.find('.dou-tabs__icon').exists()).toBe(true)
    }

    const dialog = () => wrapper.findComponent({ name: 'DouDialog' })
    const tabBar = () => wrapper.findComponent({ name: 'DouTabs' })

    expect(dialog().props('fullscreen')).toBe(false)
    expect(tabBar().props('placement')).toBe('start')
    expect(tabBar().props('height')).toBe(560)

    await wrapper.setProps({ layoutMode: 'medium' })
    await flushPromises()
    expect(dialog().props('fullscreen')).toBe(true)
    expect(tabBar().props('placement')).toBe('start')
    expect(tabBar().props('height')).toBe('100%')

    await wrapper.setProps({ layoutMode: 'narrow' })
    await flushPromises()
    expect(dialog().props('fullscreen')).toBe(true)
    expect(tabBar().props('placement')).toBe('top')
    expect(tabBar().props('height')).toBe('100%')

    state.projectSettingsTab = 'autopilot'
    await flushPromises()
    expect(wrapper.text()).not.toContain('每个讨论串最多用')
    expect(wrapper.text()).not.toContain('整个项目最多用')
  })

  test('loads settings immediately when mounted with projectSettingsOpen already true', async () => {
    const getProjectSettings = vi
      .spyOn(api, 'getProjectSettings')
      .mockResolvedValue(makeProjectSettingsView())
    vi.spyOn(api, 'getMcpSettings').mockResolvedValue({ servers: [], status: [] })
    vi.spyOn(api, 'getConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })

    state.projectSettingsOpen = true

    const wrapper = mount(ProjectSettingsDialog, {
      props: { layoutMode: 'wide' },
      global: {
        stubs: {
          FileSourcesForm: true,
          SourceNotes: true,
          MemoryList: true,
          ProjectUsage: true,
          SecretPairs: true,
          teleport: true,
        },
      },
    })
    wrappers.push(wrapper)

    await flushPromises()

    expect(getProjectSettings).toHaveBeenCalledTimes(1)
    expect(getProjectSettings).toHaveBeenCalledWith('prj_test')
    expect(wrapper.findAll('[role="tab"]')).toHaveLength(5)
  })
})
