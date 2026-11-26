// @vitest-environment happy-dom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { api, type McpSettings } from '../api'
import { ensureLocale, locale } from '../i18n'
import McpSettingsPanel from './McpSettings.vue'

const wrappers: VueWrapper[] = []

function makeMcpSettings(): McpSettings {
  return {
    servers: [
      {
        id: 'github',
        name: 'GitHub',
        transport: 'http',
        url: 'https://example.invalid/mcp',
        env: [],
        headers: [],
        tools: { search_issues: 'ask' },
      },
    ],
    status: [
      {
        id: 'github',
        state: 'ready',
        error: null,
        log: null,
        info: { name: 'github-mcp', version: '1.0.0', protocolVersion: '2025-03-26' },
        tools: [
          {
            name: 'search_issues',
            title: 'Search Issues',
            description:
              'Search issues across the connected repository with optional label, state, and query filters for project coordination',
          },
        ],
      },
    ],
  }
}

beforeEach(async () => {
  locale.value = 'zh-CN'
  await ensureLocale('zh-CN')
})

afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  vi.restoreAllMocks()
})

describe('McpSettings', () => {
  test('marks tool permission segmented controls as non-shrinking alongside truncating descriptions', async () => {
    vi.spyOn(api, 'getMcpSettings').mockResolvedValue(makeMcpSettings())

    const wrapper = mount(McpSettingsPanel)
    wrappers.push(wrapper)
    await flushPromises()

    const row = wrapper.find('.mcp-tool')
    expect(row.exists()).toBe(true)

    const textCol = row.find('.mcp-tool-text')
    expect(textCol.exists()).toBe(true)

    const markControl = row.find('.dou-segmented-control')
    expect(markControl.exists()).toBe(true)
    expect(markControl.classes()).toContain('mcp-tool-mark')

    const scopeId = row.element.getAttributeNames().find((name) => name.startsWith('data-v-'))
    expect(scopeId).toBeTruthy()
    expect(markControl.attributes(scopeId!)).toBeDefined()
  })
})
