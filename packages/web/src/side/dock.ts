import type { DockInstance } from 'dou-ui'
import { shallowRef } from 'vue'
import { artifactKey, threadKey } from './layout'

/** The dock on the right side, once it is on the page. */
export const sideDock = shallowRef<DockInstance | null>(null)

function canDrag(event: PointerEvent) {
  return (
    event.button === 0 &&
    !event.ctrlKey &&
    !event.metaKey &&
    !event.shiftKey &&
    !event.altKey &&
    event.detail <= 1
  )
}

/**
 * Let a thread shown elsewhere, a row of the thread list, a card, or a link in a message, be
 * dragged onto the side: onto a group's tabs, into a group, or to its edge for a group of its
 * own. A plain click still opens it as before.
 */
export function dragThread(event: PointerEvent, threadId: string) {
  if (!canDrag(event)) return
  sideDock.value?.startDrop(event, { value: threadKey(threadId) })
}

/**
 * Let an artifact shown elsewhere, a card in the artifact list or overview or a link in a
 * message, be dragged onto the side: onto a group's tabs, into a group, or to its edge for a
 * group of its own. A plain click still opens it as before.
 */
export function dragArtifact(event: PointerEvent, artifactId: string) {
  if (!canDrag(event)) return
  sideDock.value?.startDrop(event, { value: artifactKey(artifactId) })
}
