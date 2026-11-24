import { defineFlowExtension, flowKit } from '@dou-ui/flow'
import { whiteboardKit, type WhiteboardUpload } from '@dou-ui/flow/whiteboard'
import { decisionGroupNode } from './decision-group-node'
import { optionNode } from './option-node'
import { pendingNode } from './pending-node'
import { prototypeNode } from './prototype-node'

/** Options the agent lays out for the user to compare and pick, prototypes, and placeholders. */
const options = defineFlowExtension({
  name: 'ariadne-options',
  nodeTypes: [optionNode, pendingNode, prototypeNode],
})

/**
 * The editor parts a whiteboard is drawn with. Every node type registered here must pass
 * core's content check (see the test next to this). `upload` turns an image people add
 * into the address the board keeps.
 */
export function whiteboardExtensions(upload?: WhiteboardUpload) {
  const kit = flowKit().map((extension) =>
    extension.name === 'groups' ? { ...extension, nodeTypes: [decisionGroupNode] } : extension,
  )
  return [kit, whiteboardKit(upload ? { upload } : {}), options]
}
