import { flowKit } from '@dou-ui/flow'
import { whiteboardKit, type WhiteboardUpload } from '@dou-ui/flow/whiteboard'

/**
 * The editor parts a whiteboard is drawn with. Grouping is off: a group nests its nodes
 * with coordinates relative to it, which the agent's layout does not handle yet. Every
 * node type registered here must pass core's content check (see the test next to this).
 * `upload` turns an image people add into the address the board keeps.
 */
export function whiteboardExtensions(upload?: WhiteboardUpload) {
  return [flowKit({ group: false }), whiteboardKit(upload ? { upload } : {})]
}
