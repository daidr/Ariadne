import { defineNodeType, drawCard } from '@dou-ui/flow'
import DecisionGroupNode from './DecisionGroupNode.vue'
import type { DecisionGroupData } from './decision-groups'

/**
 * Whiteboard group frame. Keeps standard container & collapse behavior for user groups
 * while rendering decision-group metadata (round, multi-select, note, selection status).
 */
export const decisionGroupNode = defineNodeType<DecisionGroupData>({
  type: 'group',
  component: DecisionGroupNode,
  container: true,
  resizable: { minWidth: 160, minHeight: 96 },
  size: { width: 320, height: 200 },
  defaults: () => ({ label: '' }),
  draw(node, draw) {
    const title = draw.style('dou-flow-group-node__title')
    const data = node.data
    const label = data?.label || draw.messages.group
    const prefix = data?.round ? `[R${data.round}] ` : ''
    return (
      drawCard(draw, ['is-container'].concat(data?.collapsed ? ['is-collapsed'] : [])) +
      draw.text(
        `${prefix}${label}`,
        { x: 12, y: 0, width: Math.max(0, draw.width - 24), height: 40 },
        {
          size: Number.parseFloat(title.fontSize) || 13,
          weight: title.fontWeight,
          color: draw.color(title.color),
          valign: 'middle',
          nowrap: true,
          lineHeight: 40,
        },
      )
    )
  },
})
