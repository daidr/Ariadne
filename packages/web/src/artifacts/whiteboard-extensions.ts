import { flowKit } from '@dou-ui/flow'
import { whiteboardKit } from '@dou-ui/flow/whiteboard'

/**
 * The editor parts a whiteboard is drawn with. Grouping is off: a group nests its nodes
 * with coordinates relative to it, which the agent's layout does not handle yet. Every
 * node type registered here must pass core's content check (see the test next to this).
 */
export function whiteboardExtensions() {
  return [flowKit({ group: false }), whiteboardKit()]
}
