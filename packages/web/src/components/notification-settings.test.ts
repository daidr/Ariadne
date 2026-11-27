// @vitest-environment happy-dom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { api, type NotificationSettings, type NotificationSettingsInput } from '../api'
import { locale } from '../i18n'
import { pushState } from '../push'
import NotificationSettingsView from './NotificationSettings.vue'

const wrappers: VueWrapper[] = []

function makeNotificationSettings(): NotificationSettings {
  return {
    enabled: true,
    webPushEnabled: true,
    vapidPublicKey:
      'BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U',
    subscriptions: [
      {
        id: 'ps_1',
        endpoint: 'https://push.example.com/sub/current',
        label: 'macOS · Chrome',
        userAgent: 'Chrome/131',
        createdAt: 1_760_000_000_000,
        lastUsedAt: 1_760_000_500_000,
        lastError: null,
      },
      {
        id: 'ps_2',
        endpoint: 'https://push.example.com/sub/phone',
        label: 'iPhone · Safari',
        userAgent: 'Safari/18',
        createdAt: 1_760_000_100_000,
        lastUsedAt: null,
        lastError: 'HTTP 502',
      },
    ],
    webhooks: [
      {
        id: 'wh_1',
        name: '运维告警',
        url: 'https://hooks.example.com/notify',
        enabled: true,
        secretSet: true,
        headers: ['X-Team'],
      },
    ],
    notifiers: [
      {
        kind: 'lark-bot',
        title: { 'zh-CN': '飞书机器人', en: 'Lark Bot' },
        description: { 'zh-CN': '通过飞书群机器人推送消息', en: 'Send alerts via Lark bot' },
        settings: {
          type: 'object',
          required: ['webhookUrl'],
          properties: {
            webhookUrl: {
              type: 'string',
              title: { 'zh-CN': '机器人地址', en: 'Webhook URL' },
              secret: true,
            },
          },
        },
        capabilities: { supportsCheck: true },
        enabled: true,
        values: {},
        secretsSet: ['webhookUrl'],
        ready: true,
      },
    ],
  }
}

beforeEach(() => {
  locale.value = 'zh-CN'
  pushState.endpoint = 'https://push.example.com/sub/current'
  pushState.serverActive = true
  pushState.permission = 'granted'
  pushState.busy = false
})

afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  vi.restoreAllMocks()
})

describe('NotificationSettings component', () => {
  test('renders Web Push devices, webhooks, and Lua notifier plugins and tests drafts', async () => {
    const initial = makeNotificationSettings()
    vi.spyOn(api, 'getNotificationSettings').mockResolvedValue(initial)
    const testSpy = vi.spyOn(api, 'testNotification').mockResolvedValue({
      ok: true,
      status: 200,
      message: { 'zh-CN': '发送成功', en: 'Sent' },
      delivered: 1,
      failed: 0,
    })

    const wrapper = mount(NotificationSettingsView)
    wrappers.push(wrapper)
    await flushPromises()

    expect(wrapper.text()).toContain('PWA 浏览器推送（Web Push）')
    expect(wrapper.text()).toContain('macOS · Chrome')
    expect(wrapper.text()).toContain('iPhone · Safari')
    expect(wrapper.text()).toContain('HTTP 502')
    expect(wrapper.text()).toContain('飞书机器人')
    expect(wrapper.text()).toContain('已就绪')
    expect(wrapper.text()).not.toContain('发往下方通道')
    expect(wrapper.text()).not.toContain('HMAC-SHA256')
    expect(wrapper.text()).not.toContain('通过 Lua 或内置插件扩展')

    // Clicking "测试发送" on webhook tests the live draft.
    const testButtons = wrapper.findAll('button').filter((btn) => btn.text().trim() === '测试发送')
    expect(testButtons.length).toBe(2)

    await testButtons[0]!.trigger('click')
    await flushPromises()
    expect(testSpy).toHaveBeenCalledWith(
      expect.objectContaining({
        channel: 'webhook',
        targetId: 'wh_1',
        webhookDraft: expect.objectContaining({
          id: 'wh_1',
          name: '运维告警',
          url: 'https://hooks.example.com/notify',
          enabled: true,
        }),
      }),
    )

    // Clicking "测试发送" on notifier tests the plugin draft.
    await testButtons[1]!.trigger('click')
    await flushPromises()
    expect(testSpy).toHaveBeenCalledWith(
      expect.objectContaining({
        channel: 'notifier',
        targetId: 'lark-bot',
        notifierDraft: expect.objectContaining({ enabled: true }),
      }),
    )

    // Clicking "管理插件" emits openPlugins so ModelSettings switches tabs.
    const openPluginsBtn = wrapper.findAll('button').find((btn) => btn.text().includes('管理插件'))
    expect(openPluginsBtn).toBeDefined()
    await openPluginsBtn!.trigger('click')
    expect(wrapper.emitted('openPlugins')).toHaveLength(1)
  })

  test('tracks dirty state, supports adding webhooks, and saves normalized payload', async () => {
    const initial = makeNotificationSettings()
    vi.spyOn(api, 'getNotificationSettings').mockResolvedValue(initial)
    let savedPayload: NotificationSettingsInput | null = null
    vi.spyOn(api, 'saveNotificationSettings').mockImplementation(async (input) => {
      savedPayload = input
      return {
        ...initial,
        enabled: input.enabled,
        webPushEnabled: input.webPushEnabled,
        webhooks: input.webhooks.map((hook) => ({
          id: hook.id,
          name: hook.name,
          url: hook.url,
          enabled: hook.enabled,
          secretSet: Boolean(hook.secret) || (!hook.clearSecret && hook.id === 'wh_1'),
          headers: (hook.headers ?? []).map((h) => h.name),
        })),
      }
    })

    const wrapper = mount(NotificationSettingsView)
    wrappers.push(wrapper)
    await flushPromises()

    const vm = wrapper.vm as unknown as {
      dirty: boolean
      save: () => Promise<boolean>
    }
    expect(vm.dirty).toBe(false)

    // Add a second webhook and fill its URL.
    const addBtn = wrapper.findAll('button').find((btn) => btn.text().includes('添加 Webhook'))
    expect(addBtn).toBeDefined()
    await addBtn!.trigger('click')
    await flushPromises()

    const urlInputs = wrapper.findAll('input[placeholder="https://hooks.example.com/notify"]')
    expect(urlInputs).toHaveLength(2)
    await urlInputs[1]!.setValue('https://hooks.example.com/second')
    await flushPromises()

    expect(vm.dirty).toBe(true)
    const ok = await vm.save()
    expect(ok).toBe(true)
    expect(savedPayload).not.toBeNull()
    expect(savedPayload!.webhooks).toHaveLength(2)
    expect(savedPayload!.webhooks[1]!.url).toBe('https://hooks.example.com/second')
    expect(vm.dirty).toBe(false)
  })
})
