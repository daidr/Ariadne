// @vitest-environment happy-dom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, ref } from 'vue'
import { api, type ModelSettings, type ModelSettingsInput } from '../api'
import { locale } from '../i18n'
import { applyModelSettings, state } from '../store'
import ModelSettingsDialog from './ModelSettings.vue'

const wrappers: VueWrapper[] = []

const openaiKind: ModelSettings['kinds'][number] = {
  kind: 'openai-compatible',
  title: { 'zh-CN': 'OpenAI 兼容接口', en: 'OpenAI-compatible' },
  settings: {
    type: 'object',
    properties: {
      baseUrl: { type: 'string', title: { 'zh-CN': '接口地址', en: 'Base URL' } },
      apiKey: { type: 'string', title: { 'zh-CN': 'API Key', en: 'API key' }, secret: true },
    },
  },
}

const anthropicKind: ModelSettings['kinds'][number] = {
  kind: 'anthropic',
  title: { 'zh-CN': 'Anthropic 接口', en: 'Anthropic' },
  settings: {
    type: 'object',
    properties: {
      baseUrl: { type: 'string', title: { 'zh-CN': '接口地址', en: 'Base URL' } },
      apiKey: { type: 'string', title: { 'zh-CN': 'API Key', en: 'API key' }, secret: true },
    },
  },
}

function makeModelSettings(anthropicEnabled: boolean): ModelSettings {
  return {
    kinds: anthropicEnabled ? [openaiKind, anthropicKind] : [openaiKind],
    providers: [
      {
        id: 'p-oa',
        kind: 'openai-compatible',
        name: 'OpenAI',
        settings: { baseUrl: 'https://api.openai.com/v1' },
        secretsSet: ['apiKey'],
        headers: [],
        extraBody: {},
        timeoutSeconds: null,
      },
      {
        id: 'p-anth',
        kind: 'anthropic',
        name: 'Claude',
        settings: anthropicEnabled ? { baseUrl: 'https://proxy.anthropic.example' } : {},
        secretsSet: anthropicEnabled ? ['apiKey'] : [],
        headers: [],
        extraBody: {},
        timeoutSeconds: null,
      },
    ],
    models: [
      { id: 'm-oa', provider: 'p-oa', model: 'gpt-5', contextTokens: 128000 },
      {
        id: 'm-anth',
        provider: 'p-anth',
        model: 'claude-sonnet',
        contextTokens: 200000,
      },
    ],
    roles: {
      thread: { model: 'm-oa', effort: null },
      coordinator: { model: null, effort: null },
      triage: { model: null, effort: null },
      research: { model: null, effort: null },
    },
    showCost: false,
  }
}

beforeEach(() => {
  locale.value = 'zh-CN'
})

afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  vi.restoreAllMocks()
})

describe('ModelSettings', () => {
  test('hydrates settings and secretsSet when a disabled model plugin is re-enabled in the same dialog', async () => {
    let anthropicEnabled = false
    vi.spyOn(api, 'getModelSettings').mockImplementation(async () =>
      makeModelSettings(anthropicEnabled),
    )
    vi.spyOn(api, 'getConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })
    vi.spyOn(api, 'saveConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })
    let savedInput: ModelSettingsInput | null = null
    vi.spyOn(api, 'saveModelSettings').mockImplementation(async (input) => {
      savedInput = input
      return { ...makeModelSettings(anthropicEnabled), problems: [] }
    })

    const PluginSettingsStub = defineComponent({
      name: 'PluginSettings',
      emits: ['changed'],
      setup(_, { emit }) {
        return () =>
          h(
            'button',
            {
              'data-testid': 'reenable-anthropic',
              onClick: () => {
                anthropicEnabled = true
                emit('changed')
              },
            },
            're-enable',
          )
      },
    })

    const wrapper = mount(ModelSettingsDialog, {
      props: { open: false, tab: 'models' },
      global: {
        stubs: {
          PluginSettings: PluginSettingsStub,
          SearchSettings: true,
          WorkSettings: true,
          InputSettings: true,
          McpSettings: true,
          DataTab: true,
          UpdatesSettings: true,
          teleport: true,
        },
      },
    })
    wrappers.push(wrapper)

    await wrapper.setProps({ open: true })
    await flushPromises()

    // Switch to Plugins tab and re-enable Anthropic in the same open dialog.
    await wrapper.setProps({ tab: 'plugins' })
    await flushPromises()
    await wrapper.find('[data-testid="reenable-anthropic"]').trigger('click')
    await flushPromises()

    // Switch back to Models tab: both baseUrl and apiKey secret state should be hydrated.
    await wrapper.setProps({ tab: 'models' })
    await flushPromises()
    const clearSecretButtons = wrapper
      .findAll('button')
      .filter((button) => button.text().includes('清除'))
    expect(clearSecretButtons.length).toBeGreaterThanOrEqual(2)

    const saveButton = wrapper.findAll('button').find((button) => button.text().trim() === '保存')
    expect(saveButton).toBeTruthy()
    await saveButton!.trigger('click')
    await flushPromises()

    expect(savedInput).not.toBeNull()
    const anthropicProvider = savedInput!.providers.find((provider) => provider.id === 'p-anth')
    expect(anthropicProvider?.settings).toEqual({ baseUrl: 'https://proxy.anthropic.example' })
  })

  test('filters models belonging to disabled built-in providers out of chat model choices', () => {
    applyModelSettings(makeModelSettings(false))
    expect(state.models.map((model) => model.id)).toEqual(['m-oa'])

    applyModelSettings(makeModelSettings(true))
    expect(state.models.map((model) => model.id)).toEqual(['m-oa', 'm-anth'])
  })

  test('removes fallback field and lets research model pick model and effort via DouCascader', async () => {
    const initial = makeModelSettings(true)
    initial.models[0]!.efforts = ['low', 'medium', 'high']
    initial.models[1]!.efforts = ['low', 'medium', 'high', 'max']
    initial.roles.research = { model: null, effort: 'low' }

    vi.spyOn(api, 'getModelSettings').mockResolvedValue(initial)
    vi.spyOn(api, 'getConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })
    vi.spyOn(api, 'saveConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })
    let savedInput: ModelSettingsInput | null = null
    vi.spyOn(api, 'saveModelSettings').mockImplementation(async (input) => {
      savedInput = input
      return {
        ...initial,
        roles: input.roles,
        problems: [],
      }
    })

    const wrapper = mount(ModelSettingsDialog, {
      props: { open: false, tab: 'models' },
      global: {
        stubs: {
          PluginSettings: true,
          SearchSettings: true,
          WorkSettings: true,
          InputSettings: true,
          McpSettings: true,
          DataTab: true,
          UpdatesSettings: true,
          teleport: true,
        },
      },
    })
    wrappers.push(wrapper)

    await wrapper.setProps({ open: true })
    await flushPromises()

    // Fallback field is no longer rendered.
    expect(wrapper.text()).not.toContain('备用模型')

    // All three visible roles (thread, coordinator, research) use DouCascader.
    const cascaders = wrapper.findAllComponents({ name: 'DouCascader' })
    expect(cascaders).toHaveLength(3)
    const researchCascader = cascaders[2]!
    expect(researchCascader.props('modelValue')).toEqual(['', 'low'])

    // Update research role to explicit model + effort and save.
    await researchCascader.vm.$emit('update:modelValue', ['m-anth', 'max'])
    await flushPromises()

    const saveButton = wrapper.findAll('button').find((button) => button.text().trim() === '保存')
    expect(saveButton).toBeTruthy()
    await saveButton!.trigger('click')
    await flushPromises()

    expect(savedInput).not.toBeNull()
    expect(savedInput!.roles.research).toEqual({ model: 'm-anth', effort: 'max' })
    expect('fallback' in savedInput!.roles.thread).toBe(false)
    expect('fallback' in savedInput!.roles.coordinator).toBe(false)
    expect('fallback' in savedInput!.roles.research).toBe(false)
  })

  test('defaults coordinator to low effort when inherited thread model supports low and preserves explicit overrides', async () => {
    const initial = makeModelSettings(true)
    initial.models = [
      { id: 'm-oa', provider: 'p-oa', model: 'gpt-5', contextTokens: 128000 },
      {
        id: 'm-medhigh',
        provider: 'p-anth',
        model: 'claude-medhigh',
        contextTokens: 200000,
        efforts: ['medium', 'high'],
      },
      {
        id: 'm-plain',
        provider: 'p-anth',
        model: 'claude-plain',
        contextTokens: 200000,
        efforts: [],
      },
    ]
    initial.roles.thread = { model: 'm-oa', effort: 'high' }
    initial.roles.coordinator = { model: null, effort: null }

    vi.spyOn(api, 'getModelSettings').mockResolvedValue(initial)
    vi.spyOn(api, 'getConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })
    vi.spyOn(api, 'saveConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })
    let savedInput: ModelSettingsInput | null = null
    vi.spyOn(api, 'saveModelSettings').mockImplementation(async (input) => {
      savedInput = input
      return {
        ...initial,
        roles: input.roles,
        problems: [],
      }
    })

    const wrapper = mount(ModelSettingsDialog, {
      props: { open: false, tab: 'models' },
      global: {
        stubs: {
          PluginSettings: true,
          SearchSettings: true,
          WorkSettings: true,
          InputSettings: true,
          McpSettings: true,
          DataTab: true,
          UpdatesSettings: true,
          teleport: true,
        },
      },
    })
    wrappers.push(wrapper)

    await wrapper.setProps({ open: true })
    await flushPromises()

    const cascaders = () => wrapper.findAllComponents({ name: 'DouCascader' })

    // Inherited model (unknown efforts -> common low/medium/high) defaults coordinator to ['','low'] ("沿用讨论串模型 · 低").
    expect(cascaders()[1]!.props('modelValue')).toEqual(['', 'low'])
    expect(cascaders()[1]!.text()).toContain('沿用讨论串模型 · 低')

    // Saving untouched keeps coordinator role unset so it stays adaptive.
    const saveButton = () =>
      wrapper.findAll('button').find((button) => button.text().trim() === '保存')!
    await saveButton().trigger('click')
    await flushPromises()
    expect(savedInput!.roles.coordinator).toEqual({ model: null, effort: null })

    // Reopen and switch thread model to one without low effort: coordinator falls back to default effort.
    await wrapper.setProps({ open: true })
    await flushPromises()
    await cascaders()[0]!.vm.$emit('update:modelValue', ['m-medhigh', 'high'])
    await flushPromises()
    expect(cascaders()[1]!.props('modelValue')).toEqual(['', ''])
    expect(cascaders()[1]!.text()).toContain('沿用讨论串模型 · 默认')

    // Switch thread model to one without any effort support: coordinator has no effort suffix.
    await cascaders()[0]!.vm.$emit('update:modelValue', ['m-plain'])
    await flushPromises()
    expect(cascaders()[1]!.props('modelValue')).toEqual([''])

    // Switch back to m-oa and explicitly pick "沿用讨论串模型 · 默认" -> persists effort: 'default'.
    await cascaders()[0]!.vm.$emit('update:modelValue', ['m-oa', 'high'])
    await flushPromises()
    expect(cascaders()[1]!.props('modelValue')).toEqual(['', 'low'])
    await cascaders()[1]!.vm.$emit('update:modelValue', ['', ''])
    await flushPromises()
    expect(cascaders()[1]!.props('modelValue')).toEqual(['', ''])
    expect(cascaders()[1]!.text()).toContain('沿用讨论串模型 · 默认')
    await saveButton().trigger('click')
    await flushPromises()
    expect(savedInput!.roles.coordinator).toEqual({ model: null, effort: 'default' })

    // Reopen and pick an explicit model with default effort -> saves effort: null.
    await wrapper.setProps({ open: true })
    await flushPromises()
    await cascaders()[1]!.vm.$emit('update:modelValue', ['m-medhigh', ''])
    await flushPromises()
    expect(cascaders()[1]!.props('modelValue')).toEqual(['m-medhigh', ''])
    await saveButton().trigger('click')
    await flushPromises()
    expect(savedInput!.roles.coordinator).toEqual({ model: 'm-medhigh', effort: null })
  })

  test('renders icons for every tab and adapts dialog fullscreen and tab placement by layoutMode', async () => {
    vi.spyOn(api, 'getModelSettings').mockResolvedValue(makeModelSettings(false))
    vi.spyOn(api, 'getConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })

    const wrapper = mount(ModelSettingsDialog, {
      props: { open: false, tab: 'models', layoutMode: 'wide' },
      global: {
        stubs: {
          PluginSettings: true,
          SearchSettings: true,
          WorkSettings: true,
          InputSettings: true,
          McpSettings: true,
          DataTab: true,
          UpdatesSettings: true,
          teleport: true,
        },
      },
    })
    wrappers.push(wrapper)

    await wrapper.setProps({ open: true })
    await flushPromises()

    const tabs = wrapper.findAll('[role="tab"]')
    expect(tabs).toHaveLength(9)
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
  })

  test('unmounts child tabs on clean close so reopening loads fresh data, but preserves dirty tabs', async () => {
    const getModelSettings = vi
      .spyOn(api, 'getModelSettings')
      .mockResolvedValue(makeModelSettings(false))
    vi.spyOn(api, 'getConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })

    const mcpMounted = vi.fn()
    const McpStub = defineComponent({
      name: 'McpSettings',
      setup() {
        const dirty = ref(false)
        mcpMounted()
        return { dirty, save: async () => true }
      },
      render() {
        return h('div', { class: 'mcp-stub' })
      },
    })

    const wrapper = mount(ModelSettingsDialog, {
      props: { open: false, tab: 'mcp' },
      global: {
        stubs: {
          PluginSettings: true,
          SearchSettings: true,
          WorkSettings: true,
          InputSettings: true,
          McpSettings: McpStub,
          DataTab: true,
          UpdatesSettings: true,
        },
      },
    })
    wrappers.push(wrapper)

    await wrapper.setProps({ open: true })
    await flushPromises()
    expect(mcpMounted).toHaveBeenCalledTimes(1)
    expect(getModelSettings).toHaveBeenCalledTimes(1)

    // Clean close resets mounted so reopening remounts McpSettings and reloads settings.
    await wrapper.setProps({ open: false })
    await flushPromises()
    expect(wrapper.findComponent(McpStub).exists()).toBe(false)

    await wrapper.setProps({ open: true })
    await flushPromises()
    expect(mcpMounted).toHaveBeenCalledTimes(2)
    expect(getModelSettings).toHaveBeenCalledTimes(2)

    // Dirty child stays mounted across accidental close/reopen.
    ;(wrapper.findComponent(McpStub).vm as unknown as { dirty: boolean }).dirty = true
    await flushPromises()
    await wrapper.setProps({ open: false })
    await flushPromises()
    expect(wrapper.findComponent(McpStub).exists()).toBe(true)

    await wrapper.setProps({ open: true })
    await flushPromises()
    expect(mcpMounted).toHaveBeenCalledTimes(2)
    expect(getModelSettings).toHaveBeenCalledTimes(2)
  })

  test('keeps model row controls grouped and renders aligned model detail grid with distinct currency label', async () => {
    const initial = makeModelSettings(true)
    initial.models[0] = {
      ...initial.models[0]!,
      maxTokens: 32768,
      price: { currency: 'USD', input: 2, output: 10, cacheRead: 0.1 },
    }
    vi.spyOn(api, 'getModelSettings').mockResolvedValue(initial)
    vi.spyOn(api, 'getConcurrency').mockResolvedValue({ slots: 3, reserved: 0 })

    const wrapper = mount(ModelSettingsDialog, {
      props: { open: false, tab: 'models' },
      global: {
        stubs: {
          PluginSettings: true,
          SearchSettings: true,
          WorkSettings: true,
          InputSettings: true,
          McpSettings: true,
          DataTab: true,
          UpdatesSettings: true,
          teleport: true,
        },
      },
    })
    wrappers.push(wrapper)

    await wrapper.setProps({ open: true })
    await flushPromises()

    const modelRows = wrapper.findAll('.settings-model-row')
    expect(modelRows).toHaveLength(2)
    for (const row of modelRows) {
      expect(row.find('.settings-model-actions').findAll('button')).toHaveLength(2)
    }

    // Expand the first model and verify detail layout sections and accessible labels.
    await modelRows[0]!.find('.settings-model-actions button').trigger('click')
    await flushPromises()

    const detail = wrapper.find('.settings-model-more')
    expect(detail.exists()).toBe(true)
    expect(detail.findAll('.settings-model-grid')).toHaveLength(2)
    expect(detail.findAll('.settings-number')).toHaveLength(6)
    expect(detail.text()).toContain('价格（每 1M token）')

    const currencySelect = detail.find('.settings-currency [role="combobox"]')
    expect(currencySelect.exists()).toBe(true)
    expect(currencySelect.attributes('aria-label')).toBe('币种')
    expect(currencySelect.attributes('aria-labelledby')).toBeUndefined()

    const imageSwitch = detail.find('[role="switch"]')
    expect(imageSwitch.exists()).toBe(true)
    expect(imageSwitch.attributes('aria-label')).toBe('看图片')
  })
})
