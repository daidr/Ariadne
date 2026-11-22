// @vitest-environment happy-dom
import { flushPromises, mount } from '@vue/test-utils'
import { DouConfigProvider, toast } from 'dou-ui'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { createMemoryHistory } from 'vue-router'
import App from '../App.vue'
import {
  api,
  ApiError,
  followEvents,
  getToken,
  initToken,
  isValidToken,
  parseTokenInput,
  saveToken,
  setToken,
} from '../api'
import { douLocale, setLocale, t } from '../i18n'
import { authorizationHeader } from '../rpc'
import { report, start, state } from '../store'
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

  test('rejects non-HTTP-header characters and sanitizes corrupt stored tokens for Authorization', () => {
    expect(isValidToken('valid-token_123')).toBe(true)
    expect(isValidToken('?secret')).toBe(true)
    expect(isValidToken('Bearer abc')).toBe(true)
    expect(isValidToken('中文令牌')).toBe(false)
    expect(isValidToken('token-with-😀')).toBe(false)
    expect(isValidToken('line1\nline2')).toBe(false)
    expect(isValidToken('bad\r\0token')).toBe(false)

    setToken('good-token')
    expect(setToken('中文令牌')).toBe('')
    expect(saveToken('bad\ntoken')).toBe('')
    expect(getToken()).toBe('good-token')

    // Even if corrupt data is already in localStorage, authorizationHeader stays Header-safe.
    localStorage.setItem('ariadne.token', '中文令牌\n')
    expect(getToken()).toBe('中文令牌\n')
    expect(authorizationHeader()).toBe('Bearer ')
    expect(() => new Headers({ authorization: authorizationHeader() })).not.toThrow()
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

  test('rejects tokens containing invalid HTTP header characters in the dialog', async () => {
    setToken('existing-stale-token')
    const { wrapper, reload } = mountUnauthorizedScreen()

    const openBtn = wrapper.findAll('button').find((btn) => btn.text().includes(t('auth.setToken')))
    await openBtn!.trigger('click')
    await nextTick()

    const dialog = document.querySelector('.dou-dialog')!
    const input = dialog.querySelector<HTMLInputElement>('input')!
    input.value = '中文错误令牌'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()

    input.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
    )
    await nextTick()

    expect(dialog.textContent).toContain(t('auth.tokenInvalidChars'))
    expect(reload).not.toHaveBeenCalled()
    expect(getToken()).toBe('existing-stale-token')

    wrapper.unmount()
  })

  test('does not double-parse tokens extracted from startup URLs', async () => {
    for (const [pasted, expected] of [
      ['http://127.0.0.1:7428/#token=%3Fsecret', '?secret'],
      ['#token=Bearer%20abc', 'Bearer abc'],
      ['http://127.0.0.1:7428/#token=token%3Dcustom', 'token=custom'],
    ] as const) {
      localStorage.clear()
      setToken('')
      const { wrapper, reload } = mountUnauthorizedScreen()

      const openBtn = wrapper
        .findAll('button')
        .find((btn) => btn.text().includes(t('auth.setToken')))
      await openBtn!.trigger('click')
      await nextTick()

      const dialog = document.querySelector('.dou-dialog')!
      const input = dialog.querySelector<HTMLInputElement>('input')!
      input.value = pasted
      input.dispatchEvent(new Event('input', { bubbles: true }))
      await nextTick()

      input.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      await nextTick()

      expect(localStorage.getItem('ariadne.token')).toBe(expected)
      expect(getToken()).toBe(expected)
      expect(reload).toHaveBeenCalledTimes(1)

      wrapper.unmount()
      document.body.innerHTML = ''
    }
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

  test('in-flight api.ping() from a stopped EventSource cannot overwrite unauthorized back to lost', async () => {
    const listeners = new Map<string, (() => void)[]>()
    class FakeEventSource {
      static CLOSED = 2
      readyState = FakeEventSource.CLOSED
      addEventListener(type: string, cb: () => void) {
        const list = listeners.get(type) ?? []
        list.push(cb)
        listeners.set(type, list)
      }
      close() {}
    }
    vi.stubGlobal('EventSource', FakeEventSource)

    // Direct followEvents check: stopping before ping settles suppresses onStatus.
    let resolveDirectPing!: (value: { ok: boolean }) => void
    vi.spyOn(api, 'ping').mockImplementationOnce(
      () =>
        new Promise<{ ok: boolean }>((resolve) => {
          resolveDirectPing = resolve
        }),
    )
    const onStatus = vi.fn()
    const stop = followEvents(null, vi.fn(), onStatus)
    for (const cb of listeners.get('error') ?? []) cb()
    stop()
    resolveDirectPing({ ok: true })
    await flushPromises()
    expect(onStatus).not.toHaveBeenCalled()

    // Full store start() race: error starts ping, another request gets 401 first, then ping fails with a network error.
    listeners.clear()
    let rejectStorePing!: (error: unknown) => void
    vi.spyOn(api, 'ping').mockImplementationOnce(
      () =>
        new Promise<{ ok: boolean }>((_resolve, reject) => {
          rejectStorePing = reject
        }),
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

    state.stream = 'open'
    await start(async () => {})
    for (const cb of listeners.get('error') ?? []) cb()

    // Another request gets 401 before api.ping() finishes.
    report(new ApiError(401, { key: 'error.unauthorized' }))
    expect(state.stream).toBe('unauthorized')

    // Now the stale ping settles with a non-401 network error.
    rejectStorePing(new TypeError('network down'))
    await flushPromises()
    expect(state.stream).toBe('unauthorized')
  })
})
