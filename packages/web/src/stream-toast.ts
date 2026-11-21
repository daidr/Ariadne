import { toast, type ToastHandle, type ToastOptions } from 'dou-ui'
import { onScopeDispose, watch } from 'vue'
import type { StreamStatus } from './api'
import { locale, t } from './i18n'
import { state } from './store'

/** How long the recovery confirmation stays before dismissing itself. */
export const RECONNECTED_DURATION_MS = 2500

type StreamToastKind = 'lost' | 'unauthorized' | 'reconnected'

export interface StreamStatusToastOptions {
  /** Override the page reload action (primarily for tests). */
  reload?: () => void
}

/**
 * Mirror `state.stream` into a single persistent toast instead of a top banner,
 * so connection drops and recoveries never shift the workspace layout.
 */
export function useStreamStatusToast(options: StreamStatusToastOptions = {}) {
  const reload = options.reload ?? (() => window.location.reload())

  let handle: ToastHandle | null = null
  let kind: StreamToastKind | null = null

  function optionsFor(next: StreamToastKind): ToastOptions {
    if (next === 'lost') {
      return {
        message: t('error.connection'),
        tone: 'warning',
        loading: true,
        duration: 0,
        closable: false,
        action: undefined,
      }
    }
    if (next === 'unauthorized') {
      return {
        message: t('error.unauthorized'),
        tone: 'danger',
        loading: false,
        duration: 0,
        closable: false,
        action: { label: t('error.reload'), onClick: reload },
      }
    }
    return {
      message: t('error.reconnected'),
      tone: 'success',
      loading: false,
      duration: RECONNECTED_DURATION_MS,
      closable: false,
      action: undefined,
    }
  }

  function showOrUpdate(next: StreamToastKind) {
    const patch = optionsFor(next)
    if (handle) {
      kind = next
      handle.update(patch)
      return
    }
    let created: ToastHandle | null = null
    const onClose = () => {
      if (created && handle?.id === created.id) {
        handle = null
        kind = null
      }
    }
    const { message, ...extra } = patch
    created =
      next === 'lost'
        ? toast.loading(message, { ...extra, onClose })
        : next === 'unauthorized'
          ? toast.danger(message, { ...extra, onClose })
          : toast.success(message, { ...extra, onClose })
    handle = created
    kind = next
  }

  function close() {
    if (!handle) return
    const current = handle
    handle = null
    kind = null
    current.close()
  }

  function sync(status: StreamStatus) {
    if (status === 'lost') {
      showOrUpdate('lost')
      return
    }
    if (status === 'unauthorized') {
      showOrUpdate('unauthorized')
      return
    }
    if (status === 'open') {
      if (handle && (kind === 'lost' || kind === 'unauthorized')) {
        showOrUpdate('reconnected')
      }
      return
    }
    close()
  }

  watch(() => state.stream, sync, { immediate: true, flush: 'post' })
  watch(locale, () => {
    if (handle && kind) showOrUpdate(kind)
  })
  onScopeDispose(close)
}
