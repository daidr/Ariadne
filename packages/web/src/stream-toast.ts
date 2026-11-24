import { toast, type ToastHandle, type ToastOptions } from 'dou-ui'
import { onScopeDispose, watch } from 'vue'
import type { StreamStatus } from './api'
import { locale, t } from './i18n'
import { state } from './store'

function lostOptions(): ToastOptions {
  return {
    message: t('error.connection'),
    tone: 'warning',
    loading: true,
    duration: 0,
    closable: false,
    action: undefined,
  }
}

/**
 * Mirror connection drops into a single persistent loading toast instead of a top banner,
 * so the workspace layout never shifts. When the connection recovers (or becomes
 * unauthorized, which has its own full-page screen), the loading toast is simply dismissed
 * without popping a confirmation toast over the UI.
 */
export function useStreamStatusToast() {
  let handle: ToastHandle | null = null

  function showOrUpdate() {
    const patch = lostOptions()
    if (handle) {
      handle.update(patch)
      return
    }
    let created: ToastHandle | null = null
    const onClose = () => {
      if (created && handle?.id === created.id) {
        handle = null
      }
    }
    const { message, ...extra } = patch
    created = toast.loading(message, { ...extra, onClose })
    handle = created
  }

  function close() {
    if (!handle) return
    const current = handle
    handle = null
    current.close()
  }

  function sync(status: StreamStatus) {
    if (status === 'lost') {
      showOrUpdate()
      return
    }
    close()
  }

  watch(() => state.stream, sync, { immediate: true, flush: 'post' })
  watch(locale, () => {
    if (handle) showOrUpdate()
  })
  onScopeDispose(close)
}
