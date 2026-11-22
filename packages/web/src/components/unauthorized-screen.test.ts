// @vitest-environment happy-dom
import { flushPromises, mount } from '@vue/test-utils'
import { DouConfigProvider, toast } from 'dou-ui'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { createMemoryHistory } from 'vue-router'
import App from '../App.vue'
import { api, ApiError, getToken, initToken, parseTokenInput, setToken } from '../api'
import { douLocale, setLocale, t } from '../i18n'
import { report, state } from '../store'
import { createWorkspaceRouter } from '../workspace-route'
import UnauthorizedScreen from './UnauthorizedScreen.vue'

const toastItems = () => [...document.querySelectorAll<HTMLLIElement>('.dou-toast__item')]

function mountUnauthorizedScreen(reload = vi.fn()) {
  const Host = defineComponent({
    setup() {
      return () =>
        h(DouConfigProvider, { locale: douLocale.value }, () => h(UnauthorizedScreen, { reload }))
    },
  })
  return { wrapper: mount(Host, { attachTo: document.body }), reload }
}

describe('token parsing and persistence', () => {
  beforeEach(() => {
    localStorage.clear()
    setToken('')
  })

  afterEach(() => {
    localStorage.clear()
    setToken('')
  })

  test('normalizes raw tokens, Bearer values, and server startup URLs', () => {
    expect(parseTokenInput('   ')).toBe('')
    expect(parseTokenInput('  secret-123  ')).toBe('secret-123')
    expect(parseTokenInput('Bearer secret-456')).toBe('secret-456')
    expect(parseTokenInput('http://127.0.0.1:7428/#token=url-token-789')).toBe('url-token-789')
    expect(parseTokenInput('https://ariadne.local/?token=query%20token')).toBe('query token')
    expect(parseTokenInput('#token=hash-token&other=1')).toBe('hash-token')
    expect(parseTokenInput('token=bare-param')).toBe('bare-param')
    expect(parseTokenInput('http://127.0.0.1:7428/')).toBe('')
    expect(parseTokenInput('#token=')).toBe('')
  })

  test('persists manually set token in localStorage and supports initToken from hash', () => {
    expect(setToken('http://127.0.0.1:7428/#token=pwa-secret')).toBe('pwa-secret')
    expect(localStorage.getItem('ariadne.token')).toBe('pwa-secret')
    expect(getToken()).toBe('pwa-secret')

    localStorage.clear()
    setToken('')
    history.replaceState(null, '', '/projects#token=hash-secret')
    expect(initToken()).toBe('hash-secret')
    expect(localStorage.getItem('ariadne.token')).toBe('hash-secret')
    expect(location.hash).toBe('')
  })
})

describe('UnauthorizedScreen', () => {
  beforeEach(() => {
    localStorage.clear()
    setToken('')
    toast.closeAll()
    setLocale('zh-CN')
    state.stream = 'unauthorized'
  })

  afterEach(() => {
    document.body.innerHTML = ''
    localStorage.clear()
    setToken('')
    toast.closeAll()
    state.stream = 'connecting'
    setLocale('zh-CN')
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  test('shows missing-token copy when no token is stored and invalid-token copy when one exists', async () => {
    const first = mountUnauthorizedScreen()
    expect(first.wrapper.text()).toContain(t('error.unauthorized'))
    expect(first.wrapper.text()).toContain(t('auth.missingDescription'))
    expect(first.wrapper.find('.dou-result--warning').exists()).toBe(true)
    first.wrapper.unmount()

    setToken('stale-token')
    const second = mountUnauthorizedScreen()
    expect(second.wrapper.text()).toContain(t('error.unauthorized'))
    expect(second.wrapper.text()).toContain(t('auth.invalidDescription'))
    expect(second.wrapper.find('.dou-result--danger').exists()).toBe(true)
    second.wrapper.unmount()
  })

  test('validates blank input without clearing existing token or reloading', async () => {
    setToken('existing-stale-token')
    const { wrapper, reload } = mountUnauthorizedScreen()

    const openBtn = wrapper.findAll('button').find((btn) => btn.text().includes(t('auth.setToken')))
    expect(openBtn).toBeDefined()
    await openBtn!.trigger('click')
    await nextTick()

    const dialog = document.querySelector('.dou-dialog')
    expect(dialog).not.toBeNull()

    const input = dialog!.querySelector<HTMLInputElement>('input')!
    input.value = '   '
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()

    // Submit via confirm button -> shows inline validation error.
    const confirmBtn = [...dialog!.querySelectorAll<HTMLButtonElement>('button')].find((btn) =>
      btn.textContent?.includes(t('auth.confirm')),
    )
    expect(confirmBtn).toBeDefined()
    confirmBtn!.click()
    await nextTick()

    expect(dialog!.textContent).toContain(t('auth.tokenRequired'))
    expect(reload).not.toHaveBeenCalled()
    expect(getToken()).toBe('existing-stale-token')

    // Typing clears the error; pressing Enter with a valid token persists and reloads.
    input.value = 'http://127.0.0.1:7428/#token=fresh-pwa-token'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(dialog!.textContent).not.toContain(t('auth.tokenRequired'))

    input.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
    )
    await nextTick()

    expect(localStorage.getItem('ariadne.token')).toBe('fresh-pwa-token')
    expect(getToken()).toBe('fresh-pwa-token')
    expect(reload).toHaveBeenCalledTimes(1)

    wrapper.unmount()
  })

  test('App shows only header and unauthorized screen without sidebar, workspace or toast on 401', async () => {
    const eventSourceCtor = vi.fn()
    vi.stubGlobal(
      'EventSource',
      class {
        constructor(...args: unknown[]) {
          eventSourceCtor(...args)
        }
        addEventListener() {}
        close() {}
      },
    )
    vi.spyOn(api, 'listProjects').mockRejectedValue(
      new ApiError(401, { key: 'error.unauthorized' }),
    )
    vi.spyOn(api, 'getModelSettings').mockRejectedValue(
      new ApiError(401, { key: 'error.unauthorized' }),
    )
    vi.spyOn(api, 'getInputSettings').mockRejectedValue(
      new ApiError(401, { key: 'error.unauthorized' }),
    )
    vi.spyOn(api, 'sourceKinds').mockRejectedValue(new ApiError(401, { key: 'error.unauthorized' }))
    vi.spyOn(api, 'foundSetups').mockRejectedValue(new ApiError(401, { key: 'error.unauthorized' }))

    state.stream = 'connecting'
    const router = createWorkspaceRouter(createMemoryHistory(), App)
    await router.push('/projects')
    await router.isReady()

    const wrapper = mount(App, { global: { plugins: [router] }, attachTo: document.body })
    await flushPromises()
    await nextTick()

    expect(state.stream).toBe('unauthorized')
    expect(eventSourceCtor).not.toHaveBeenCalled()
    expect(toastItems()).toHaveLength(0)

    // Header stays visible with brand only; sidebar and workspace panels are hidden.
    expect(wrapper.find('.workspace-header').exists()).toBe(true)
    expect(wrapper.find('.header-actions').exists()).toBe(false)
    expect(wrapper.find('.project-sidebar').exists()).toBe(false)
    expect(wrapper.find('.project-split').exists()).toBe(false)
    expect(wrapper.find('.unauthorized-screen').exists()).toBe(true)
    expect(wrapper.text()).toContain(t('error.unauthorized'))
    expect(wrapper.text()).toContain(t('auth.setToken'))

    wrapper.unmount()
  })

  test('report() switches stream state to unauthorized on 401 without popping a toast', async () => {
    state.stream = 'open'
    report(new ApiError(401, { key: 'error.unauthorized' }))
    await nextTick()

    expect(state.stream).toBe('unauthorized')
    expect(toastItems()).toHaveLength(0)
  })
})
