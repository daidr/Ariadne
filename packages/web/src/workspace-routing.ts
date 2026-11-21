import { onMounted, onScopeDispose, watch } from 'vue'
import { useRouter, type RouteLocationNormalized, type Router } from 'vue-router'
import { captureWorkspace, report, restoreWorkspace, start } from './store'
import { workspaceAddress, workspaceLocation, type WorkspaceSnapshot } from './workspace-route'

const HISTORY_KEY = 'ariadneWorkspace'
const snapshotWithoutSplitWidths = (snapshot: WorkspaceSnapshot) =>
  JSON.stringify(snapshot, function (key, value) {
    return key === 'size' && this.type === 'split' ? undefined : value
  })

export function connectWorkspaceRouter(router: Router) {
  const history = router.options.history
  let ready = false
  let stopped = false
  let restoring: symbol | undefined
  let writing: string | undefined
  let pendingResize: { path: string; entry: string } | undefined
  let resizeTimer: ReturnType<typeof setTimeout> | undefined

  function clearResize() {
    clearTimeout(resizeTimer)
    resizeTimer = undefined
    pendingResize = undefined
  }

  function saveResize() {
    const next = pendingResize
    clearResize()
    if (next?.path === history.location) history.replace(next.path, { [HISTORY_KEY]: next.entry })
  }

  function write(replace = false) {
    if (stopped || !ready || restoring || writing) return
    const { location, snapshot } = captureWorkspace()
    const address = workspaceAddress(location)
    const path = router.resolve(address).fullPath
    const entry = JSON.stringify({ path, snapshot })
    if (path === router.currentRoute.value.fullPath) {
      if (history.state[HISTORY_KEY] === entry) {
        clearResize()
        return
      }
      const saved = savedFor(path)
      // Splitters can emit on every pointermove. Tabs and other state still save immediately.
      if (saved && snapshotWithoutSplitWidths(saved) === snapshotWithoutSplitWidths(snapshot)) {
        pendingResize = { path, entry }
        resizeTimer ??= setTimeout(saveResize, 400)
        return
      }
      clearResize()
      history.replace(path, { [HISTORY_KEY]: entry })
      return
    }
    saveResize()
    writing = path
    const route = { ...address, replace, state: { [HISTORY_KEY]: entry } }
    void router.push(route).then(
      (failure) => {
        writing = undefined
        if (!failure) write()
      },
      (error) => {
        writing = undefined
        report(error)
      },
    )
  }

  function savedFor(path: string): WorkspaceSnapshot | undefined {
    const entry = history.state[HISTORY_KEY]
    if (typeof entry !== 'string') return
    try {
      const saved = JSON.parse(entry) as { path?: string; snapshot?: WorkspaceSnapshot }
      if (saved?.path === path) return saved.snapshot
    } catch {
      // The URL still opens when the browser no longer has the saved layout.
    }
  }

  async function restore(route = router.currentRoute.value as RouteLocationNormalized) {
    if (stopped) return
    ready = true
    const current = (restoring = Symbol())
    try {
      await restoreWorkspace(workspaceLocation(route), savedFor(route.fullPath))
    } finally {
      if (current === restoring) {
        restoring = undefined
        write(true)
      }
    }
  }

  const stopWatching = watch(
    () => JSON.stringify(captureWorkspace()),
    () => write(),
    { flush: 'post' },
  )
  const stopRouting = router.afterEach((to, _from, failure) => {
    if (ready && !failure && to.fullPath !== writing) void restore(to).catch(report)
  })
  if (typeof window !== 'undefined') window.addEventListener('pagehide', saveResize)
  return {
    restore,
    stop() {
      stopped = true
      saveResize()
      if (typeof window !== 'undefined') window.removeEventListener('pagehide', saveResize)
      stopWatching()
      stopRouting()
    },
  }
}

export function useWorkspaceRouting() {
  const router = useRouter()
  const binding = connectWorkspaceRouter(router)
  onMounted(() => void start(binding.restore))
  onScopeDispose(binding.stop)
}
