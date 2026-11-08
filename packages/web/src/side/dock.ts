import type { DockInstance } from 'dou-ui'
import { shallowRef } from 'vue'
import { threadKey } from './layout'

/** The dock on the right side, once it is on the page. */
export const sideDock = shallowRef<DockInstance | null>(null)

/**
 * Let a thread shown elsewhere, a row of the thread list or a card in a message, be dragged
 * onto the side: onto a group's tabs, into a group, or to its edge for a group of its own.
 * A plain click still opens it as before.
 */
export function dragThread(event: PointerEvent, threadId: string) {
  if (event.button !== 0) return
  sideDock.value?.startDrop(event, { value: threadKey(threadId) })
}
