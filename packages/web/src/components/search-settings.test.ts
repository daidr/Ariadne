// @vitest-environment happy-dom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { api, type SearchSettings, type SearchSettingsInput } from '../api'
import { locale } from '../i18n'
import SearchSettingsView from './SearchSettings.vue'

const wrappers: VueWrapper[] = []

function makeSearchSettings(): SearchSettings {
  return {
    provider: 'tavily',
    ready: true,
    keySet: true,
    needed: false,
    models: ['OpenAI / gpt-5'],
    plugins: [{ 'zh-CN': 'Tavily', en: 'Tavily' }],
    services: [
      {
        kind: 'tavily',
        title: { 'zh-CN': 'Tavily', en: 'Tavily' },
        settings: {
          type: 'object',
          properties: {
            baseUrl: {
              type: 'string',
              title: { 'zh-CN': '接口地址', en: 'Base URL' },
              description: { 'zh-CN': '留空使用默认地址', en: 'Leave blank for default' },
              default: 'https://api.tavily.com',
            },
            apiKey: {
              type: 'string',
              title: { 'zh-CN': 'API Key', en: 'API key' },
              secret: true,
            },
          },
          required: ['apiKey'],
        },
        capabilities: { supportsCheck: true },
        values: { baseUrl: '' },
        secretsSet: ['apiKey'],
        ready: true,
      },
      {
        kind: 'custom-lua',
        title: { 'zh-CN': '自定义搜索', en: 'Custom Search' },
        description: { 'zh-CN': '外部 Lua 搜索插件', en: 'External Lua search plugin' },
        settings: {
          type: 'object',
          properties: {
            region: {
              type: 'enum',
              title: { 'zh-CN': '地区', en: 'Region' },
              options: ['cn', 'us'],
              default: 'cn',
            },
            strict: {
              type: 'boolean',
              title: { 'zh-CN': '严格过滤', en: 'Strict filter' },
              default: true,
            },
          },
        },
        capabilities: { supportsCheck: false },
        values: { region: 'cn', strict: true },
        secretsSet: [],
        ready: true,
      },
    ],
  }
}

beforeEach(() => {
  locale.value = 'zh-CN'
})

afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  vi.restoreAllMocks()
})

describe('SearchSettings', () => {
  test('renders registered search providers via SettingsFields and saves dynamic payload', async () => {
    const initial = makeSearchSettings()
    vi.spyOn(api, 'getSearchSettings').mockResolvedValue(initial)
    let savedInput: SearchSettingsInput | null = null
    vi.spyOn(api, 'saveSearchSettings').mockImplementation(async (input) => {
      savedInput = input
      return initial
    })

    const wrapper = mount(SearchSettingsView)
    wrappers.push(wrapper)
    await flushPromises()

    expect(wrapper.text()).toContain('OpenAI / gpt-5')
    expect(wrapper.text()).toContain('接口地址')
    expect(wrapper.text()).toContain('API Key')
    expect(wrapper.text()).toContain('测试连接')

    // Clearing the secret sends null on save.
    const clearBtn = wrapper.findAll('button').find((btn) => btn.text().includes('清除'))
    expect(clearBtn).toBeTruthy()
    await clearBtn!.trigger('click')

    const ok = await (wrapper.vm as unknown as { save: () => Promise<boolean> }).save()
    expect(ok).toBe(true)
    expect(savedInput).toEqual({
      provider: 'tavily',
      services: {
        tavily: { baseUrl: '', apiKey: null },
        'custom-lua': { region: 'cn', strict: true },
      },
    })
  })

  test('renders unset boolean default as enabled and saves false on first toggle', async () => {
    const initial = makeSearchSettings()
    initial.provider = 'custom-lua'
    initial.services[1]!.values = {}
    vi.spyOn(api, 'getSearchSettings').mockResolvedValue(initial)
    let savedInput: SearchSettingsInput | null = null
    vi.spyOn(api, 'saveSearchSettings').mockImplementation(async (input) => {
      savedInput = input
      return initial
    })

    const wrapper = mount(SearchSettingsView)
    wrappers.push(wrapper)
    await flushPromises()

    const toggle = wrapper.find('[role="switch"]')
    expect(toggle.exists()).toBe(true)
    expect(toggle.attributes('aria-checked')).toBe('true')

    await toggle.trigger('click')
    expect(toggle.attributes('aria-checked')).toBe('false')

    const ok = await (wrapper.vm as unknown as { save: () => Promise<boolean> }).save()
    expect(ok).toBe(true)
    expect(savedInput).toEqual({
      provider: 'custom-lua',
      services: {
        tavily: { baseUrl: '' },
        'custom-lua': { strict: false },
      },
    })
  })
})
