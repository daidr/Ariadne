import type { BranchChanges } from '@ariadne/plugin-sdk'
import { computed, onMounted, onScopeDispose, shallowRef, watch } from 'vue'
import { api, ApiError } from '../api'
import { tm } from '../i18n'

export function sameComparison(before: BranchChanges | null, after: BranchChanges | null) {
  return (
    before === after ||
    (!!before &&
      !!after &&
      before.branch === after.branch &&
      before.base === after.base &&
      before.from === after.from &&
      before.head === after.head)
  )
}

/** Keep the comparison being read in place until the user opens the newer one. */
export function useBranchUpdates(threadId: () => string | null, visible: () => boolean) {
  type Snapshot = Awaited<ReturnType<typeof api.getChanges>>
  const shown = shallowRef<Snapshot | null>(null)
  const newer = shallowRef<Snapshot | null>(null)
  const problem = shallowRef<string | null>(null)
  const loading = shallowRef(true)
  const mergeable = shallowRef(false)
  let controller: AbortController | null = null
  let timer: ReturnType<typeof setInterval> | undefined
  let needsFullCheck = false

  async function check(full = false) {
    const id = threadId()
    if (!id || (!full && (document.hidden || !visible()))) return
    if (controller) {
      needsFullCheck ||= full
      return
    }
    const request = new AbortController()
    controller = request
    const last = (newer.value ?? shown.value)?.changes
    const known = !full && last?.baseHead ? { head: last.head, baseHead: last.baseHead } : undefined
    try {
      const next = await api.getChanges(id, known, request.signal)
      if (request.signal.aborted) return
      mergeable.value = next.mergeable
      problem.value = null
      if (next.unchanged) return
      if (!shown.value || sameComparison(shown.value.changes, next.changes)) {
        shown.value = next
        newer.value = null
      } else {
        newer.value = next
      }
    } catch (error) {
      // A background failure leaves the last successful comparison available.
      if (!request.signal.aborted && !shown.value) {
        problem.value = error instanceof ApiError ? tm(error.localized) : String(error)
      }
    } finally {
      if (controller === request) {
        controller = null
        loading.value = false
        if (needsFullCheck) {
          needsFullCheck = false
          void check(true)
        }
      }
    }
  }

  function showLatest() {
    if (!newer.value) return
    shown.value = newer.value
    newer.value = null
  }

  watch(
    threadId,
    (id) => {
      controller?.abort()
      controller = null
      needsFullCheck = false
      shown.value = null
      newer.value = null
      problem.value = null
      loading.value = !!id
      mergeable.value = false
      if (id) void check(true)
    },
    { immediate: true },
  )

  const poll = () => {
    void check()
  }
  const resume = () => {
    if (!controller && !document.hidden && visible()) void check(true)
  }
  onMounted(() => {
    timer = setInterval(poll, 12_000)
    window.addEventListener('focus', resume)
    document.addEventListener('visibilitychange', resume)
  })
  onScopeDispose(() => {
    controller?.abort()
    controller = null
    needsFullCheck = false
    clearInterval(timer)
    window.removeEventListener('focus', resume)
    document.removeEventListener('visibilitychange', resume)
  })

  return {
    changes: computed(() => shown.value?.changes ?? null),
    hasUpdate: computed(() => newer.value !== null),
    loading,
    mergeable,
    problem,
    check,
    showLatest,
  }
}
