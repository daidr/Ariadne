import { computed, onBeforeUnmount, onMounted, shallowRef } from 'vue'
import { api, ApiError, type UpdatePreferences, type UpdateStatus } from '../api'
import { t, tm } from '../i18n'

function needsPolling(view: UpdateStatus | null) {
  if (!view) return false
  return (
    view.state === 'checking' ||
    view.state === 'downloading' ||
    view.state === 'installing' ||
    (view.preferences.automatic &&
      view.canInstall &&
      view.state === 'available' &&
      view.release?.installable &&
      !view.failure)
  )
}

export function useUpdatesSettings() {
  const status = shallowRef<UpdateStatus | null>(null)
  const pending = shallowRef<'load' | 'save' | 'check' | 'install' | null>('load')
  const error = shallowRef<string | null>(null)
  const controller = new AbortController()
  let timer: ReturnType<typeof setTimeout> | undefined
  let sequence = 0

  const updating = computed(
    () =>
      pending.value === 'install' ||
      status.value?.state === 'downloading' ||
      status.value?.state === 'installing',
  )
  const busy = computed(
    () => pending.value !== null || status.value?.state === 'checking' || updating.value,
  )
  const release = computed(() => status.value?.release)
  const available = computed(() => status.value?.state === 'available')
  const installable = computed(
    () => available.value && status.value?.canInstall && release.value?.installable,
  )
  const installLabel = computed(() => {
    if (status.value?.state === 'installing') return t('updates.installing')
    if (updating.value)
      return t('updates.downloading', { percent: status.value?.downloadPercent ?? 0 })
    return t('updates.install')
  })

  const message = computed(() => {
    const view = status.value
    if (error.value) return error.value
    if (pending.value === 'check' || view?.state === 'checking') return t('updates.checking')
    if (!view || updating.value) return null
    if (view.failure) return t(`updates.error.${view.failure}`)
    if (view.state === 'no-release') {
      return t(view.preferences.channel === 'stable' ? 'updates.noStable' : 'updates.noRelease')
    }
    if (view.state === 'current') return t('updates.current')
    if (view.state === 'ready') return t('updates.ready', { version: view.release?.version ?? '' })
    return null
  })

  function poll(delay = status.value?.state === 'downloading' ? 500 : 1200) {
    clearTimeout(timer)
    if (!controller.signal.aborted && needsPolling(status.value)) {
      timer = setTimeout(() => void load(), delay)
    }
  }

  async function perform(action: typeof pending.value, request: () => Promise<UpdateStatus>) {
    clearTimeout(timer)
    const current = ++sequence
    pending.value = action
    if (action) error.value = null
    try {
      const view = await request()
      if (!controller.signal.aborted && current === sequence) {
        status.value = view
        error.value = null
      }
    } catch (cause) {
      if (!controller.signal.aborted && current === sequence) {
        error.value =
          cause instanceof ApiError && cause.localized
            ? tm(cause.localized)
            : t(updating.value ? 'updates.error.progress' : 'updates.error.check')
      }
    } finally {
      if (current === sequence) {
        pending.value = null
        poll(error.value ? 4000 : undefined)
      }
    }
  }

  function load() {
    return perform(status.value ? null : 'load', () => api.updateStatus(controller.signal))
  }

  function check() {
    return perform('check', () => api.checkForUpdate(controller.signal))
  }

  function save(preferences: UpdatePreferences, refresh: boolean) {
    if (busy.value) return
    return perform('save', async () => {
      const view = await api.saveUpdatePreferences(preferences, controller.signal)
      if (refresh && !controller.signal.aborted) {
        status.value = view
        return api.checkForUpdate(controller.signal)
      }
      return view
    })
  }

  function setChannel(value: unknown) {
    if (!status.value || (value !== 'stable' && value !== 'preview')) return
    void save({ ...status.value.preferences, channel: value }, true)
  }

  function setAutomatic(automatic: boolean) {
    if (status.value) void save({ ...status.value.preferences, automatic }, automatic)
  }

  function install() {
    if (!release.value) return
    const { version } = release.value
    void perform('install', () => api.installUpdate(version, controller.signal))
  }

  onMounted(load)
  onBeforeUnmount(() => {
    controller.abort()
    clearTimeout(timer)
  })

  return {
    status,
    pending,
    busy,
    updating,
    error,
    release,
    message,
    installable,
    installLabel,
    check,
    setChannel,
    setAutomatic,
    install,
    load,
  }
}
