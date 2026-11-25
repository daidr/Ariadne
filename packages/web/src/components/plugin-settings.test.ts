// @vitest-environment happy-dom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { api, type PluginRecord } from '../api'
import { locale } from '../i18n'
import PluginSettings from './PluginSettings.vue'
import {
  formatPluginUsage,
  hasPluginUsage,
  pluginCapabilities,
  splitPlugins,
} from './plugin-settings'

const wrappers: VueWrapper[] = []

function record(partial: Partial<PluginRecord> & { id: string }): PluginRecord {
  return {
    name: { 'zh-CN': partial.id, en: partial.id },
    source: 'builtin',
    path: null,
    status: 'loaded',
    error: null,
    provides: {
      models: [],
      contexts: [],
      dashboards: [],
      searches: [],
      links: [],
      notifiers: [],
    },
    usage: {
      modelProviders: [],
      modelRoles: [],
      searches: [],
      projects: [],
    },
    ...partial,
  }
}

beforeEach(() => {
  locale.value = 'zh-CN'
})

afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  vi.restoreAllMocks()
})

describe('plugin-settings helpers', () => {
  test('splits third-party and built-in plugins cleanly', () => {
    const items = [
      record({ id: 'model-openai', source: 'builtin' }),
      record({ id: 'custom-lua', source: 'path', path: '/tmp/custom.lua' }),
      record({ id: 'search-tavily', source: 'builtin', status: 'disabled' }),
    ]
    const { thirdParty, builtins } = splitPlugins(items)
    expect(thirdParty.map((p) => p.id)).toEqual(['custom-lua'])
    expect(builtins.map((p) => p.id)).toEqual(['model-openai', 'search-tavily'])
  })

  test('derives multi-capability categories in display order', () => {
    const github = record({
      id: 'github',
      provides: {
        models: [],
        contexts: ['github'],
        dashboards: [],
        searches: [],
        links: ['github'],
        notifiers: [],
      },
    })
    const hybrid = record({
      id: 'hybrid',
      provides: {
        models: ['custom-model'],
        contexts: [],
        dashboards: ['metrics'],
        searches: ['custom-search'],
        links: [],
        notifiers: ['custom-push'],
      },
    })
    expect(pluginCapabilities(github)).toEqual(['contexts', 'links'])
    expect(pluginCapabilities(hybrid)).toEqual(['models', 'searches', 'dashboards', 'notifiers'])
  })

  test('formats usage summaries across model providers, roles, search, and projects', () => {
    expect(hasPluginUsage(record({ id: 'empty' }))).toBe(false)

    const used = record({
      id: 'multi',
      usage: {
        modelProviders: ['OpenAI', 'DeepSeek'],
        modelRoles: ['thread', 'research'],
        searches: ['tavily'],
        projects: ['Ariadne', 'Demo', 'Web', 'CLI'],
      },
    })
    expect(hasPluginUsage(used)).toBe(true)
    expect(formatPluginUsage(used.usage)).toBe(
      '模型提供方 OpenAI、DeepSeek（讨论串默认、子调研）；当前联网搜索；项目 Ariadne、Demo 等 4 个项目',
    )

    locale.value = 'en'
    expect(formatPluginUsage(used.usage)).toBe(
      'Model provider OpenAI, DeepSeek (Thread default, Research); Current web search; 4 projects (Ariadne, Demo, ...)',
    )
    expect(
      formatPluginUsage({
        modelProviders: ['Claude'],
        modelRoles: [],
        searches: [],
        projects: ['Ariadne'],
      }),
    ).toBe('Model provider Claude; Project Ariadne')
  })
})

describe('PluginSettings component', () => {
  test('shows readable capability tags without internal IDs and confirms before disabling in-use built-in plugins', async () => {
    const initialPlugins: PluginRecord[] = [
      record({
        id: 'model-openai',
        name: { 'zh-CN': 'OpenAI 兼容接口', en: 'OpenAI-compatible' },
        provides: {
          models: ['openai-compatible'],
          contexts: [],
          dashboards: [],
          searches: [],
          links: [],
          notifiers: [],
        },
        usage: {
          modelProviders: ['OpenAI'],
          modelRoles: ['thread'],
          searches: [],
          projects: [],
        },
      }),
      record({
        id: 'search-deepseek',
        name: { 'zh-CN': 'DeepSeek 搜索', en: 'DeepSeek Search' },
        provides: {
          models: [],
          contexts: [],
          dashboards: [],
          searches: ['deepseek'],
          links: [],
          notifiers: [],
        },
        usage: {
          modelProviders: [],
          modelRoles: [],
          searches: [],
          projects: [],
        },
      }),
      record({
        id: 'github',
        name: { 'zh-CN': 'GitHub', en: 'GitHub' },
        provides: {
          models: [],
          contexts: ['github'],
          dashboards: [],
          searches: [],
          links: ['github'],
          notifiers: [],
        },
        usage: {
          modelProviders: [],
          modelRoles: [],
          searches: [],
          projects: ['Ariadne'],
        },
      }),
    ]

    vi.spyOn(api, 'listPlugins').mockResolvedValue({ plugins: initialPlugins })
    const setEnabledSpy = vi
      .spyOn(api, 'setPluginEnabled')
      .mockImplementation(async (id, enabled) => ({
        plugins: initialPlugins.map((plugin) =>
          plugin.id === id ? { ...plugin, status: enabled ? 'loaded' : 'disabled' } : plugin,
        ),
      }))

    const wrapper = mount(PluginSettings)
    wrappers.push(wrapper)
    await flushPromises()

    // Expand built-in plugins collapsible section.
    const collapseTrigger = wrapper
      .findAll('button')
      .find((button) => button.text().includes('内置插件'))
    expect(collapseTrigger).toBeTruthy()
    await collapseTrigger!.trigger('click')
    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('OpenAI 兼容接口')
    expect(text).toContain('模型')
    expect(text).toContain('文件源')
    expect(text).toContain('链接预览')
    expect(text).toContain('使用中：模型提供方 OpenAI（讨论串默认）')
    expect(text).toContain('使用中：项目 Ariadne')
    expect(text).not.toContain('openai-compatible')
    expect(text).not.toContain('提供：')

    const switches = wrapper.findAll('button[role="switch"]')
    expect(switches).toHaveLength(3)

    // Disabling an unused plugin applies immediately without Popconfirm.
    await switches[1]!.trigger('click')
    await flushPromises()
    expect(setEnabledSpy).toHaveBeenCalledTimes(1)
    expect(setEnabledSpy).toHaveBeenCalledWith('search-deepseek', false)

    // Clicking switch on an in-use plugin opens Popconfirm and does NOT disable immediately.
    await switches[0]!.trigger('click')
    await flushPromises()
    expect(setEnabledSpy).toHaveBeenCalledTimes(1)
    expect(document.body.textContent).toContain('停用「OpenAI 兼容接口」？')
    expect(document.body.textContent).toContain('模型提供方 OpenAI（讨论串默认）')

    // Confirming disables the plugin.
    const confirmBtn = Array.from(document.body.querySelectorAll('button')).find((button) =>
      button.textContent?.includes('仍要停用'),
    )
    expect(confirmBtn).toBeTruthy()
    confirmBtn!.click()
    await flushPromises()
    expect(setEnabledSpy).toHaveBeenCalledTimes(2)
    expect(setEnabledSpy).toHaveBeenCalledWith('model-openai', false)
  })
})
