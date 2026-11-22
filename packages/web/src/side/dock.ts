import type { DockInstance } from 'dou-ui'
import { shallowRef } from 'vue'
import { artifactKey, threadKey } from './layout'

/** The dock on the right side, once it is on the page. */
export const sideDock = shallowRef<DockInstance | null>(null)

const MULTI_CLICK_MS = 400
const MULTI_CLICK_DISTANCE = 8

let lastPress: {
  time: number
  x: number
  y: number
  pointerType: string
  target: EventTarget
} | null = null

function isRapidRepeat(event: PointerEvent) {
  const time = performance.now()
  const target = event.currentTarget ?? event.target
  if (!target) {
    lastPress = null
    return false
  }
  const repeat =
    lastPress !== null &&
    lastPress.target === target &&
    lastPress.pointerType === event.pointerType &&
    time - lastPress.time <= MULTI_CLICK_MS &&
    Math.hypot(event.clientX - lastPress.x, event.clientY - lastPress.y) <= MULTI_CLICK_DISTANCE
  lastPress = {
    time,
    x: event.clientX,
    y: event.clientY,
    pointerType: event.pointerType,
    target,
  }
  return repeat
}

function canDrag(event: PointerEvent) {
  if (event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) {
    return false
  }
  return !isRapidRepeat(event)
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
