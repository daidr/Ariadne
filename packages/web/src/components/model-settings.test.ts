// @vitest-environment happy-dom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h } from 'vue'
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
})
