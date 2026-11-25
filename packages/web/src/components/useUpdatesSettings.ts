import { computed, onBeforeUnmount, onMounted, shallowRef, watch } from 'vue'
import { api, ApiError, type RestartMode, type UpdatePreferences, type UpdateStatus } from '../api'
import { t, tm } from '../i18n'
import { applyUpdateStatus, onUpdateStatus, state } from '../store'
import {
  effectiveRestartPhase,
  hasActiveRestartWork,
  restartNowWarningMessage,
  restartStatusMessage,
} from '../update-restart'

export function useUpdatesSettings() {
  const status = shallowRef<UpdateStatus | null>(state.updateStatus)
  const pending = shallowRef<
    'load' | 'save' | 'check' | 'install' | 'restart' | 'restartNow' | 'cancelRestart' | null
  >(state.updateStatus ? null : 'load')
  const error = shallowRef<string | null>(null)
  const confirmingRestartNow = shallowRef(false)
  const controller = new AbortController()
  let sequence = 0
  let pushGeneration = 0
  let applyingLocal = false
  let stopListening: (() => void) | undefined

  function syncLocalStatus(view: UpdateStatus) {
    status.value = view
    applyingLocal = true
    try {
      applyUpdateStatus(view)
    } finally {
      applyingLocal = false
    }
  }

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
  const restartable = computed(
    () => status.value?.state === 'ready' && Boolean(status.value?.canRestart),
  )
  const restartPhase = computed(() => effectiveRestartPhase(status.value))
  const restarting = computed(
    () =>
      pending.value === 'restart' || pending.value === 'restartNow' || restartPhase.value !== '',
  )
  const hasActiveWork = computed(() => hasActiveRestartWork(status.value))
  const restartNowWarning = computed(() => restartNowWarningMessage(status.value))
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
    const phaseMessage = restartStatusMessage(view)
    if (phaseMessage) return phaseMessage
    if (view.state === 'no-release') {
      return t(view.preferences.channel === 'stable' ? 'updates.noStable' : 'updates.noRelease')
    }
    if (view.state === 'current') return t('updates.current')
    if (view.state === 'ready') {
      return t('updates.ready', { version: view.release?.version ?? '' })
    }
    return null
  })

  watch([restartPhase, hasActiveWork, restartable], ([nextPhase, active, canRestartNow]) => {
    if (nextPhase === 'restarting' || !active || !canRestartNow) {
      confirmingRestartNow.value = false
    }
  })

  async function perform(action: typeof pending.value, request: () => Promise<UpdateStatus>) {
    const current = ++sequence
    const startedPushGeneration = pushGeneration
    pending.value = action
    if (action) error.value = null
    try {
      const view = await request()
      if (
        !controller.signal.aborted &&
        current === sequence &&
        pushGeneration === startedPushGeneration
      ) {
        syncLocalStatus(view)
        error.value = null
      }
    } catch (cause) {
      if (!controller.signal.aborted && current === sequence) {
        if (action === 'restart' || action === 'restartNow') {
          const isBusinessError =
            cause instanceof ApiError &&
            cause.localized &&
            cause.status < 500 &&
            cause.status !== 499
          if (isBusinessError) {
            error.value = tm(cause.localized)
          } else if (status.value) {
            const next: UpdateStatus = {
              ...status.value,
              restarting: true,
              restartPhase: 'restarting',
            }
            syncLocalStatus(next)
            confirmingRestartNow.value = false
            error.value = null
          }
          return
        }
        error.value =
          cause instanceof ApiError && cause.localized
            ? tm(cause.localized)
            : t(updating.value ? 'updates.error.progress' : 'updates.error.check')
      }
    } finally {
      if (current === sequence) {
        pending.value = null
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
      const beforeSavePush = pushGeneration
      const view = await api.saveUpdatePreferences(preferences, controller.signal)
      if (refresh && !controller.signal.aborted) {
        if (pushGeneration === beforeSavePush) {
          syncLocalStatus(view)
        }
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

  function restart(mode: RestartMode = 'when-idle') {
    if (!restartable.value) return
    if (mode === 'when-idle' && restarting.value) return
    if (mode === 'now' && restartPhase.value === 'restarting') return
    confirmingRestartNow.value = false
    const action = mode === 'now' ? 'restartNow' : 'restart'
    void perform(action, () => api.restartToApplyUpdate(mode, controller.signal))
  }

  function requestRestartNow() {
    if (!restartable.value || restartPhase.value === 'restarting') return
    if (hasActiveWork.value && !confirmingRestartNow.value) {
      confirmingRestartNow.value = true
      error.value = null
      return
    }
    restart('now')
  }

  function dismissRestartNowConfirm() {
    confirmingRestartNow.value = false
  }

  function cancelRestart() {
    if (!restarting.value || pending.value === 'cancelRestart') return
    confirmingRestartNow.value = false
    void perform('cancelRestart', () => api.cancelRestart(controller.signal))
  }

  watch(
    () => state.stream,
    (next, previous) => {
      if (next === 'open' && previous === 'lost' && !controller.signal.aborted) {
        void load()
      }
    },
  )

  onMounted(() => {
    stopListening = onUpdateStatus((view) => {
      if (controller.signal.aborted || applyingLocal) return
      pushGeneration++
      status.value = view
      error.value = null
    })
    void load()
  })
  onBeforeUnmount(() => {
    controller.abort()
    stopListening?.()
    stopListening = undefined
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
    restartable,
    restartPhase,
    restarting,
    hasActiveWork,
    confirmingRestartNow,
    restartNowWarning,
    check,
    setChannel,
    setAutomatic,
    install,
    restart,
    requestRestartNow,
    dismissRestartNowConfirm,
    cancelRestart,
    load,
  }
}
