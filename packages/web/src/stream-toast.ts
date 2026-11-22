import { toast, type ToastHandle, type ToastOptions } from 'dou-ui'
import { onScopeDispose, watch } from 'vue'
import type { StreamStatus } from './api'
import { locale, t } from './i18n'
import { state } from './store'

/** How long the recovery confirmation stays before dismissing itself. */
export const RECONNECTED_DURATION_MS = 2500

type StreamToastKind = 'lost' | 'reconnected'

/**
 * Mirror connection drops and recoveries into a single persistent toast instead of a top
 * banner, so the workspace layout never shifts. Unauthorized state has its own full-page
 * error screen, so any active connection toast is dismissed instead of stacking on top.
 */
export function useStreamStatusToast() {
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
    if (status === 'open') {
      if (handle && kind === 'lost') {
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
