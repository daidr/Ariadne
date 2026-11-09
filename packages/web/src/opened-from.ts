import { inject, provide, type InjectionKey } from 'vue'
import { openView, type OpenedView, type OpenViewOptions } from './store'

/**
 * The thread whose conversation a link or button sits in, if one does: what it opens
 * shows full screen with that thread beside it.
 */
const fromThread: InjectionKey<() => string | null> = Symbol('fromThread')

export function provideFromThread(threadId: () => string | null) {
  provide(fromThread, threadId)
}

/** Open an artifact or changes the way the place it is opened from wants; see `openView`. */
export function useOpenView() {
  const from = inject(fromThread, () => null)
  return (view: OpenedView, options: OpenViewOptions = {}) =>
    openView(view, { from: from(), ...options })
}
