// @vitest-environment happy-dom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, ref } from 'vue'
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
      models: {
        thread: { model: null, effort: null },
        coordinator: { model: null, effort: null },
        research: { model: null, effort: null },
      },
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
    const view = makeProjectSettingsView()
    view.settings.routines = [
      {
        id: 'rtn_1',
        prompt: '每天整理新 issue',
        days: [1, 2, 3, 4, 5],
        time: '09:00',
        enabled: true,
      },
    ]
    vi.spyOn(api, 'getProjectSettings').mockResolvedValue(view)
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
    expect(tabs).toHaveLength(6)
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

    const scopeId = wrapper
      .find('.project-settings-body')
      .element.getAttributeNames()
      .find((name) => name.startsWith('data-v-'))
    expect(scopeId).toBeTruthy()
    const dialogPanel = wrapper.find('[role="dialog"]')
    expect(dialogPanel.exists()).toBe(true)
    expect(dialogPanel.attributes(scopeId!)).toBe('')

    await wrapper.setProps({ layoutMode: 'narrow' })
    await flushPromises()
    expect(dialog().props('fullscreen')).toBe(true)
    expect(tabBar().props('placement')).toBe('top')
    expect(tabBar().props('height')).toBe('100%')

    state.projectSettingsTab = 'autopilot'
    await flushPromises()
    expect(wrapper.text()).not.toContain('每个讨论串最多用')
    expect(wrapper.text()).not.toContain('整个项目最多用')

    const routineTimePicker = wrapper.find('.project-routine-time.dou-picker')
    expect(routineTimePicker.exists()).toBe(true)
    expect(routineTimePicker.attributes(scopeId!)).toBe('')
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
    expect(wrapper.findAll('[role="tab"]')).toHaveLength(6)
  })

  test('tracks FileSourcesForm dirty state on usage tab and across close/reopen', async () => {
    const getProjectSettings = vi
      .spyOn(api, 'getProjectSettings')
      .mockResolvedValue(makeProjectSettingsView())
    vi.spyOn(api, 'getMcpSettings').mockResolvedValue({ servers: [], status: [] })
    vi.spyOn(api, 'getConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })

    const FileSourcesStub = defineComponent({
      name: 'FileSourcesForm',
      setup() {
        const dirty = ref(false)
        return { dirty, ready: true, save: async () => true }
      },
      render() {
        return h('div', { class: 'sources-stub' })
      },
    })

    const wrapper = mount(ProjectSettingsDialog, {
      props: { layoutMode: 'wide' },
      global: {
        stubs: {
          FileSourcesForm: FileSourcesStub,
          SourceNotes: true,
          MemoryList: true,
          ProjectUsage: true,
          SecretPairs: true,
        },
      },
    })
    wrappers.push(wrapper)

    state.projectSettingsOpen = true
    await flushPromises()
    expect(getProjectSettings).toHaveBeenCalledTimes(1)

    // Real Teleport preserves parent scopeId and reactive dialog classes on the panel.
    const bodyEl = document.body.querySelector('.project-settings-body')
    const scopeId = bodyEl?.getAttributeNames().find((name) => name.startsWith('data-v-'))
    const dialogPanel = document.body.querySelector('[role="dialog"]')
    expect(scopeId).toBeTruthy()
    expect(dialogPanel?.getAttribute(scopeId!)).toBe('')
    await wrapper.setProps({ layoutMode: 'medium' })
    await flushPromises()
    expect(dialogPanel?.classList.contains('project-settings-dialog-fullscreen')).toBe(true)
    expect(dialogPanel?.classList.contains('project-settings-dialog-narrow')).toBe(false)
    await wrapper.setProps({ layoutMode: 'narrow' })
    await flushPromises()
    expect(dialogPanel?.classList.contains('project-settings-dialog-fullscreen')).toBe(true)
    expect(dialogPanel?.classList.contains('project-settings-dialog-narrow')).toBe(true)
    await wrapper.setProps({ layoutMode: 'wide' })
    await flushPromises()

    // Clean usage tab hides Save and only shows Close.
    state.projectSettingsTab = 'usage'
    await flushPromises()
    expect(
      Array.from(document.body.querySelectorAll('button')).some(
        (b) => b.textContent?.trim() === '保存',
      ),
    ).toBe(false)

    // Dirty FileSourcesForm keeps Save visible on usage tab and preserves form across close/reopen.
    ;(wrapper.findComponent(FileSourcesStub).vm as unknown as { dirty: boolean }).dirty = true
    await flushPromises()
    expect(
      Array.from(document.body.querySelectorAll('button')).some(
        (b) => b.textContent?.trim() === '保存',
      ),
    ).toBe(true)

    state.projectSettingsOpen = false
    await flushPromises()
    expect(wrapper.findComponent(FileSourcesStub).exists()).toBe(true)

    state.projectSettingsOpen = true
    await flushPromises()
    expect(getProjectSettings).toHaveBeenCalledTimes(1)
  })

  test('edits and saves per-project thread, coordinator, and research model overrides', async () => {
    const view = makeProjectSettingsView()
    vi.spyOn(api, 'getProjectSettings').mockResolvedValue(view)
    vi.spyOn(api, 'getMcpSettings').mockResolvedValue({ servers: [], status: [] })
    vi.spyOn(api, 'getConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })
    const updateSpy = vi.spyOn(api, 'updateProject').mockResolvedValue({
      project: state.detail!.project,
      ...view,
    })
    vi.spyOn(api, 'getProject').mockResolvedValue({
      ...state.detail!,
      sync: { cursor: 'c1', partial: false },
    })
    const getConvSpy = vi.spyOn(api, 'getConversation').mockResolvedValue({
      conversation: {
        id: 'thr_coord',
        projectId: 'prj_test',
        role: 'coordinator',
        pendingSeq: 0,
        turn: null,
        lastOutcome: null,
        lastError: null,
        usage: state.detail!.project.usage,
        contextTokens: 0,
        compactions: 0,
        wait: null,
      },
      messages: [],
      notices: [],
      more: false,
      lastSeq: 0,
      todo: { conversationId: 'thr_coord', items: [], updatedAt: 0 },
      model: { id: 'm-global', contextTokens: 128000, compactAt: 96000, effort: 'low' },
    })

    state.currentId = 'prj_test'
    state.models = [
      { id: 'm-global', label: 'OpenAI / gpt-5', efforts: ['low', 'medium', 'high'] },
      { id: 'm-special', label: 'Claude Code / opus', efforts: ['low', 'medium', 'high', 'max'] },
    ]
    state.threadDefault = 'm-global'
    state.threadDefaultEffort = 'high'
    state.globalRoles = {
      thread: { model: 'm-global', effort: 'high' },
      coordinator: { model: null, effort: null },
      triage: { model: null, effort: null },
      research: { model: null, effort: null },
    }
    state.projectSettingsTab = 'models'

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

    const cascaders = wrapper.findAllComponents({ name: 'DouCascader' })
    expect(cascaders).toHaveLength(3)

    // Initially all three roles show dynamic global inheritance previews.
    expect(cascaders[0]!.props('options')[0].label).toBe('跟随全局（OpenAI / gpt-5 · 高）')
    expect(cascaders[1]!.props('options')[0].label).toBe('跟随全局（OpenAI / gpt-5 · 低）')
    expect(cascaders[2]!.props('options')[0].label).toBe('跟随全局（和提问的一方相同）')

    // Overriding thread model immediately updates coordinator's inherited preview.
    await cascaders[0]!.vm.$emit('update:modelValue', ['m-special', 'medium'])
    await flushPromises()
    expect(cascaders[1]!.props('options')[0].label).toBe('跟随全局（Claude Code / opus · 低）')

    // Override coordinator and research as well, then save.
    await cascaders[1]!.vm.$emit('update:modelValue', ['m-global', 'low'])
    await cascaders[2]!.vm.$emit('update:modelValue', ['m-special', ''])
    await flushPromises()

    const saveButton = wrapper.findAll('button').find((btn) => btn.text().trim() === '保存')
    expect(saveButton).toBeTruthy()
    await saveButton!.trigger('click')
    await flushPromises()

    expect(updateSpy).toHaveBeenCalledTimes(1)
    expect(updateSpy.mock.calls[0]?.[1]?.settings?.models).toEqual({
      thread: { model: 'm-special', effort: 'medium' },
      coordinator: { model: 'm-global', effort: 'low' },
      research: { model: 'm-special', effort: null },
    })
    expect(getConvSpy).toHaveBeenCalledWith('thr_coord', expect.any(Object))

    // Cleanup shared model state for subsequent tests.
    state.currentId = null
    state.models = []
    state.threadDefault = null
    state.threadDefaultEffort = null
    state.globalRoles = null
  })
})
