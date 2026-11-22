import { describe, expect, test } from 'vitest'
import type { PluginRecord } from '../api'
import {
  affectedConfigs,
  builtinGroupOf,
  groupBuiltinPlugins,
  pluginProvides,
  splitPlugins,
} from './plugin-settings'

function record(partial: Partial<PluginRecord> & { id: string }): PluginRecord {
  return {
    name: { 'zh-CN': partial.id },
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
    },
    ...partial,
  }
}

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

  test('groups built-in plugins into models, searches, sources, and other even when disabled', () => {
    const builtins = [
      record({
        id: 'github',
        provides: {
          models: [],
          contexts: ['github'],
          dashboards: [],
          searches: [],
          links: ['github'],
        },
      }),
      record({
        id: 'search-tavily',
        status: 'disabled',
        provides: {
          models: [],
          contexts: [],
          dashboards: [],
          searches: ['tavily'],
          links: [],
        },
      }),
      record({
        id: 'model-anthropic',
        status: 'disabled',
        provides: {
          models: ['anthropic'],
          contexts: [],
          dashboards: [],
          searches: [],
          links: [],
        },
      }),
      record({
        id: 'dash-only',
        provides: {
          models: [],
          contexts: [],
          dashboards: ['metrics'],
          searches: [],
          links: [],
        },
      }),
    ]

    expect(builtinGroupOf(builtins[0]!)).toBe('sources')
    expect(builtinGroupOf(builtins[1]!)).toBe('searches')
    expect(builtinGroupOf(builtins[2]!)).toBe('models')
    expect(builtinGroupOf(builtins[3]!)).toBe('other')

    const groups = groupBuiltinPlugins(builtins)
    expect(groups.map((g) => g.key)).toEqual(['models', 'searches', 'sources', 'other'])
    expect(groups[0]!.plugins.map((p) => p.id)).toEqual(['model-anthropic'])
    expect(groups[1]!.plugins.map((p) => p.id)).toEqual(['search-tavily'])
    expect(groups[2]!.plugins.map((p) => p.id)).toEqual(['github'])
    expect(groups[3]!.plugins.map((p) => p.id)).toEqual(['dash-only'])
    expect(pluginProvides(builtins[0]!)).toEqual(['github'])
  })

  test('detects affected model providers and active search service on disable', () => {
    const modelPlugin = record({
      id: 'model-anthropic',
      provides: {
        models: ['anthropic'],
        contexts: [],
        dashboards: [],
        searches: [],
        links: [],
      },
    })
    const searchPlugin = record({
      id: 'search-tavily',
      provides: {
        models: [],
        contexts: [],
        dashboards: [],
        searches: ['tavily'],
        links: [],
      },
    })

    expect(
      affectedConfigs(
        modelPlugin,
        [
          { name: 'Claude 主力', kind: 'anthropic' },
          { name: 'OpenAI', kind: 'openai-compatible' },
        ],
        'tavily',
      ),
    ).toEqual(['Claude 主力'])

    expect(affectedConfigs(searchPlugin, [], 'tavily', { tavily: 'Tavily' })).toEqual(['Tavily'])

    expect(affectedConfigs(searchPlugin, [], 'deepseek', { tavily: 'Tavily' })).toEqual([])
  })
})
