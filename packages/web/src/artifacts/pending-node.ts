import { PENDING } from '@ariadne/core'
import { defineNodeType } from '@dou-ui/flow'
import PendingNode from './PendingNode.vue'

/** A placeholder on a whiteboard: what an agent will put at this place, drawn in grey. */
export interface PendingNodeData {
  [key: string]: unknown
  text?: string
}

export const pendingNode = defineNodeType<PendingNodeData>({
  type: PENDING,
  component: PendingNode,
  ports: 'sides',
  frame: false,
  resizable: { minWidth: 80, minHeight: 40 },
  size: { width: 160, height: 90 },
  defaults: () => ({ text: '' }),
  label: (node) => node.data?.text ?? '',
  draw(node, draw) {
    const style = draw.style('pending-node')
    const color = draw.color(style.color)
    return (
      `<rect x="0.5" y="0.5" width="${Math.max(0, draw.width - 1)}" height="${Math.max(0, draw.height - 1)}" rx="8" fill="none" stroke="${color}" stroke-width="1" stroke-dasharray="4 4"/>` +
      draw.text(
        node.data?.text ?? '',
        { x: 12, y: 8, width: Math.max(0, draw.width - 24), height: Math.max(0, draw.height - 16) },
        { size: Number.parseFloat(style.fontSize) || 13, color, align: 'center', valign: 'middle' },
      )
    )
  },
})
