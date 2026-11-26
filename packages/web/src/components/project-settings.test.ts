// @vitest-environment happy-dom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, ref } from 'vue'
import { api, type ModelSettings, type ProjectDetail, type ProjectSettingsView } from '../api'
import { locale } from '../i18n'
import { openSettingsKey } from '../open-settings'
import { state } from '../store'
import ProjectSettingsDialog from './ProjectSettings.vue'

const wrappers: VueWrapper[] = []

function makeModelSettings(): ModelSettings {
  return {
    kinds: [
      {
        kind: 'openai-compatible',
        title: { 'zh-CN': 'OpenAI', en: 'OpenAI' },
        settings: { type: 'object', properties: {} },
      },
      {
        kind: 'anthropic',
        title: { 'zh-CN': 'Anthropic', en: 'Anthropic' },
        settings: { type: 'object', properties: {} },
      },
    ],
    providers: [
      {
        id: 'p-oa',
        kind: 'openai-compatible',
        name: 'OpenAI',
        settings: {},
        secretsSet: [],
        headers: [],
        extraBody: {},
        timeoutSeconds: null,
      },
      {
        id: 'p-anth',
        kind: 'anthropic',
        name: 'Claude',
        settings: {},
        secretsSet: [],
        headers: [],
        extraBody: {},
        timeoutSeconds: null,
      },
    ],
    models: [
      {
        id: 'm-gpt',
        provider: 'p-oa',
        model: 'gpt-5',
        contextTokens: 128000,
        efforts: ['low', 'medium', 'high'],
      },
      {
        id: 'm-opus',
        provider: 'p-anth',
        model: 'claude-opus',
        contextTokens: 200000,
        efforts: ['low', 'medium', 'high', 'max'],
      },
      {
        id: 'm-haiku',
        provider: 'p-anth',
        model: 'claude-haiku',
        contextTokens: 100000,
        efforts: ['low', 'medium'],
      },
    ],
    roles: {
      thread: { model: 'm-gpt', effort: 'high' },
      coordinator: { model: null, effort: null },
      triage: { model: null, effort: null },
      research: { model: 'm-haiku', effort: 'low' },
    },
    showCost: false,
  }
}

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
      models: { thread: null, coordinator: null, research: null },
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
  state.currentId = 'prj_test'
  state.detail = makeProjectDetail()
  state.projectSettingsOpen = false
  state.projectSettingsTab = 'source'
  state.models = []
  state.threadDefault = null
  state.threadDefaultEffort = null
})

afterEach(() => {
  state.projectSettingsOpen = false
  state.detail = null
  state.currentId = null
  state.models = []
  state.threadDefault = null
  state.threadDefaultEffort = null
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
    vi.spyOn(api, 'getModelSettings').mockResolvedValue(makeModelSettings())

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
    vi.spyOn(api, 'getModelSettings').mockResolvedValue(makeModelSettings())

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
    vi.spyOn(api, 'getModelSettings').mockResolvedValue(makeModelSettings())

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

  test('displays inherited global models by default, supports cascading overrides, one-click clear, and saves', async () => {
    const view = makeProjectSettingsView()
    vi.spyOn(api, 'getProjectSettings').mockResolvedValue(view)
    vi.spyOn(api, 'getMcpSettings').mockResolvedValue({ servers: [], status: [] })
    vi.spyOn(api, 'getConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })
    vi.spyOn(api, 'getModelSettings').mockResolvedValue(makeModelSettings())

    let updatedPatch: Parameters<typeof api.updateProject>[1] | null = null
    vi.spyOn(api, 'updateProject').mockImplementation(async (_id, patch) => {
      updatedPatch = patch
      const nextSettings = patch.settings ?? view.settings
      return {
        ...view,
        settings: nextSettings,
        project: {
          ...state.detail!.project,
          settings: nextSettings,
        },
      }
    })
    vi.spyOn(api, 'getProject').mockResolvedValue({
      ...state.detail!,
      sync: { cursor: '1', partial: false },
    })
    const getConversationSpy = vi.spyOn(api, 'getConversation').mockResolvedValue({
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
      lastSeq: 1,
      todo: { conversationId: 'thr_coord', items: [], updatedAt: 0 },
      model: { id: 'm-opus', contextTokens: 200000, compactAt: 150000, effort: 'low' },
    })

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

    const cascaders = () => wrapper.findAllComponents({ name: 'DouCascader' })
    expect(cascaders()).toHaveLength(3)

    // Default inherited placeholders show resolved global settings (including coordinator's default low effort).
    expect(cascaders()[0]!.props('modelValue')).toBeNull()
    expect(cascaders()[0]!.props('placeholder')).toBe('沿用全局设置（gpt-5 · 高）')
    expect(cascaders()[1]!.props('modelValue')).toBeNull()
    expect(cascaders()[1]!.props('placeholder')).toBe('沿用全局设置（gpt-5 · 低）')
    expect(cascaders()[2]!.props('modelValue')).toBeNull()
    expect(cascaders()[2]!.props('placeholder')).toBe('沿用全局设置（claude-haiku · 低）')

    // Overriding project thread model dynamically updates the inherited coordinator placeholder.
    cascaders()[0]!.vm.$emit('update:modelValue', ['m-opus', 'max'])
    await flushPromises()
    expect(cascaders()[0]!.props('modelValue')).toEqual(['m-opus', 'max'])
    expect(cascaders()[1]!.props('placeholder')).toBe('沿用全局设置（claude-opus · 低）')

    // Overriding coordinator to "same as thread · default" and research to "same as asking · medium".
    cascaders()[1]!.vm.$emit('update:modelValue', ['', ''])
    cascaders()[2]!.vm.$emit('update:modelValue', ['', 'medium'])
    await flushPromises()
    expect(cascaders()[1]!.props('modelValue')).toEqual(['', ''])
    expect(cascaders()[2]!.props('modelValue')).toEqual(['', 'medium'])

    // One-click clear resets coordinator back to inheriting global setting.
    const clearButtons = wrapper.findAll('button.dou-picker__clear')
    expect(clearButtons.length).toBe(3)
    await clearButtons[1]!.trigger('click')
    await flushPromises()
    expect(cascaders()[1]!.props('modelValue')).toBeNull()
    expect(cascaders()[1]!.props('placeholder')).toBe('沿用全局设置（claude-opus · 低）')

    const saveButton = wrapper.findAll('button').find((button) => button.text().trim() === '保存')
    expect(saveButton).toBeTruthy()
    await saveButton!.trigger('click')
    await flushPromises()

    expect(updatedPatch).not.toBeNull()
    expect(updatedPatch!.settings?.models).toEqual({
      thread: { model: 'm-opus', effort: 'max' },
      coordinator: null,
      research: { model: null, effort: 'medium' },
    })
    expect(getConversationSpy).toHaveBeenCalled()
  })

  test('shows an action to open global model settings when no models are configured', async () => {
    const emptyModels: ModelSettings = {
      ...makeModelSettings(),
      providers: [],
      models: [],
      roles: {
        thread: { model: null, effort: null },
        coordinator: { model: null, effort: null },
        triage: { model: null, effort: null },
        research: { model: null, effort: null },
      },
    }
    vi.spyOn(api, 'getProjectSettings').mockResolvedValue(makeProjectSettingsView())
    vi.spyOn(api, 'getMcpSettings').mockResolvedValue({ servers: [], status: [] })
    vi.spyOn(api, 'getConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })
    vi.spyOn(api, 'getModelSettings').mockResolvedValue(emptyModels)
    const openSettings = vi.fn()

    state.projectSettingsTab = 'models'
    const wrapper = mount(ProjectSettingsDialog, {
      props: { layoutMode: 'wide' },
      global: {
        provide: { [openSettingsKey as symbol]: openSettings },
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

    expect(wrapper.findAllComponents({ name: 'DouCascader' })).toHaveLength(0)
    const openButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('前往全局设置添加模型'))
    expect(openButton).toBeTruthy()
    await openButton!.trigger('click')
    await flushPromises()

    expect(state.projectSettingsOpen).toBe(false)
    expect(openSettings).toHaveBeenCalledWith('models')
  })

  test('refreshes global model settings when reopening with unsaved draft without overwriting draft', async () => {
    const emptyModels: ModelSettings = {
      ...makeModelSettings(),
      providers: [],
      models: [],
      roles: {
        thread: { model: null, effort: null },
        coordinator: { model: null, effort: null },
        triage: { model: null, effort: null },
        research: { model: null, effort: null },
      },
    }
    const updatedModels = makeModelSettings()
    const getProjectSettings = vi
      .spyOn(api, 'getProjectSettings')
      .mockResolvedValue(makeProjectSettingsView())
    vi.spyOn(api, 'getMcpSettings').mockResolvedValue({ servers: [], status: [] })
    vi.spyOn(api, 'getConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })
    const getModelSettings = vi
      .spyOn(api, 'getModelSettings')
      .mockResolvedValueOnce(emptyModels)
      .mockResolvedValueOnce(updatedModels)

    state.projectSettingsTab = 'instructions'
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
    expect(getProjectSettings).toHaveBeenCalledTimes(1)
    expect(getModelSettings).toHaveBeenCalledTimes(1)

    // Edit instructions to create an unsaved draft.
    const instructionsTextarea = wrapper.find('textarea')
    expect(instructionsTextarea.exists()).toBe(true)
    await instructionsTextarea.setValue('保留这段未保存草稿')
    await flushPromises()

    // Switch to models tab and verify empty state before adding models globally.
    state.projectSettingsTab = 'models'
    await flushPromises()
    expect(wrapper.findAllComponents({ name: 'DouCascader' })).toHaveLength(0)

    // Close dialog (draft stays preserved) and reopen after adding global models.
    state.projectSettingsOpen = false
    await flushPromises()
    state.projectSettingsOpen = true
    await flushPromises()

    expect(getProjectSettings).toHaveBeenCalledTimes(1)
    expect(getModelSettings).toHaveBeenCalledTimes(2)
    const cascaders = wrapper.findAllComponents({ name: 'DouCascader' })
    expect(cascaders).toHaveLength(3)
    expect(cascaders[0]!.props('placeholder')).toBe('沿用全局设置（gpt-5 · 高）')
    expect(cascaders[1]!.props('placeholder')).toBe('沿用全局设置（gpt-5 · 低）')
    expect(cascaders[2]!.props('placeholder')).toBe('沿用全局设置（claude-haiku · 低）')

    // Switch back to instructions tab and confirm unsaved draft was preserved.
    state.projectSettingsTab = 'instructions'
    await flushPromises()
    expect(wrapper.find('textarea').element.value).toBe('保留这段未保存草稿')
  })

  test('falls back to plain inherit placeholder when global model fetch fails or points to a disabled plugin', async () => {
    vi.spyOn(api, 'getProjectSettings').mockResolvedValue(makeProjectSettingsView())
    vi.spyOn(api, 'getMcpSettings').mockResolvedValue({ servers: [], status: [] })
    vi.spyOn(api, 'getConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })

    // Case 1: getModelSettings fails while state.models already has cached models.
    state.models = [{ id: 'm-gpt', label: 'gpt-5', efforts: ['low', 'medium', 'high'] }]
    state.threadDefault = 'm-gpt'
    state.threadDefaultEffort = 'high'
    const getModelSettings = vi
      .spyOn(api, 'getModelSettings')
      .mockRejectedValueOnce(new Error('network error'))

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

    let cascaders = wrapper.findAllComponents({ name: 'DouCascader' })
    expect(cascaders).toHaveLength(3)
    expect(cascaders[1]!.props('placeholder')).toBe('沿用全局设置')
    expect(cascaders[2]!.props('placeholder')).toBe('沿用全局设置')

    // Case 2: global coordinator and research roles explicitly point to a model from a disabled plugin.
    const disabledPluginModels: ModelSettings = {
      ...makeModelSettings(),
      kinds: [
        {
          kind: 'openai-compatible',
          title: { 'zh-CN': 'OpenAI', en: 'OpenAI' },
          settings: { type: 'object', properties: {} },
        },
      ],
      roles: {
        thread: { model: 'm-gpt', effort: 'high' },
        coordinator: { model: 'm-opus', effort: 'high' },
        triage: { model: null, effort: null },
        research: { model: 'm-haiku', effort: 'low' },
      },
    }
    getModelSettings.mockResolvedValueOnce(disabledPluginModels)

    state.projectSettingsOpen = false
    await flushPromises()
    state.projectSettingsOpen = true
    await flushPromises()

    cascaders = wrapper.findAllComponents({ name: 'DouCascader' })
    expect(cascaders).toHaveLength(3)
    expect(cascaders[0]!.props('placeholder')).toBe('沿用全局设置（gpt-5 · 高）')
    expect(cascaders[1]!.props('placeholder')).toBe('沿用全局设置')
    expect(cascaders[2]!.props('placeholder')).toBe('沿用全局设置')
  })
})
