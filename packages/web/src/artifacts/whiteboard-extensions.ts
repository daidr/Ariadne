import { defineFlowExtension, flowKit } from '@dou-ui/flow'
import { whiteboardKit, type WhiteboardUpload } from '@dou-ui/flow/whiteboard'
import { optionNode } from './option-node'
import { pendingNode } from './pending-node'

/** Options the agent lays out for the user to compare and pick, and its placeholders. */
const options = defineFlowExtension({
  name: 'ariadne-options',
  nodeTypes: [optionNode, pendingNode],
})

/**
 * The editor parts a whiteboard is drawn with. Every node type registered here must pass
 * core's content check (see the test next to this). `upload` turns an image people add
 * into the address the board keeps.
 */
export function whiteboardExtensions(upload?: WhiteboardUpload) {
  return [flowKit(), whiteboardKit(upload ? { upload } : {}), options]
}
