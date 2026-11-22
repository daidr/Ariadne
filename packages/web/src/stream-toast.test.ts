// @vitest-environment happy-dom
import { flushPromises, mount } from '@vue/test-utils'
import { DouToastViewport, toast } from 'dou-ui'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { createMemoryHistory } from 'vue-router'
import App from './App.vue'
import { api } from './api'
import { setLocale, t } from './i18n'
import { state } from './store'
import { RECONNECTED_DURATION_MS, useStreamStatusToast } from './stream-toast'
import { createWorkspaceRouter } from './workspace-route'

function mountHost() {
  const Host = defineComponent({
    setup() {
      useStreamStatusToast()
      return () => h(DouToastViewport)
    },
  })
  return { wrapper: mount(Host) }
}

const toastItems = () => [...document.querySelectorAll<HTMLLIElement>('.dou-toast__item')]

describe('useStreamStatusToast', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    toast.closeAll()
    setLocale('zh-CN')
    state.stream = 'connecting'
  })

  afterEach(() => {
    toast.closeAll()
    state.stream = 'connecting'
    setLocale('zh-CN')
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  test('stays silent on normal initial connection', async () => {
    const { wrapper } = mountHost()
    await nextTick()
    expect(toastItems()).toHaveLength(0)

    state.stream = 'open'
    await nextTick()
    expect(toastItems()).toHaveLength(0)

    wrapper.unmount()
  })

  test('shows a persistent loading toast on disconnect and resolves it in place on reconnect', async () => {
    const { wrapper } = mountHost()
    state.stream = 'open'
    await nextTick()

    state.stream = 'lost'
    await nextTick()

    let items = toastItems()
    expect(items).toHaveLength(1)
    expect(items[0]?.classList.contains('dou-toast--warning')).toBe(true)
    expect(items[0]?.getAttribute('aria-busy')).toBe('true')
    expect(items[0]?.querySelector('.dou-spinner')).not.toBeNull()
    expect(items[0]?.querySelector('.dou-toast__close')).toBeNull()
    expect(items[0]?.textContent).toContain(t('error.connection'))

    // Persistent: stays open indefinitely while disconnected.
    vi.advanceTimersByTime(60_000)
    await nextTick()
    expect(toastItems()).toHaveLength(1)

    // Reconnecting morphs the same toast into a self-dismissing success confirmation.
    state.stream = 'open'
    await nextTick()

    items = toastItems()
    expect(items).toHaveLength(1)
    expect(items[0]?.classList.contains('dou-toast--success')).toBe(true)
    expect(items[0]?.getAttribute('aria-busy')).toBeNull()
    expect(items[0]?.querySelector('.dou-spinner')).toBeNull()
    expect(items[0]?.textContent).toContain(t('error.reconnected'))

    vi.advanceTimersByTime(RECONNECTED_DURATION_MS)
    await nextTick()
    expect(toastItems()).toHaveLength(0)

    wrapper.unmount()
  })

  test('reuses the active toast without duplicating if connection drops again before recovery fades', async () => {
    const { wrapper } = mountHost()
    state.stream = 'lost'
    await nextTick()
    expect(toastItems()).toHaveLength(1)

    state.stream = 'open'
    await nextTick()
    vi.advanceTimersByTime(RECONNECTED_DURATION_MS / 2)

    // Drop again before the recovery toast finishes its countdown.
    state.stream = 'lost'
    await nextTick()

    const items = toastItems()
    expect(items).toHaveLength(1)
    expect(items[0]?.classList.contains('dou-toast--warning')).toBe(true)
    expect(items[0]?.getAttribute('aria-busy')).toBe('true')
    expect(items[0]?.textContent).toContain(t('error.connection'))

    // Previous countdown was cancelled; the loading toast stays persistent.
    vi.advanceTimersByTime(60_000)
    await nextTick()
    expect(toastItems()).toHaveLength(1)

    wrapper.unmount()
  })

  test('dismisses any active connection toast and stays silent when unauthorized', async () => {
    const { wrapper } = mountHost()
    state.stream = 'lost'
    await nextTick()
    expect(toastItems()).toHaveLength(1)

    state.stream = 'unauthorized'
    await nextTick()
    expect(toastItems()).toHaveLength(0)

    vi.advanceTimersByTime(60_000)
    await nextTick()
    expect(toastItems()).toHaveLength(0)

    wrapper.unmount()
  })

  test('updates copy in place when the UI locale changes while preserving persistence', async () => {
    const { wrapper } = mountHost()
    state.stream = 'lost'
    await nextTick()
    expect(toastItems()[0]?.textContent).toContain('与本地服务的连接断开，正在重连')

    setLocale('en')
    await nextTick()

    const items = toastItems()
    expect(items).toHaveLength(1)
    expect(items[0]?.getAttribute('aria-busy')).toBe('true')
    expect(items[0]?.textContent).toContain('Lost connection to the local server, reconnecting')

    // Still persistent after locale update.
    vi.advanceTimersByTime(60_000)
    await nextTick()
    expect(toastItems()).toHaveLength(1)

    wrapper.unmount()
  })

  test('App renders connection status via floating toast with no top layout banner', async () => {
    vi.stubGlobal(
      'EventSource',
      class {
        addEventListener() {}
        close() {}
      },
    )
    vi.spyOn(api, 'listProjects').mockResolvedValue({ projects: [], lastSeq: 1 })
    vi.spyOn(api, 'getModelSettings').mockResolvedValue({
      models: [],
      providers: [],
      roles: { thread: { model: '' } },
      showCost: false,
    } as never)
    vi.spyOn(api, 'getInputSettings').mockResolvedValue({
      submitOn: 'enter',
      snippets: [],
      keys: {},
    } as never)
    vi.spyOn(api, 'foundSetups').mockResolvedValue({ found: [] })
    vi.spyOn(api, 'sourceKinds').mockResolvedValue({ kinds: [] })

    const router = createWorkspaceRouter(createMemoryHistory(), App)
    await router.push('/projects')
    await router.isReady()

    const wrapper = mount(App, { global: { plugins: [router] } })
    await flushPromises()
    state.stream = 'lost'
    await nextTick()

    expect(wrapper.find('.banner').exists()).toBe(false)
    expect(wrapper.find('.dou-alert').exists()).toBe(false)
    expect(toastItems()).toHaveLength(1)
    expect(toastItems()[0]?.getAttribute('aria-busy')).toBe('true')
    expect(toastItems()[0]?.textContent).toContain(t('error.connection'))

    wrapper.unmount()
    await nextTick()
    expect(toastItems()).toHaveLength(0)
  })
})
