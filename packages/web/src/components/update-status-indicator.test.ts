// @vitest-environment happy-dom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { DouConfigProvider } from 'dou-ui'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { api, type UpdateStatus } from '../api'
import { douLocale, locale, t } from '../i18n'
import { state } from '../store'
import UpdateStatusIndicator from './UpdateStatusIndicator.vue'

const wrappers: VueWrapper[] = []

function makeStatus(overrides: Partial<UpdateStatus> = {}): UpdateStatus {
  return {
    version: '0.1.0-alpha.23',
    preferences: { channel: 'preview', automatic: true },
    canInstall: true,
    canRestart: true,
    restarting: false,
    restartPhase: '',
    activeTurns: 0,
    activeBackground: 0,
    state: 'ready',
    downloadPercent: 0,
    release: {
      version: '0.1.0-alpha.24',
      notes: 'Release notes',
      url: 'https://example.test/release',
      prerelease: true,
      installable: true,
    },
    ...overrides,
  }
}

function mountIndicator(compact = false) {
  const onOpenSettings = vi.fn()
  const Host = defineComponent({
    setup() {
      return () =>
        h(DouConfigProvider, { locale: douLocale.value }, () =>
          h(UpdateStatusIndicator, { compact, onOpenSettings }),
        )
    },
  })
  const wrapper = mount(Host, { attachTo: document.body })
  wrappers.push(wrapper)
  return { wrapper, onOpenSettings }
}

function findPopoverButton(label: string): HTMLButtonElement | undefined {
  return [...document.body.querySelectorAll<HTMLButtonElement>('.dou-popover button')].find(
    (btn) => btn.textContent?.trim() === label,
  )
}

beforeEach(() => {
  locale.value = 'zh-CN'
  state.updateStatus = null
})

afterEach(() => {
  state.updateStatus = null
  wrappers.splice(0).forEach((w) => w.unmount())
  document.body.innerHTML = ''
  vi.restoreAllMocks()
})

describe('UpdateStatusIndicator', () => {
  test('stays hidden when no update is ready and no restart phase is active', async () => {
    state.updateStatus = makeStatus({ state: 'current', release: null })
    const { wrapper } = mountIndicator()
    await nextTick()
    expect(wrapper.find('button').exists()).toBe(false)
  })

  test('renders waiting_idle status, asks confirmation before interrupting active sessions, and cancels wait', async () => {
    const waiting = makeStatus({
      restarting: true,
      restartPhase: 'waiting_idle',
      activeTurns: 3,
      activeBackground: 1,
    })
    state.updateStatus = waiting

    const restartSpy = vi.spyOn(api, 'restartToApplyUpdate').mockResolvedValue({
      ...waiting,
      restartPhase: 'restarting',
    })
    const cancelSpy = vi.spyOn(api, 'cancelRestart').mockResolvedValue(makeStatus())

    const { wrapper, onOpenSettings } = mountIndicator()
    await nextTick()

    const trigger = wrapper.get('button')
    expect(trigger.text()).toContain(t('updates.header.waitingIdle'))

    await trigger.trigger('click')
    await nextTick()

    const popoverText = document.body.querySelector('.dou-popover')?.textContent ?? ''
    expect(popoverText).toContain('3')
    expect(popoverText).toContain('1')

    // First click on "立即重启" shows inline confirmation instead of calling API directly.
    const restartNowBtn = findPopoverButton(t('updates.restartNow'))
    expect(restartNowBtn).toBeTruthy()
    restartNowBtn!.click()
    await nextTick()
    expect(restartSpy).not.toHaveBeenCalled()
    expect(document.body.querySelector('.dou-popover')?.textContent).toContain(
      t('updates.restartNowConfirm'),
    )

    // Dismissing confirmation restores actions; clicking "取消等待" cancels cleanly.
    findPopoverButton(t('settings.cancel'))!.click()
    await nextTick()
    findPopoverButton(t('updates.cancelRestart'))!.click()
    await flushPromises()
    expect(cancelSpy).toHaveBeenCalledTimes(1)
    expect(state.updateStatus?.restartPhase).toBe('')

    // Clicking "更新设置" forwards openSettings.
    findPopoverButton(t('updates.header.openSettings'))!.click()
    await nextTick()
    expect(onOpenSettings).toHaveBeenCalledTimes(1)
  })

  test('restarts directly when idle without extra confirmation and exposes cancel drain while draining', async () => {
    const readyIdle = makeStatus({ activeTurns: 0, activeBackground: 0 })
    state.updateStatus = readyIdle

    const restartSpy = vi.spyOn(api, 'restartToApplyUpdate').mockResolvedValue({
      ...readyIdle,
      restarting: true,
      restartPhase: 'restarting',
    })
    const cancelSpy = vi.spyOn(api, 'cancelRestart').mockResolvedValue(readyIdle)

    const { wrapper } = mountIndicator()
    await nextTick()

    await wrapper.get('button').trigger('click')
    await nextTick()
    findPopoverButton(t('updates.restartApply'))!.click()
    await flushPromises()
    expect(restartSpy).toHaveBeenCalledWith('when-idle')

    // Draining triggered externally (e.g. CLI) appears even when state !== ready.
    state.updateStatus = makeStatus({
      state: 'current',
      release: null,
      restarting: true,
      restartPhase: 'draining',
      activeTurns: 2,
    })
    await nextTick()
    expect(wrapper.get('button').text()).toContain(t('updates.header.draining'))
    findPopoverButton(t('updates.cancelDrain'))!.click()
    await flushPromises()
    expect(cancelSpy).toHaveBeenCalledTimes(1)
  })
})
